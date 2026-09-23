"""Gemini generateContent adapter; tool calls are executed only by our graph."""

from __future__ import annotations

import json
import os
import re
import warnings
from uuid import uuid4

from tradingagents.runtime.messages import AIMessage, SystemMessage, ToolMessage

from .chat_model import ChatModel, plain_dict, usage


def google_messages(messages):
    system, contents, names = [], [], {}
    for message in messages:
        if isinstance(message, SystemMessage):
            system.append(message.content)
            continue
        if isinstance(message, AIMessage):
            names.update({call["id"]: call["name"] for call in message.tool_calls})
        role = "model" if isinstance(message, AIMessage) else "user"
        if isinstance(message, ToolMessage):
            try:
                output = json.loads(message.content)
            except (ValueError, TypeError):
                output = {"output": message.content}
            if not isinstance(output, dict):
                output = {"output": output}
            parts = [
                {
                    "function_response": {
                        "name": message.name or names[message.tool_call_id],
                        "response": output,
                    }
                }
            ]
        elif isinstance(message, AIMessage) and "google_parts" in message.additional_kwargs:
            parts = message.additional_kwargs["google_parts"]
        else:
            parts = [{"text": message.content}] if message.content else []
            if isinstance(message, AIMessage):
                parts += [
                    {"function_call": {"name": call["name"], "args": call["args"]}}
                    for call in message.tool_calls
                ]
        if contents and contents[-1]["role"] == role:
            contents[-1]["parts"].extend(parts)
        else:
            contents.append({"role": role, "parts": list(parts)})
    return "\n\n".join(system), contents


class ChatGoogleGenerativeAI(ChatModel):
    structured_method = "json_schema"

    def __init__(self, model, **kwargs):
        kwargs.setdefault("max_retries", 6)
        super().__init__(model, **kwargs)
        if self.temperature is not None and not 0 <= self.temperature <= 2:
            raise ValueError("temperature must be in the range [0.0, 2.0]")

    def with_structured_output(self, schema, *, method=None, **kwargs):
        bound = super().with_structured_output(schema, method=method, **kwargs)
        if (method or self.structured_method) == "json_schema":
            bound.kwargs["response_format"]["json_schema"]["schema"] = schema.model_json_schema()
        return bound

    def _sdk(self):
        if self.client is None:
            from google import genai

            options = {}
            if self.options.get("base_url"):
                options["base_url"] = self.options["base_url"]
            if self.options.get("timeout") is not None:
                options["timeout"] = int(self.options["timeout"] * 1000)
            if "max_retries" in self.options:
                options["retry_options"] = {"attempts": self.max_retries}
            if self.options.get("http_client") is not None:
                options["httpx_client"] = self.options["http_client"]
            self.client = genai.Client(
                api_key=self.options.get("google_api_key")
                or os.environ.get("GOOGLE_API_KEY")
                or os.environ.get("GEMINI_API_KEY"),
                http_options=options,
            )
        return self.client

    def _get_request_payload(self, messages, **kwargs):
        system, contents = google_messages(messages)
        config = {"automatic_function_calling": {"disable": True}}
        if system:
            config["system_instruction"] = system
        for key in ("temperature", "max_output_tokens"):
            if self.options.get(key) is not None:
                config[key] = self.options[key]
        model = re.sub(r"-\d{3}$", "", self.model.lower().rsplit("/", 1)[-1])
        if model in ("gemini-3.5-flash-lite", "gemini-3.6-flash") and "temperature" in config:
            warnings.warn(
                f"Model '{self.model}' uses fixed sampling defaults; temperature will be ignored.",
                UserWarning,
                stacklevel=2,
            )
            config.pop("temperature")
        if self.options.get("thinking_level"):
            config["thinking_config"] = {"thinking_level": self.options["thinking_level"]}
        tools = kwargs.pop("tools", None)
        if tools:
            config["tools"] = [
                {
                    "function_declarations": [
                        {
                            "name": t["function"]["name"],
                            "description": t["function"]["description"],
                            "parameters_json_schema": t["function"]["parameters"],
                        }
                        for t in tools
                    ]
                }
            ]
        choice = kwargs.pop("tool_choice", None)
        if choice:
            calling = (
                {"mode": choice.upper()}
                if choice in ("auto", "any", "none")
                else {"mode": "ANY", "allowed_function_names": [choice]}
            )
            config["tool_config"] = {"function_calling_config": calling}
        format_ = kwargs.pop("response_format", None)
        if format_:
            config["response_mime_type"] = "application/json"
            if format_["type"] == "json_schema":
                config["response_json_schema"] = format_["json_schema"]["schema"]
        return {"model": self.model, "contents": contents, "config": {**config, **kwargs}}

    def _generate(self, messages, **kwargs):
        response = plain_dict(
            self._sdk().models.generate_content(**self._get_request_payload(messages, **kwargs))
        )
        candidates = response.get("candidates") or []
        if not candidates:
            raise ValueError(f"Gemini returned no candidates: {response.get('prompt_feedback')}")
        parts = candidates[0].get("content", {}).get("parts", [])
        content, calls = [], []
        for part in parts:
            if part.get("text") and not part.get("thought"):
                content.append({"type": "text", "text": part["text"]})
            if part.get("function_call"):
                call = part["function_call"]
                calls.append(
                    {
                        "id": call.get("id") or str(uuid4()),
                        "name": call["name"],
                        "args": call.get("args") or {},
                    }
                )
        tokens = response.get("usage_metadata") or {}
        return AIMessage(
            content,
            id=response.get("response_id"),
            tool_calls=calls,
            additional_kwargs={"google_parts": parts},
            response_metadata={"finish_reason": candidates[0].get("finish_reason")},
            usage_metadata=usage(
                tokens.get("prompt_token_count", 0),
                tokens.get("candidates_token_count", 0) + tokens.get("thoughts_token_count", 0),
            ),
        )
