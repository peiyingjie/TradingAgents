"""Generate offline Python -> Go agent parity cases (development only)."""
# ruff: noqa: E402 -- bootstrap the repository import path before reference imports.

import copy
import importlib
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from tradingagents.dataflows.config import set_config
from tradingagents.graph.propagation import Propagator
from tradingagents.runtime.messages import AIMessage, to_messages


class Recorder:
    def __init__(self):
        self.calls = []

    def with_structured_output(self, *a, **k):
        return self

    def bind_tools(self, tools):
        return self

    def invoke(self, input, **kwargs):
        self.calls.append([{"type": m.type, "content": m.content} for m in to_messages(input)])
        return AIMessage("reference response")


agents = [
    ("market", "analysts.market_analyst", "create_market_analyst"),
    ("news", "analysts.news_analyst", "create_news_analyst"),
    ("fundamentals", "analysts.fundamentals_analyst", "create_fundamentals_analyst"),
    ("social", "analysts.sentiment_analyst", "create_sentiment_analyst"),
    ("bull", "researchers.bull_researcher", "create_bull_researcher"),
    ("bear", "researchers.bear_researcher", "create_bear_researcher"),
    ("research", "managers.research_manager", "create_research_manager"),
    ("trader", "trader.trader", "create_trader"),
    ("aggressive", "risk_mgmt.aggressive_debator", "create_aggressive_debator"),
    ("conservative", "risk_mgmt.conservative_debator", "create_conservative_debator"),
    ("neutral", "risk_mgmt.neutral_debator", "create_neutral_debator"),
    ("portfolio", "managers.portfolio_manager", "create_portfolio_manager"),
]
cases = []
for asset, full, language in [
    ("stock", False, "English"),
    ("stock", True, "English"),
    ("crypto", True, "Chinese"),
]:
    set_config({"output_language": language})
    s = Propagator().create_initial_state(
        "AAPL" if asset == "stock" else "BTC-USD", "2025-06-12", asset
    )
    s["messages"] = [AIMessage("previous")]
    s["investment_plan"] = "investment plan"
    s["trader_investment_plan"] = "trader plan"
    if full:
        for field in [
            "market_report",
            "sentiment_report",
            "news_report",
            "fundamentals_report",
            "past_context",
            "portfolio_context",
            "instrument_context",
        ]:
            s[field] = "test " + field
        for field in ["history", "bull_history", "bear_history", "current_response"]:
            s["investment_debate_state"][field] = "test " + field
        for field in [
            "history",
            "aggressive_history",
            "conservative_history",
            "neutral_history",
            "current_aggressive_response",
            "current_conservative_response",
            "current_neutral_response",
        ]:
            s["risk_debate_state"][field] = "test " + field
    for key, mod, fn in agents:
        module = importlib.import_module("tradingagents.agents." + mod)
        if key == "social":
            module.get_news.func = lambda *a, **k: "news block"
            module.fetch_stocktwits_messages = lambda *a, **k: "stocktwits block"
            module.fetch_reddit_posts = lambda *a, **k: "reddit block"
        rec = Recorder()
        update = getattr(module, fn)(rec)(copy.deepcopy(s))

        def encode(v):
            if isinstance(v, AIMessage):
                return {"type": v.type, "content": v.content}
            raise TypeError(type(v))

        cases.append(
            {
                "agent": key,
                "language": language,
                "state": s,
                "messages": rec.calls[-1],
                "update": update,
            }
        )
p = ROOT / "internal/agents/testdata/python_agents.json"
p.parent.mkdir(parents=True, exist_ok=True)
p.write_text(json.dumps(cases, default=encode, ensure_ascii=False, indent=2), encoding="utf-8")
print(f"Captured {len(cases)} Python agent cases.")
