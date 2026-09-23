"""Optional boto3 Converse transport, with AWS credentials or bearer auth."""

from __future__ import annotations

import json

from tradingagents.runtime.messages import AIMessage, SystemMessage, ToolMessage

from .chat_model import ChatModel, usage


def bedrock_messages(messages):
    system, history = [], []
    for message in messages:
        if isinstance(message, SystemMessage):
            system.append({"text": message.content})
            continue
        role = "assistant" if isinstance(message, AIMessage) else "user"
        if isinstance(message, ToolMessage):
            try:
                output = {"json": json.loads(message.content)}
            except (ValueError, TypeError):
                output = {"text": message.content}
            blocks = [
                {
                    "toolResult": {
                        "toolUseId": message.tool_call_id,
                        "content": [output],
                        "status": message.status,
                    }
                }
            ]
        elif isinstance(message, AIMessage) and "bedrock_content" in message.additional_kwargs:
            blocks = message.additional_kwargs["bedrock_content"]
        else:
            blocks = [{"text": message.content}] if message.content else []
            if isinstance(message, AIMessage):
                blocks += [
                    {
                        "toolUse": {
                            "toolUseId": call["id"],
                            "name": call["name"],
                            "input": call["args"],
                        }
                    }
                    for call in message.tool_calls
                ]
        if history and history[-1]["role"] == role:
            history[-1]["content"].extend(blocks)
        else:
            history.append({"role": role, "content": list(blocks)})
    return system, history


class ChatBedrockConverse(ChatModel):
    def _sdk(self):
        if self.client is None:
            import boto3
            from botocore.config import Config
            from botocore.session import get_session
            from botocore.tokens import FrozenAuthToken

            config = {}
            if "max_retries" in self.options:
                config["retries"] = {"max_attempts": self.max_retries, "mode": "standard"}
            token = self.options.get("api_key")
            if token:

                class TokenProvider:
                    def load_token(self, **kwargs):
                        return FrozenAuthToken(token)

                session = get_session()
                session.register_component("token_provider", TokenProvider())
                session = boto3.Session(botocore_session=session)
                config["auth_scheme_preference"] = "httpBearerAuth"
            else:
                session = boto3.Session()
            self.client = session.client(
                "bedrock-runtime", region_name=self.region_name, config=Config(**config)
            )
        return self.client

    def with_structured_output(self, schema, *, method=None, **kwargs):
        # Converse exposes forced tool choice only for certain model families.
        if "claude" in self.model or "nova" in self.model:
            choice = schema.__name__
        elif "mistral-large" in self.model:
            choice = "any"
        else:
            choice = None
        kwargs.setdefault("tool_choice", choice)
        return super().with_structured_output(schema, method=method, **kwargs)

    def _get_request_payload(self, messages, **kwargs):
        system, history = bedrock_messages(messages)
        payload = {"modelId": self.model, "messages": history}
        if system:
            payload["system"] = system
        inference = {}
        if self.max_tokens is not None:
            inference["maxTokens"] = self.max_tokens
        if self.temperature is not None:
            inference["temperature"] = self.temperature
        if inference:
            payload["inferenceConfig"] = inference
        tools = kwargs.pop("tools", None)
        choice = kwargs.pop("tool_choice", None)
        if tools:
            tool_config = {
                "tools": [
                    {
                        "toolSpec": {
                            "name": t["function"]["name"],
                            "description": t["function"]["description"],
                            "inputSchema": {"json": t["function"]["parameters"]},
                        }
                    }
                    for t in tools
                ]
            }
            if choice:
                tool_config["toolChoice"] = (
                    {choice: {}} if choice in ("auto", "any") else {"tool": {"name": choice}}
                )
            payload["toolConfig"] = tool_config
        if "response_format" in kwargs:
            raise NotImplementedError("Use function_calling structured output for Bedrock")
        return {**payload, **kwargs}

    def _generate(self, messages, **kwargs):
        response = self._sdk().converse(**self._get_request_payload(messages, **kwargs))
        blocks = response["output"]["message"]["content"]
        content = [{"type": "text", "text": block["text"]} for block in blocks if "text" in block]
        calls = [
            {
                "id": b["toolUse"]["toolUseId"],
                "name": b["toolUse"]["name"],
                "args": b["toolUse"]["input"],
            }
            for b in blocks
            if "toolUse" in b
        ]
        tokens = response.get("usage") or {}
        return AIMessage(
            content,
            tool_calls=calls,
            additional_kwargs={"bedrock_content": blocks},
            response_metadata={"stop_reason": response.get("stopReason")},
            usage_metadata=usage(tokens.get("inputTokens", 0), tokens.get("outputTokens", 0)),
        )
