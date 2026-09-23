"""Function schemas, hidden state injection, and ordered tool results."""

from __future__ import annotations

import inspect
import json
from concurrent.futures import ThreadPoolExecutor
from contextvars import copy_context
from dataclasses import dataclass
from typing import Annotated, Any, get_args, get_origin, get_type_hints

from pydantic import Field, ValidationError, create_model

from .callbacks import callbacks_for, notify
from .messages import AIMessage, ToolMessage


@dataclass(frozen=True)
class InjectedState:
    field: str


class Tool:
    def __init__(self, func):
        self.func = func
        self.name = func.__name__
        self.description = inspect.getdoc(func) or ""
        self.injected = {}
        fields, public = {}, {}
        hints = get_type_hints(func, include_extras=True)
        for name, parameter in inspect.signature(func).parameters.items():
            annotation = hints.get(name, Any)
            description = None
            if get_origin(annotation) is Annotated:
                annotation, *metadata = get_args(annotation)
                description = next((m for m in metadata if isinstance(m, str)), None)
                for item in metadata:
                    if isinstance(item, InjectedState):
                        self.injected[name] = item.field
            default = ... if parameter.default is inspect.Parameter.empty else parameter.default
            fields[name] = (annotation, Field(default, description=description))
            if name not in self.injected:
                public[name] = fields[name]
        self.args_schema = create_model(self.name, __doc__=self.description, **fields)
        self.tool_call_schema = create_model(self.name, __doc__=self.description, **public)

    @property
    def args(self):
        return self.args_schema.model_json_schema()["properties"]

    def invoke(self, input, config=None):
        handlers = callbacks_for(config)
        notify(handlers, "on_tool_start", {"name": self.name}, str(input))
        try:
            parsed = self.args_schema.model_validate(input)
            # Preserve omitted defaults, including legacy str parameters defaulting to None.
            result = self.func(
                **{k: getattr(parsed, k) for k in input if k in self.args_schema.model_fields}
            )
        except Exception as exc:
            notify(handlers, "on_tool_error", exc)
            raise
        notify(handlers, "on_tool_end", result)
        return result


def tool(func):
    return Tool(func)


def json_schema(schema):
    """Resolve Pydantic references and omit presentation-only field titles."""
    raw = schema.model_json_schema() if hasattr(schema, "model_json_schema") else schema
    definitions = raw.get("$defs", {})

    def resolve(value):
        if isinstance(value, list):
            return [resolve(v) for v in value]
        if isinstance(value, dict):
            if "$ref" in value:
                target = definitions[value["$ref"].split("/")[-1]]
                return resolve({**target, **{k: v for k, v in value.items() if k != "$ref"}})
            return {k: resolve(v) for k, v in value.items() if k not in ("title", "$defs")}
        return value

    return resolve(raw)


def tool_spec(value):
    if isinstance(value, dict):
        return value
    if isinstance(value, Tool):
        name, description, schema = value.name, value.description, value.tool_call_schema
    else:
        schema = value
        raw = schema.model_json_schema()
        name, description = raw["title"], raw.get("description", "")
    parameters = json_schema(schema)
    parameters.pop("description", None)
    return {
        "type": "function",
        "function": {"name": name, "description": description, "parameters": parameters},
    }


class ToolRegistry:
    def __init__(self, tools):
        self.tools_by_name = {}
        for item in tools:
            item = item if isinstance(item, Tool) else Tool(item)
            if item.name in self.tools_by_name:
                raise ValueError(f"Duplicate tool: {item.name}")
            self.tools_by_name[item.name] = item


class ToolExecutor:
    def __init__(self, tools):
        self.registry = ToolRegistry(tools)
        self.tools_by_name = self.registry.tools_by_name

    def _execute(self, call, state, config):
        name, call_id = call["name"], call["id"]
        item = self.tools_by_name.get(name)
        if item is None:
            return ToolMessage(
                f"Error: {name} is not a valid tool, try one of [{', '.join(self.tools_by_name)}].",
                name=name,
                tool_call_id=call_id,
                status="error",
            )
        arguments = {**call["args"], **{k: state[v] for k, v in item.injected.items()}}
        try:
            result = item.invoke(arguments, config)
        except ValidationError as exc:
            errors = [e for e in exc.errors() if e["loc"][0] not in item.injected]
            details = "\n".join(f"{'.'.join(map(str, e['loc']))}: {e['msg']}" for e in errors)
            return ToolMessage(
                f"Error invoking tool '{name}' with kwargs {call['args']} with error:\n {details}\n Please fix the error and try again.",
                name=name,
                tool_call_id=call_id,
                status="error",
            )
        if not isinstance(result, (str, list)):
            result = json.dumps(result, ensure_ascii=False)
        return ToolMessage(result, name=name, tool_call_id=call_id)

    def invoke(self, state, config=None):
        message = next((m for m in reversed(state["messages"]) if isinstance(m, AIMessage)), None)
        if message is None:
            raise ValueError("Tool execution requires an AIMessage")
        calls = message.tool_calls
        if len(calls) < 2:
            results = [self._execute(call, state, config) for call in calls]
        else:
            # Match the existing batch behavior: parallel execution, input-order results.
            with ThreadPoolExecutor(max_workers=(config or {}).get("max_concurrency")) as pool:
                futures = [
                    pool.submit(copy_context().run, self._execute, call, state, config)
                    for call in calls
                ]
                results = [future.result() for future in futures]
        return {"messages": results}


ToolNode = ToolExecutor
