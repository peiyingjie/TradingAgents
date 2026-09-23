"""Shared invoke/bind/parse behavior; providers own their wire formats."""

from __future__ import annotations

import json
from dataclasses import dataclass

from tradingagents.runtime.callbacks import (
    ChatGeneration,
    LLMResult,
    callbacks_for,
    notify,
)
from tradingagents.runtime.messages import to_messages
from tradingagents.runtime.tools import json_schema, tool_spec


class ChatModel:
    structured_method = "function_calling"

    def __init__(self, model, **kwargs):
        self.model = self.model_name = model
        self.options = dict(kwargs)
        self.callbacks = kwargs.get("callbacks") or []
        self.temperature = kwargs.get("temperature")
        self.max_tokens = kwargs.get("max_tokens")
        self.max_retries = kwargs.get("max_retries", 2)
        self.client = None
        for key, value in kwargs.items():
            setattr(self, key, value)

    def invoke(self, input, config=None, **kwargs):
        messages = to_messages(input)
        handlers = callbacks_for(config, self.callbacks)
        notify(handlers, "on_chat_model_start", {"name": type(self).__name__}, [messages])
        try:
            response = self._generate(messages, **kwargs)
        except Exception as exc:
            notify(handlers, "on_llm_error", exc)
            raise
        notify(handlers, "on_llm_end", LLMResult([[ChatGeneration(response)]]))
        return response

    def bind_tools(self, tools, **kwargs):
        return BoundModel(self, {"tools": [tool_spec(t) for t in tools], **kwargs})

    def with_structured_output(self, schema, *, method=None, **kwargs):
        method = method or self.structured_method
        spec = tool_spec(schema)
        function = spec["function"]
        if method == "function_calling":
            options = {"tools": [spec], "tool_choice": function["name"], **kwargs}
        elif method in ("json_schema", "json_mode"):
            options = {"response_format": {"type": "json_object"}, **kwargs}
            if method == "json_schema":
                options["response_format"] = {
                    "type": "json_schema",
                    "json_schema": {"name": function["name"], "schema": json_schema(schema)},
                }
        else:
            raise NotImplementedError(f"Unsupported structured output method: {method}")
        return BoundModel(self, options, schema, method)


@dataclass
class BoundModel:
    model: ChatModel
    kwargs: dict
    schema: object = None
    method: str | None = None

    def invoke(self, input, config=None, **kwargs):
        response = self.model.invoke(input, config, **{**self.kwargs, **kwargs})
        if self.schema is None:
            return response
        if self.method == "function_calling":
            if not response.tool_calls:
                return None
            call = response.tool_calls[0]
            if call["name"] != self.schema.__name__:
                raise ValueError(f"Expected {self.schema.__name__}, got tool {call['name']}")
            data = call["args"]
        else:
            text = response.content.strip()
            if text.startswith("```"):
                text = text.split("\n", 1)[1].rsplit("```", 1)[0]
            data = json.loads(text)
        return self.schema.model_validate(data)


def plain_dict(value):
    return value if isinstance(value, dict) else value.model_dump(exclude_none=True)


def usage(input_tokens=0, output_tokens=0, **extra):
    return {
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "total_tokens": input_tokens + output_tokens,
        **extra,
    }


def parsed_calls(calls):
    valid, invalid = [], []
    for call in calls:
        function = call["function"]
        try:
            arguments = json.loads(function["arguments"])
            if not isinstance(arguments, dict):
                raise ValueError("Tool arguments must be a JSON object")
            valid.append({"id": call["id"], "name": function["name"], "args": arguments})
        except (ValueError, TypeError) as exc:
            invalid.append(
                {
                    "id": call["id"],
                    "name": function["name"],
                    "args": function["arguments"],
                    "error": str(exc),
                }
            )
    return valid, invalid
