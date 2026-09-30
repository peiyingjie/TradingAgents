"""Capture behavior from retained Python code for offline Go tests."""
# ruff: noqa: E402 -- bootstrap the repository import path before reference imports.

import copy
import json
import sys
import tempfile
from contextlib import ExitStack
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from datetime import datetime
from types import SimpleNamespace

import numpy as np
import pandas as pd
from stockstats import wrap

from tests.test_sec_edgar import FACTS, TICKER_MAP
from tradingagents.agents.utils.memory import TradingMemoryLog
from tradingagents.backtest import summarize
from tradingagents.dataflows import market_data_validator, sec_edgar, stockstats_utils, y_finance
from tradingagents.dataflows.config import set_config
from tradingagents.graph.conditional_logic import ConditionalLogic
from tradingagents.graph.propagation import Propagator
from tradingagents.graph.setup import GraphSetup
from tradingagents.graph.trading_graph import TradingAgentsGraph
from tradingagents.portfolio import PortfolioContext
from tradingagents.reporting import write_report_tree
from tradingagents.runtime.checkpoint import _encode
from tradingagents.runtime.messages import AIMessage, BaseMessage, to_messages


def save(path, value):
    p = ROOT / path
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(
        json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False),
        encoding="utf-8",
        newline="\n",
    )


bars = []
for i, date in enumerate(pd.bdate_range("2025-01-01", periods=230)):
    close = 100 + i * 0.13 + np.sin(i / 3) * 4
    bars.append(
        {
            "Date": str(date.date()),
            "Open": close - 0.5,
            "High": close + 2,
            "Low": close - 1.5,
            "Close": close,
            "Volume": float(1000 + (i % 13) * 150),
        }
    )
frame = wrap(pd.DataFrame(bars))
names = [
    "close_10_ema",
    "close_50_sma",
    "close_200_sma",
    "macd",
    "macds",
    "macdh",
    "rsi",
    "atr",
    "boll",
    "boll_ub",
    "boll_lb",
    "vwma",
    "mfi",
]
save(
    "internal/dataflows/testdata/python_indicators.json",
    {
        "bars": bars,
        "indicators": {n: [None if pd.isna(v) else float(v) for v in frame[n]] for n in names},
    },
)
with patch.object(
    sec_edgar,
    "_cached_json",
    side_effect=lambda url, name: TICKER_MAP if "company_tickers" in url else FACTS,
):
    cases = []
    for method, date, freq in [
        ("get_balance_sheet", "2009-06-30", "annual"),
        ("get_balance_sheet", "2011-01-01", "annual"),
        ("get_balance_sheet", "2024-10-15", "annual"),
        ("get_balance_sheet", "2024-11-15", "annual"),
        ("get_income_statement", "2026-06-01", "quarterly"),
        ("get_income_statement", "2024-11-15", "annual"),
    ]:
        cases.append(
            {
                "method": method,
                "date": date,
                "frequency": freq,
                "output": getattr(sec_edgar, method)("AAPL", freq, date),
            }
        )
save(
    "internal/dataflows/testdata/python_sec.json",
    {"tickers": TICKER_MAP, "facts": FACTS, "cases": cases},
)
books = [
    {},
    {"cash": 1000, "positions": []},
    {
        "cash": 25000,
        "currency": "USD",
        "positions": [
            {"ticker": "AAPL", "quantity": 120, "average_price": 150},
            {"ticker": "MSFT", "quantity": 10},
        ],
    },
    {"positions": [{"ticker": "aapl", "quantity": -1.2345, "average_price": 1.2}]},
]
books += [{"cash": value} for value in [1e-8, 1e-6, 1e-5, 1e-4, 1e6, 1e15, 1e16, 1e20, -0.0]]
books += [
    {"cash": "1200.5", "positions": [{"ticker": "AAPL", "quantity": "2.5", "average_price": "100"}]}
]
save(
    "internal/portfolio/testdata/python_portfolios.json",
    [
        {
            "input": b,
            "render": PortfolioContext.model_validate(b).render("AAPL"),
            "fingerprint": PortfolioContext.model_validate(b).fingerprint(),
        }
        for b in books
    ],
)

gap_csv = """Datetime,Open,High,Low,Close,Volume
2026-08-10 00:00:00+09:00,,102,,101,
2026-08-11 00:00:00+09:00,102,,99,103,2000
2026-08-12 00:00:00+09:00,999,999,999,NaN,9999
2026-08-13 00:00:00+09:00,NaN,105,,104,
2026-08-14 00:00:00+09:00,1000,1001,999,1000,99999
"""
from io import StringIO

gap_frame = stockstats_utils._clean_dataframe(pd.read_csv(StringIO(gap_csv)))
gap_frame = gap_frame[gap_frame["Date"] <= pd.Timestamp("2026-08-13")]
gap_results = {}
for fill in (False, True):
    result = (
        stockstats_utils._fill_price_gaps(gap_frame)
        if fill
        else gap_frame.dropna(subset=["Close"]).copy()
    )
    result["Date"] = result["Date"].dt.strftime("%Y-%m-%d")
    gap_results[str(fill).lower()] = json.loads(result.to_json(orient="records"))
save(
    "internal/dataflows/testdata/python_gaps.json",
    {"csv": gap_csv, "date": "2026-08-13", "results": gap_results},
)
with tempfile.TemporaryDirectory() as td:
    path = Path(td) / "memory.md"
    log = TradingMemoryLog({"memory_log_path": str(path)})
    for ticker, date, decision in [
        ("AAPL", "2025-01-01", "**Rating**: Buy\nThesis A"),
        ("MSFT", "2025-01-02", "**Rating**: Sell\nThesis B"),
        ("AAPL", "2025-01-03", "unclear"),
        ("AAPL", "2025-01-04", "**Rating**: Hold"),
    ]:
        log.store_decision(ticker, date, decision)
    log.batch_update_with_outcomes(
        [
            {
                "ticker": "AAPL",
                "trade_date": "2025-01-01",
                "raw_return": 0.052,
                "alpha_return": 0.023,
                "holding_days": 5,
                "reflection": "lesson A",
                "resolution_date": "2025-01-08",
            },
            {
                "ticker": "MSFT",
                "trade_date": "2025-01-02",
                "raw_return": -0.021,
                "alpha_return": -0.031,
                "holding_days": 5,
                "reflection": "lesson B",
                "resolution_date": "2025-01-09",
            },
        ]
    )
    save(
        "internal/memory/testdata/python_memory.json",
        {
            "log": path.read_text(),
            "live": log.get_past_context("AAPL"),
            "historical": log.get_past_context("AAPL", as_of="2025-01-08"),
            "summary": summarize(log).render(),
        },
    )


def clean(v):
    if isinstance(v, BaseMessage):
        return {
            "type": v.type,
            "content": v.content,
            **(
                {
                    k: getattr(v, k)
                    for k in ["tool_calls", "tool_call_id", "name", "status"]
                    if getattr(v, k, None) is not None
                }
            ),
        }
    if isinstance(v, dict):
        return {k: clean(x) for k, x in v.items()}
    if isinstance(v, (list, tuple)):
        return [clean(x) for x in v]
    return v


samples = {
    "ResearchPlan": {
        "recommendation": "Buy",
        "rationale": "research evidence",
        "strategic_actions": "research actions",
    },
    "TraderProposal": {
        "action": "Buy",
        "reasoning": "trader evidence",
        "entry_price": 100,
        "stop_loss": 90,
        "position_sizing": "5%",
    },
    "PortfolioDecision": {
        "rating": "Overweight",
        "executive_summary": "summary",
        "investment_thesis": "thesis",
        "price_target": 120,
        "time_horizon": "3 months",
    },
    "SentimentReport": {
        "overall_band": "Bullish",
        "overall_score": 7,
        "confidence": "medium",
        "narrative": "sentiment evidence",
    },
}


class Bound:
    def __init__(self, parent, tools=(), schema=None):
        self.parent = parent
        self.tools = tools
        self.schema = schema

    def invoke(self, input, **kwargs):
        return self.parent.call(input, self.tools, self.schema)


class LLM:
    def __init__(self):
        self.calls = []
        self.turns = {}

    def with_structured_output(self, schema):
        return Bound(self, schema=schema)

    def bind_tools(self, tools):
        return Bound(self, tools=tools)

    def invoke(self, input, **kwargs):
        return self.call(input, (), None)

    def call(self, input, tools, schema):
        self.calls.append(
            {
                "messages": [{"type": m.type, "content": m.content} for m in to_messages(input)],
                "tools": [t.name for t in tools],
                "schema": schema.__name__ if schema else "",
            }
        )
        if schema:
            return schema.model_validate(samples[schema.__name__])
        if tools:
            names = tuple(t.name for t in tools)
            turn = self.turns.get(names, 0)
            self.turns[names] = turn + 1
            if turn < len(tools):
                tool = tools[turn]
                values = {
                    "symbol": "AAPL",
                    "ticker": "AAPL",
                    "indicator": "rsi",
                    "start_date": "2026-08-01",
                    "end_date": "2026-09-15",
                    "curr_date": "2026-09-15",
                    "topic": "Fed rate cut",
                }
                args = {
                    k: values[k]
                    for k in tool.tool_call_schema.model_json_schema().get("required", [])
                }
                return AIMessage(
                    "",
                    tool_calls=[{"name": tool.name, "args": args, "id": f"call-{len(self.calls)}"}],
                )
        return AIMessage(f"**Rating**: Buy\nEvidence {len(self.calls)}")


scenarios = []
set_config({"output_language": "English"})
for selected, rounds in [
    (["market", "social", "news", "fundamentals"], 1),
    (["fundamentals", "news", "market"], 2),
    (["social"], 1),
    (["market"], 0),
]:
    llm = LLM()
    vendors = []

    def vendor(*args, _vendors=vendors, **kwargs):
        _vendors.append([list(args), kwargs])
        return "Fixed source evidence"

    with ExitStack() as stack:
        for module in [
            "core_stock",
            "technical_indicators",
            "news_data",
            "macro_data",
            "prediction_markets",
            "fundamental_data",
        ]:
            stack.enter_context(
                patch(f"tradingagents.agents.utils.{module}_tools.route_to_vendor", vendor)
            )
        stack.enter_context(
            patch(
                "tradingagents.agents.utils.market_data_validation_tools.build_verified_market_snapshot",
                vendor,
            )
        )
        for name in ["fetch_stocktwits_messages", "fetch_reddit_posts"]:
            stack.enter_context(
                patch(
                    f"tradingagents.agents.analysts.sentiment_analyst.{name}",
                    return_value="Fixed social evidence",
                )
            )
        workflow = (
            GraphSetup(
                llm,
                llm,
                TradingAgentsGraph._create_tool_nodes(None),
                ConditionalLogic(rounds, rounds),
            )
            .setup_graph(selected)
            .compile()
        )
        initial = Propagator().create_initial_state(
            "AAPL", "2026-08-14", instrument_context="AAPL / Apple / USD"
        )
        initial["messages"] = to_messages(initial["messages"])
        initial["messages"][0].id = "initial"
        chunks = list(
            workflow.stream(initial, config={"recursion_limit": 100}, stream_mode="updates")
        )
        final = copy.deepcopy(initial)
        # Use the real state reducer, including removals, for the final state.
        for chunk in chunks:
            final = workflow._merge(final, next(iter(chunk.values())))
    scenarios.append(
        {
            "selected": selected,
            "rounds": rounds,
            "nodes": [next(iter(c)) for c in chunks],
            "calls": llm.calls,
            "vendors": vendors,
            "final": clean(final),
        }
    )
save("internal/graph/testdata/python_graph.json", {"samples": samples, "scenarios": scenarios})
with patch.object(market_data_validator, "load_ohlcv", return_value=pd.DataFrame(bars)):
    save(
        "internal/dataflows/testdata/python_snapshot.json",
        {
            "bars": bars,
            "date": bars[-1]["Date"],
            "output": market_data_validator.build_verified_market_snapshot(
                "AAPL", bars[-1]["Date"], 3
            ),
        },
    )
with tempfile.TemporaryDirectory() as td:

    class FixedDateTime:
        @staticmethod
        def now():
            return datetime(2026, 8, 14, 12, 34, 56)

    with patch("tradingagents.reporting.datetime", FixedDateTime):
        write_report_tree(scenarios[0]["final"], "AAPL", td)
    save(
        "internal/reporting/testdata/python_reports.json",
        {
            "state": scenarios[0]["final"],
            "files": {
                p.relative_to(td).as_posix(): p.read_text(encoding="utf-8")
                for p in Path(td).rglob("*.md")
            },
        },
    )
print(
    "Captured Python indicators, SEC vintages, portfolios, memory, summary, and four complete graph scenarios."
)

csv_bars = [
    {
        "Date": "2025-01-01",
        "Open": 100.0,
        "High": 102.345,
        "Low": 99.5,
        "Close": 101.0,
        "Volume": 1000,
        "Dividends": 0.247,
        "StockSplits": 0.0,
    },
    {
        "Date": "2025-01-02",
        "Open": 101.555,
        "High": 105.25,
        "Low": 100.0,
        "Close": 102.1,
        "Volume": 1200,
        "Dividends": 0.0,
        "StockSplits": 2.0,
    },
]
history = pd.DataFrame(csv_bars).rename(columns={"StockSplits": "Stock Splits"}).set_index("Date")
history.index = pd.DatetimeIndex(history.index, name="Date").tz_localize("America/New_York")


class QuoteDateTime(datetime):
    @classmethod
    def now(cls, tz=None):
        return cls(2026, 8, 14, 12, 34, 56, tzinfo=tz)


with (
    patch.object(
        y_finance.yf,
        "Ticker",
        return_value=SimpleNamespace(history=lambda **kwargs: history.copy()),
    ),
    patch.object(y_finance, "datetime", QuoteDateTime),
):
    csv_output = y_finance.get_YFin_data_online("AAPL", "2025-01-01", "2025-01-02")
save("internal/dataflows/testdata/python_stock_csv.json", {"bars": csv_bars, "output": csv_output})
checkpoint_state = Propagator().create_initial_state("AAPL", "2026-08-14")
checkpoint_state["messages"] = [
    AIMessage(
        "",
        id="persisted",
        tool_calls=[{"id": "call-1", "name": "get_news", "args": {"ticker": "AAPL"}}],
        additional_kwargs={
            "google_parts": [
                {
                    "thought_signature": b"signed",
                    "function_call": {"name": "get_news", "args": {"ticker": "AAPL"}},
                }
            ]
        },
    )
]
save(
    "internal/runtime/testdata/python_checkpoint.json",
    {"state": _encode(checkpoint_state), "next_node": "tools_news", "step": 3},
)
