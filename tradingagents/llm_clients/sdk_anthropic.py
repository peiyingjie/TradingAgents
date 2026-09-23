"""Anthropic Messages API, including signed thinking and tool-use blocks."""

from __future__ import annotations

import os

from tradingagents.runtime.messages import AIMessage, SystemMessage, ToolMessage

from .chat_model import ChatModel, plain_dict, usage


def anthropic_messages(messages):
    system, history = [], []
    for message in messages:
        if isinstance(message, SystemMessage):
            system.append(message.content)
            continue
        role = "assistant" if isinstance(message, AIMessage) else "user"
        if isinstance(message, ToolMessage):
            blocks = [
                {
                    "type": "tool_result",
                    "tool_use_id": message.tool_call_id,
                    "content": message.content,
                    "is_error": message.status == "error",
                }
            ]
        elif isinstance(message, AIMessage) and "anthropic_content" in message.additional_kwargs:
            blocks = message.additional_kwargs["anthropic_content"]
        else:
            blocks = (
                ([{"type": "text", "text": message.content}] if message.content else [])
                if isinstance(message.content, str)
                else list(message.content)
            )
            if isinstance(message, AIMessage):
                blocks += [
                    {
                        "type": "tool_use",
                        "id": call["id"],
                        "name": call["name"],
                        "input": call["args"],
                    }
                    for call in message.tool_calls
                ]
        if (
            isinstance(message.content, str)
            and not isinstance(message, ToolMessage)
            and not (
                isinstance(message, AIMessage)
                and (message.tool_calls or "anthropic_content" in message.additional_kwargs)
            )
        ):
            blocks = message.content
        if history and history[-1]["role"] == role:
            if isinstance(history[-1]["content"], str):
                history[-1]["content"] = [{"type": "text", "text": history[-1]["content"]}]
            if isinstance(blocks, str):
                blocks = [{"type": "text", "text": blocks}]
            history[-1]["content"].extend(blocks)
        else:
            history.append(
                {"role": role, "content": blocks if isinstance(blocks, str) else list(blocks)}
            )
    return "\n\n".join(system), history


def default_max_tokens(model):
    # Values used by the previous adapter for the project's curated model catalog.
    if model.startswith(
        ("claude-fable-5", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-5")
    ):
        return 128000
    if model == "claude-haiku-4-5":
        return 64000
    return 4096


class ChatAnthropic(ChatModel):
    def __init__(self, model, **kwargs):
        super().__init__(model, **kwargs)
        if self.max_tokens is None:
            self.max_tokens = default_max_tokens(model)

    def _sdk(self):
        if self.client is None:
            from anthropic import Anthropic

            options = {
                k: self.options[k]
                for k in ("api_key", "base_url", "timeout", "max_retries", "http_client")
                if k in self.options
            }
            # The previous adapter explicitly passed None, disabling the SDK's
            # non-streaming token-limit heuristic for large model defaults.
            options.setdefault("timeout", None)
            if not options.get("base_url") and os.environ.get("ANTHROPIC_API_URL"):
                options["base_url"] = os.environ["ANTHROPIC_API_URL"]
            self.client = Anthropic(**options)
        return self.client

    def _get_request_payload(self, messages, **kwargs):
        if self.model.startswith("claude-fable-5") and self.temperature not in (None, 1):
            raise ValueError(
                f"`temperature` is not supported for {self.model} at non-default values."
            )
        system, history = anthropic_messages(messages)
        payload = {"model": self.model, "messages": history, "max_tokens": self.max_tokens}
        if system:
            payload["system"] = system
        if self.temperature is not None and not self.model.startswith("claude-fable-5"):
            payload["temperature"] = self.temperature
        effort = self.options.get("effort")
        if effort:
            payload["output_config"] = {"effort": effort}
            if self.model.startswith(
                (
                    "claude-opus-4-7",
                    "claude-opus-4-8",
                    "claude-opus-5",
                    "claude-sonnet-5",
                    "claude-fable-5",
                )
            ):
                payload["thinking"] = {"type": "adaptive", "display": "summarized"}
        tools = kwargs.pop("tools", None)
        if tools:
            payload["tools"] = [
                {
                    "name": t["function"]["name"],
                    "description": t["function"]["description"],
                    "input_schema": t["function"]["parameters"],
                }
                for t in tools
            ]
        choice = kwargs.pop("tool_choice", None)
        if choice:
            payload["tool_choice"] = (
                {"type": choice}
                if choice in ("auto", "any", "none")
                else {"type": "tool", "name": choice}
            )
        format_ = kwargs.pop("response_format", None)
        if format_:
            if format_["type"] != "json_schema":
                raise NotImplementedError("Anthropic requires a schema for JSON output")
            payload.setdefault("output_config", {})["format"] = {
                "type": "json_schema",
                "schema": format_["json_schema"]["schema"],
            }
        return {**payload, **kwargs}

    def _generate(self, messages, **kwargs):
        response = plain_dict(
            self._sdk().messages.create(**self._get_request_payload(messages, **kwargs))
        )
        blocks = response.get("content", [])
        calls = [
            {"id": b["id"], "name": b["name"], "args": b["input"]}
            for b in blocks
            if b["type"] == "tool_use"
        ]
        tokens = response.get("usage") or {}
        return AIMessage(
            blocks,
            id=response.get("id"),
            tool_calls=calls,
            additional_kwargs={"anthropic_content": blocks},
            response_metadata={
                "model_name": response.get("model"),
                "stop_reason": response.get("stop_reason"),
            },
            usage_metadata=usage(
                tokens.get("input_tokens", 0)
                + tokens.get("cache_read_input_tokens", 0)
                + tokens.get("cache_creation_input_tokens", 0),
                tokens.get("output_tokens", 0),
            ),
        )
