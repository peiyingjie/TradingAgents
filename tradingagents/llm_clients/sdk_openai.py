"""Native OpenAI/Azure SDK transport for Chat Completions and Responses."""

from __future__ import annotations

import json
import os
import re

from pydantic import SecretStr

from tradingagents.runtime.callbacks import ChatGeneration, ChatResult
from tradingagents.runtime.messages import AIMessage, ToolMessage, to_messages

from .chat_model import ChatModel, parsed_calls, plain_dict, usage


def chat_messages(messages):
    result = []
    for message in messages:
        role = {"human": "user", "ai": "assistant"}.get(message.type, message.type)
        item = {"role": role, "content": message.content}
        if message.name:
            item["name"] = message.name
        if isinstance(message, ToolMessage):
            item["tool_call_id"] = message.tool_call_id
        if isinstance(message, AIMessage) and message.tool_calls:
            item["content"] = message.content or None
            item["tool_calls"] = [
                {
                    "type": "function",
                    "id": call["id"],
                    "function": {
                        "name": call["name"],
                        "arguments": json.dumps(call["args"], ensure_ascii=False),
                    },
                }
                for call in message.tool_calls
            ]
        result.append(item)
    return result


class ChatOpenAI(ChatModel):
    def __init__(self, model, **kwargs):
        super().__init__(model, **kwargs)
        self.use_responses_api = kwargs.get("use_responses_api", False)
        self.openai_api_base = kwargs.get("base_url")
        self.openai_api_key = SecretStr(
            kwargs.get("api_key") or os.environ.get("OPENAI_API_KEY", "")
        )
        self.reasoning_effort = kwargs.get("reasoning_effort")

    def _sdk(self):
        if self.client is None:
            from openai import OpenAI

            options = {
                k: self.options[k]
                for k in ("api_key", "base_url", "timeout", "max_retries", "http_client")
                if k in self.options
            }
            self.client = OpenAI(**options)
        return self.client

    def with_structured_output(self, schema, *, method=None, **kwargs):
        if (method or self.structured_method) == "function_calling":
            kwargs.setdefault("parallel_tool_calls", False)
        return super().with_structured_output(schema, method=method, **kwargs)

    def _get_request_payload(self, input_, *, stop=None, **kwargs):
        payload = {"model": self.model_name, "messages": chat_messages(to_messages(input_))}
        for key in ("temperature", "max_tokens", "reasoning_effort"):
            value = getattr(self, key, None)
            if value is not None:
                payload[key] = value
        if "max_tokens" in payload:
            payload["max_completion_tokens"] = payload.pop("max_tokens")
        # Match the existing adapter's temperature restrictions for reasoning models.
        model = self.model_name.lower()
        if model.startswith("o1") and "temperature" not in self.options:
            payload["temperature"] = 1
        elif (
            model.startswith("gpt-5")
            and "chat" not in model
            and self.reasoning_effort != "none"
            and self.temperature != 1
        ):
            payload.pop("temperature", None)
        if stop is not None:
            payload["stop"] = stop
        payload.update(kwargs)
        if re.match(r"^o\d", self.model_name):
            for message in payload["messages"]:
                if message["role"] == "system":
                    message["role"] = "developer"
        choice = payload.get("tool_choice")
        if choice is None:
            payload.pop("tool_choice", None)
        elif isinstance(choice, str) and choice not in ("auto", "none", "required"):
            payload["tool_choice"] = {"type": "function", "function": {"name": choice}}
        return payload

    def _create_chat_result(self, response, generation_info=None):
        response = plain_dict(response)
        if response.get("error"):
            raise ValueError(response["error"])
        generations = []
        tokens = response.get("usage") or {}
        for choice in response.get("choices", []):
            message = choice["message"]
            calls, invalid = parsed_calls(message.get("tool_calls") or [])
            ai = AIMessage(
                message.get("content") or "",
                id=response.get("id"),
                tool_calls=calls,
                invalid_tool_calls=invalid,
                response_metadata={
                    "model_name": response.get("model"),
                    "finish_reason": choice.get("finish_reason"),
                },
                usage_metadata=usage(
                    tokens.get("prompt_tokens", 0), tokens.get("completion_tokens", 0)
                ),
            )
            generations.append(ChatGeneration(ai))
        if not generations:
            raise ValueError("Provider returned no completion choices")
        return ChatResult(generations)

    def _responses_payload(self, messages, payload):
        payload = dict(payload)
        payload.pop("messages")
        payload.pop("stop", None)
        items = []
        for message in messages:
            raw_output = message.additional_kwargs.get("responses_output")
            if isinstance(message, AIMessage) and raw_output is not None:
                items.extend(raw_output)
            elif isinstance(message, ToolMessage):
                items.append(
                    {
                        "type": "function_call_output",
                        "call_id": message.tool_call_id,
                        "output": message.content,
                    }
                )
            elif isinstance(message, AIMessage):
                if message.content:
                    items.append(
                        {"type": "message", "role": "assistant", "content": message.content}
                    )
                items.extend(
                    {
                        "type": "function_call",
                        "call_id": call["id"],
                        "name": call["name"],
                        "arguments": json.dumps(call["args"], ensure_ascii=False),
                    }
                    for call in message.tool_calls
                )
            else:
                items.extend({"type": "message", **item} for item in chat_messages([message]))
        payload["input"] = items
        if "max_completion_tokens" in payload:
            payload["max_output_tokens"] = payload.pop("max_completion_tokens")
        if "reasoning_effort" in payload:
            payload["reasoning"] = {"effort": payload.pop("reasoning_effort")}
        if (
            self.model.startswith("gpt-5")
            and "chat" not in self.model
            and (payload.get("reasoning") or {}).get("effort") != "none"
        ):
            payload.pop("temperature", None)
        if "tools" in payload:
            payload["tools"] = [{"type": "function", **t["function"]} for t in payload["tools"]]
        choice = payload.get("tool_choice")
        if isinstance(choice, dict):
            payload["tool_choice"] = {"type": "function", "name": choice["function"]["name"]}
        response_format = payload.pop("response_format", None)
        if response_format:
            if response_format["type"] == "json_schema":
                response_format = {"type": "json_schema", **response_format["json_schema"]}
            payload["text"] = {"format": response_format}
        return payload

    def _generate(self, messages, **kwargs):
        payload = self._get_request_payload(messages, **kwargs)
        if not self.use_responses_api:
            response = self._sdk().chat.completions.create(**payload)
            return self._create_chat_result(response).generations[0].message
        response = plain_dict(
            self._sdk().responses.create(**self._responses_payload(messages, payload))
        )
        if response.get("error"):
            raise ValueError(response["error"])
        content, calls = [], []
        output = response.get("output", [])
        for item in output:
            if item["type"] == "message":
                content.extend(
                    {"type": "text", "text": block["text"]}
                    for block in item.get("content", [])
                    if block["type"] == "output_text"
                )
            elif item["type"] == "function_call":
                calls.append(
                    {
                        "id": item["call_id"],
                        "function": {"name": item["name"], "arguments": item["arguments"]},
                    }
                )
        valid, invalid = parsed_calls(calls)
        tokens = response.get("usage") or {}
        return AIMessage(
            content,
            id=response.get("id"),
            tool_calls=valid,
            invalid_tool_calls=invalid,
            additional_kwargs={"responses_output": output},
            response_metadata={
                "model_name": response.get("model"),
                "status": response.get("status"),
            },
            usage_metadata=usage(tokens.get("input_tokens", 0), tokens.get("output_tokens", 0)),
        )


class AzureChatOpenAI(ChatOpenAI):
    def _sdk(self):
        if self.client is None:
            from openai import AzureOpenAI

            options = {
                k: self.options[k]
                for k in ("api_key", "timeout", "max_retries", "http_client")
                if k in self.options
            }
            self.client = AzureOpenAI(
                azure_deployment=self.azure_deployment,
                azure_endpoint=os.environ.get("AZURE_OPENAI_ENDPOINT"),
                api_version=os.environ.get("OPENAI_API_VERSION"),
                **options,
            )
        return self.client
