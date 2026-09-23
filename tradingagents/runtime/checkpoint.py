"""Atomic state + next-node snapshots, in versioned JSON stored in SQLite."""

from __future__ import annotations

import base64
import json
import threading
from dataclasses import dataclass

from .messages import MESSAGE_TYPES, BaseMessage


def _encode(value):
    if isinstance(value, BaseMessage):
        return {"__ta_message__": value.type, "data": _encode(value.model_dump())}
    if isinstance(value, bytes):
        return {"__ta_bytes__": base64.b64encode(value).decode("ascii")}
    if isinstance(value, dict):
        return {k: _encode(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_encode(v) for v in value]
    return value


def _decode(value):
    if isinstance(value, list):
        return [_decode(v) for v in value]
    if isinstance(value, dict):
        if set(value) == {"__ta_message__", "data"}:
            return MESSAGE_TYPES[value["__ta_message__"]](**_decode(value["data"]))
        if set(value) == {"__ta_bytes__"}:
            return base64.b64decode(value["__ta_bytes__"])
        return {k: _decode(v) for k, v in value.items()}
    return value


@dataclass
class Checkpoint:
    state: dict
    next_node: str
    step: int


class SqliteSaver:
    def __init__(self, conn):
        self.conn = conn
        self.lock = threading.Lock()

    def setup(self):
        with self.lock, self.conn:
            self.conn.execute("""CREATE TABLE IF NOT EXISTS runtime_checkpoints (
                thread_id TEXT PRIMARY KEY, version INTEGER NOT NULL,
                state TEXT NOT NULL, next_node TEXT NOT NULL, step INTEGER NOT NULL
            )""")

    def load(self, thread_id):
        with self.lock:
            row = self.conn.execute(
                "SELECT version, state, next_node, step FROM runtime_checkpoints WHERE thread_id = ?",
                (thread_id,),
            ).fetchone()
            if row is None:
                # Never silently discard an interrupted run from the old runtime.
                legacy = self.conn.execute(
                    "SELECT 1 FROM sqlite_master WHERE type='table' AND name='checkpoints'"
                ).fetchone()
                if (
                    legacy
                    and self.conn.execute(
                        "SELECT 1 FROM checkpoints WHERE thread_id = ? LIMIT 1", (thread_id,)
                    ).fetchone()
                ):
                    raise ValueError(
                        "This run has a legacy framework checkpoint. Resume it with the "
                        "previous version, or use --clear-checkpoints to start fresh."
                    )
                return None
        version, state, next_node, step = row
        if version != 1:
            raise ValueError(f"Unsupported checkpoint version: {version}")
        return Checkpoint(_decode(json.loads(state)), next_node, step)

    def save(self, thread_id, state, next_node, step):
        payload = json.dumps(_encode(state), ensure_ascii=False)
        with self.lock, self.conn:
            self.conn.execute(
                "INSERT OR REPLACE INTO runtime_checkpoints VALUES (?, 1, ?, ?, ?)",
                (thread_id, payload, next_node, step),
            )

    def delete(self, thread_id):
        with self.lock, self.conn:
            self.conn.execute("DELETE FROM runtime_checkpoints WHERE thread_id = ?", (thread_id,))
