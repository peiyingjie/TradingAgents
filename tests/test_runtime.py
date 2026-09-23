from __future__ import annotations

import sqlite3
import subprocess
import sys
from threading import Barrier
from typing import Annotated

import pytest

from cli.stats_handler import StatsCallbackHandler
from tradingagents.runtime.callbacks import run_config
from tradingagents.runtime.checkpoint import SqliteSaver
from tradingagents.runtime.graph import END, START, GraphRecursionError, MessagesState, StateGraph
from tradingagents.runtime.messages import (
    AIMessage,
    HumanMessage,
    RemoveMessage,
    ToolMessage,
    add_messages,
)
from tradingagents.runtime.tools import InjectedState, ToolNode, tool


def test_message_reducer_replaces_removes_and_preserves_order():
    first, second = HumanMessage("a", id="a"), AIMessage("b", id="b")
    changed = AIMessage("replacement", id="b")
    last = ToolMessage("result", tool_call_id="call")
    result = add_messages([first, second], [RemoveMessage(id="a"), changed, last])
    assert result == [changed, last]
    assert last.id
    assert add_messages(result, [HumanMessage("final", id="b")])[0].content == "final"
    with pytest.raises(ValueError, match="unknown"):
        add_messages(result, RemoveMessage(id="missing"))


class State(MessagesState):
    trade_date: str
    debate: dict


def test_state_updates_replace_nested_fields_and_streams_are_independent():
    builder = StateGraph(State)
    builder.add_node("first", lambda s: {"debate": {"count": 1}})
    builder.add_node("second", lambda s: {"messages": [AIMessage("done")]})
    builder.add_edge(START, "first").add_edge("first", "second").add_edge("second", END)
    graph = builder.compile()
    input = {"messages": [("human", "begin")], "debate": {"count": 0, "history": "old"}}
    snapshots = list(graph.stream(input, stream_mode="values"))
    assert len(snapshots) == 3
    assert snapshots[1]["debate"] == {"count": 1}
    snapshots[-1]["messages"][0].content = "mutated"
    assert snapshots[0]["messages"][0].content == "begin"
    assert graph.invoke(input)["messages"][-1].content == "done"


def test_tool_batches_return_in_call_order_and_receive_authoritative_state():
    barrier = Barrier(2)

    @tool
    def example(number: int, date: Annotated[str, InjectedState("trade_date")] = "") -> str:
        """Return the injected date after both calls have started."""
        barrier.wait(timeout=5)
        return f"{number}:{date}"

    node = ToolNode([example])
    stats = StatsCallbackHandler()
    ai = AIMessage(
        "",
        tool_calls=[
            {"name": "example", "args": {"number": n, "date": "forged"}, "id": str(n)}
            for n in (2, 1)
        ],
    )
    result = node.invoke({"messages": [ai], "trade_date": "2026-08-14"}, {"callbacks": [stats]})
    assert [m.content for m in result["messages"]] == ["2:2026-08-14", "1:2026-08-14"]
    assert [m.tool_call_id for m in result["messages"]] == ["2", "1"]
    assert stats.get_stats()["tool_calls"] == 2


def test_invalid_calls_return_errors_but_vendor_failures_propagate():
    @tool
    def example(number: int) -> str:
        """Always raises at the data source."""
        raise RuntimeError("vendor unavailable")

    node = ToolNode([example])

    def call(name, args):
        return node.invoke(
            {"messages": [AIMessage("", tool_calls=[{"name": name, "args": args, "id": "1"}])]}
        )

    assert call("unknown", {})["messages"][0].status == "error"
    result = call("example", {"number": "bad"})["messages"][0]
    assert result.status == "error" and "number:" in result.content
    with pytest.raises(RuntimeError, match="vendor unavailable"):
        call("example", {"number": 1})


def test_checkpoint_restores_typed_messages_and_retries_only_failed_node(tmp_path):
    calls, fail = [], [True]

    def first(state):
        calls.append("first")
        return {
            "messages": [
                AIMessage(
                    "",
                    tool_calls=[{"name": "tool", "args": {}, "id": "c"}],
                    additional_kwargs={"signature": b"signed"},
                )
            ]
        }

    def second(state):
        calls.append("second")
        assert state["messages"][-1].additional_kwargs["signature"] == b"signed"
        if fail[0]:
            raise RuntimeError("crash")
        return {"messages": [ToolMessage("ok", tool_call_id="c")]}

    builder = StateGraph(State)
    builder.add_node("first", first).add_node("second", second)
    builder.add_edge(START, "first").add_edge("first", "second").add_edge("second", END)
    cfg = {"configurable": {"thread_id": "thread"}}
    db = tmp_path / "checkpoint.db"
    with sqlite3.connect(db) as conn:
        saver = SqliteSaver(conn)
        saver.setup()
        with pytest.raises(RuntimeError, match="crash"):
            builder.compile(saver).invoke({"messages": [("human", "begin")]}, cfg)
    fail[0] = False
    with sqlite3.connect(db) as conn:
        saver = SqliteSaver(conn)
        saver.setup()
        graph = builder.compile(saver)
        final = graph.invoke(None, cfg)
        assert calls == ["first", "second", "second"]
        assert [m.type for m in final["messages"]] == ["human", "ai", "tool"]
        assert graph.invoke(None, cfg) == final
        assert calls == ["first", "second", "second"]
    assert run_config.get() is None


def test_loops_obey_limit_and_do_not_leak_run_context():
    builder = StateGraph(State).add_node("loop", lambda s: {"messages": [AIMessage("again")]})
    builder.add_edge(START, "loop").add_edge("loop", "loop")
    with pytest.raises(GraphRecursionError):
        builder.compile().invoke({"messages": []}, {"recursion_limit": 3})
    assert run_config.get() is None


def test_unknown_router_target_fails_before_checkpoint_commit():
    builder = StateGraph(State).add_node("node", lambda s: {"debate": {"count": 1}})
    builder.add_edge(START, "node").add_conditional_edges("node", lambda s: "missing", [END])
    with pytest.raises(ValueError, match="unmapped target"):
        builder.compile().invoke({})


def test_legacy_checkpoint_is_preserved_and_lifecycle_is_closed(tmp_path):
    from tests.test_checkpoint_lifecycle import _bare_graph
    from tradingagents.graph.checkpointer import thread_id

    graph = _bare_graph(str(tmp_path))
    folder = tmp_path / "checkpoints"
    folder.mkdir()
    tid = thread_id("AAPL", "2026-08-14", graph._run_signature("stock"))
    db = folder / "AAPL.db"
    with sqlite3.connect(db) as conn:
        conn.execute("CREATE TABLE checkpoints (thread_id TEXT, checkpoint BLOB)")
        conn.execute("INSERT INTO checkpoints VALUES (?, ?)", (tid, b"legacy data"))
    with pytest.raises(ValueError, match="legacy framework checkpoint"):
        graph.begin_checkpoint("AAPL", "2026-08-14")
    assert graph._checkpointer_ctx is None
    assert graph._resuming is False
    with sqlite3.connect(db) as conn:
        assert conn.execute("SELECT checkpoint FROM checkpoints").fetchone()[0] == b"legacy data"


def test_entrypoints_import_with_framework_imports_blocked():
    code = """
import importlib.abc
import sys
class BlockFrameworks(importlib.abc.MetaPathFinder):
    def find_spec(self, fullname, path=None, target=None):
        if fullname.startswith(("langchain", "langgraph")):
            raise AssertionError("Forbidden framework import: " + fullname)
sys.meta_path.insert(0, BlockFrameworks())
import tradingagents.graph.trading_graph
import tradingagents.backtest
import cli.main
from tradingagents.llm_clients import create_llm_client
create_llm_client("ollama", "local-model").get_llm()
print("framework-free")
"""
    result = subprocess.run(
        [sys.executable, "-c", code], capture_output=True, text=True, check=True
    )
    assert result.stdout.strip() == "framework-free"
