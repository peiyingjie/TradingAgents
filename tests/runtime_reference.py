"""Deterministic business replay. The fixture was captured before the runtime swap."""

import hashlib
import json
from contextlib import ExitStack
from unittest.mock import patch

from tradingagents.graph.conditional_logic import ConditionalLogic
from tradingagents.graph.propagation import Propagator
from tradingagents.graph.setup import GraphSetup
from tradingagents.graph.trading_graph import TradingAgentsGraph
from tradingagents.runtime.messages import AIMessage, BaseMessage


def clean(value):
    if isinstance(value, BaseMessage):
        result = {"type": value.type, "content": value.content}
        for key in ("tool_calls", "tool_call_id", "name", "status"):
            item = getattr(value, key, None)
            if item is not None:
                result[key] = clean(item)
        return result
    if isinstance(value, dict):
        return {k: clean(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [clean(v) for v in value]
    return value


def digest(value):
    return hashlib.sha256(
        json.dumps(clean(value), sort_keys=True, ensure_ascii=False).encode()
    ).hexdigest()


class RecordedLLM:
    def __init__(self):
        self.calls = []
        self.bound = []
        self.tools = []
        self.turns = {}

    def with_structured_output(self, schema):
        raise NotImplementedError("fixed free-text reference")

    def bind_tools(self, tools):
        self.tools = tools
        self.bound.append([t.name for t in tools])
        return self

    def __call__(self, input):
        return self.invoke(input)

    def invoke(self, input, config=None):
        if hasattr(input, "to_messages"):
            input = input.to_messages()
        self.calls.append(digest(input))
        if isinstance(input, list) and self.tools:
            text = str(getattr(input[0], "content", ""))
            if "provided tools to progress" in text:
                names = tuple(t.name for t in self.tools)
                turn = self.turns.get(names, 0)
                self.turns[names] = turn + 1
                # Exercise every tool bound to every analyst, in separate rounds.
                if turn < len(self.tools):
                    t = self.tools[turn]
                    values = {
                        "symbol": "AAPL",
                        "ticker": "AAPL",
                        "indicator": "rsi",
                        "start_date": "2026-08-01",
                        "end_date": "2026-09-15",
                        "curr_date": "2026-09-15",
                        "topic": "Fed rate cut",
                    }
                    schema = t.tool_call_schema.model_json_schema()
                    args = {k: values[k] for k in schema.get("required", [])}
                    return AIMessage(
                        "",
                        tool_calls=[
                            {"name": t.name, "args": args, "id": f"call-{len(self.calls)}"}
                        ],
                    )
        return AIMessage(f"**Rating**: Buy\nEvidence {len(self.calls)}")


class RecordedLogic(ConditionalLogic):
    def __init__(self, *args):
        super().__init__(*args)
        self.routes = []

    def __getattribute__(self, name):
        value = super().__getattribute__(name)
        if name.startswith("should_continue_"):

            def route(state):
                result = value(state)
                self.routes.append([name, result])
                return result

            return route
        return value


def replay(analysts=("market", "social", "news", "fundamentals"), rounds=1, stream_mode="values"):
    llm = RecordedLLM()
    logic = RecordedLogic(rounds, rounds)
    vendors = []

    def vendor(*args, **kwargs):
        vendors.append([list(args), kwargs])
        return "Fixed source evidence"

    with ExitStack() as stack:
        for module in (
            "core_stock",
            "technical_indicators",
            "news_data",
            "macro_data",
            "prediction_markets",
            "fundamental_data",
        ):
            stack.enter_context(
                patch(f"tradingagents.agents.utils.{module}_tools.route_to_vendor", vendor)
            )
        stack.enter_context(
            patch(
                "tradingagents.agents.utils.market_data_validation_tools.build_verified_market_snapshot",
                vendor,
            )
        )
        for name in ("fetch_stocktwits_messages", "fetch_reddit_posts"):
            stack.enter_context(
                patch(
                    f"tradingagents.agents.analysts.sentiment_analyst.{name}",
                    return_value="Fixed social evidence",
                )
            )
        nodes = TradingAgentsGraph._create_tool_nodes(None)
        graph = GraphSetup(llm, llm, nodes, logic).setup_graph(analysts).compile()
        state = Propagator().create_initial_state(
            "AAPL", "2026-08-14", instrument_context="AAPL / Apple / USD"
        )
        chunks = list(graph.stream(state, config={"recursion_limit": 100}, stream_mode=stream_mode))
    return {
        "chunks": clean(chunks),
        "prompts": llm.calls,
        "bindings": llm.bound,
        "routes": logic.routes,
        "vendors": vendors,
    }


SCENARIOS = [
    (["market", "social", "news", "fundamentals"], 1),
    (["fundamentals", "news", "market"], 2),
    (["social"], 1),
    (["market"], 0),
]


def reference():
    results = []
    for analysts, rounds in SCENARIOS:
        result = replay(analysts, rounds)
        chunks = result.pop("chunks")
        result["states"] = [digest(chunk) for chunk in chunks]
        result["final"] = chunks[-1]
        result["nodes"] = [next(iter(c)) for c in replay(analysts, rounds, "updates")["chunks"]]
        results.append(result)
    return results
