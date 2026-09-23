"""Messages shared by agents, providers, tools, and persisted graph state."""

from __future__ import annotations

from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, Field, field_validator


class BaseMessage(BaseModel):
    content: str | list = ""
    type: str = "base"
    id: str | None = None
    name: str | None = None
    additional_kwargs: dict[str, Any] = Field(default_factory=dict)
    response_metadata: dict[str, Any] = Field(default_factory=dict)

    def __init__(self, content="", **kwargs):
        super().__init__(content=content, **kwargs)

    def pretty_print(self):
        print(f"{self.type.title()} Message\n\n{self.content}")


class HumanMessage(BaseMessage):
    type: Literal["human"] = "human"


class SystemMessage(BaseMessage):
    type: Literal["system"] = "system"


class AIMessage(BaseMessage):
    type: Literal["ai"] = "ai"
    tool_calls: list[dict[str, Any]] = Field(default_factory=list)
    invalid_tool_calls: list[dict[str, Any]] = Field(default_factory=list)
    usage_metadata: dict[str, Any] | None = None

    @field_validator("tool_calls")
    @classmethod
    def normalize_calls(cls, calls):
        return [{**call, "type": "tool_call"} for call in calls]


class ToolMessage(BaseMessage):
    type: Literal["tool"] = "tool"
    tool_call_id: str
    status: Literal["success", "error"] = "success"


class RemoveMessage(BaseMessage):
    type: Literal["remove"] = "remove"
    id: str


MESSAGE_TYPES = {
    c.model_fields["type"].default: c
    for c in (HumanMessage, SystemMessage, AIMessage, ToolMessage, RemoveMessage)
}


def as_message(value) -> BaseMessage:
    if isinstance(value, BaseMessage):
        return value
    if isinstance(value, str):
        return HumanMessage(value)
    if isinstance(value, (tuple, list)) and len(value) == 2:
        value = {"role": value[0], "content": value[1]}
    if not isinstance(value, dict):
        raise TypeError(f"Unsupported message: {type(value).__name__}")
    data = dict(value)
    role = data.pop("role", None) or data.pop("type", None)
    role = {"user": "human", "assistant": "ai"}.get(role, role)
    if role not in MESSAGE_TYPES:
        raise ValueError(f"Unsupported message role: {role!r}")
    return MESSAGE_TYPES[role](**data)


def to_messages(value) -> list[BaseMessage]:
    if hasattr(value, "to_messages"):
        value = value.to_messages()
    if isinstance(value, (str, BaseMessage, dict)):
        value = [value]
    return [as_message(item) for item in value]


def add_messages(left, right) -> list[BaseMessage]:
    """Append new IDs, replace existing IDs, and apply explicit removals."""
    messages = to_messages(left or [])
    incoming = to_messages(right or [])
    for message in messages + incoming:
        if message.id is None:
            message.id = str(uuid4())
    positions = {m.id: i for i, m in enumerate(messages)}
    removed = set()
    for message in incoming:
        if isinstance(message, RemoveMessage):
            if message.id not in positions:
                raise ValueError(f"Cannot remove unknown message ID: {message.id}")
            removed.add(message.id)
        elif message.id in positions:
            messages[positions[message.id]] = message
            removed.discard(message.id)
        else:
            positions[message.id] = len(messages)
            messages.append(message)
    return [m for m in messages if m.id not in removed]
