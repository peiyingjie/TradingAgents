"""Development-only export of immutable reference assets; never used by Go at runtime."""
# ruff: noqa: E402 -- bootstrap the repository import path before reference imports.

import ast
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))


def write(path, value):
    p = ROOT / path
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(value, encoding="utf-8", newline="\n")


from tradingagents.default_config import DEFAULT_CONFIG

defaults = dict(DEFAULT_CONFIG)
for k in ("project_dir", "results_dir", "data_cache_dir", "memory_log_path"):
    defaults[k] = ""
write("internal/config/defaults.json", json.dumps(defaults, ensure_ascii=False, indent=2))

from tradingagents.agents import schemas
from tradingagents.runtime.tools import tool_spec

write(
    "internal/agents/prompts/schemas.json",
    json.dumps(
        {
            n: tool_spec(getattr(schemas, n))
            for n in ["ResearchPlan", "TraderProposal", "PortfolioDecision", "SentimentReport"]
        },
        ensure_ascii=False,
        indent=2,
    ),
)
write(
    "internal/agents/prompts/response_schemas.json",
    json.dumps(
        {
            n: getattr(schemas, n).model_json_schema()
            for n in ["ResearchPlan", "TraderProposal", "PortfolioDecision", "SentimentReport"]
        },
        ensure_ascii=False,
        indent=2,
    ),
)
from tradingagents.agents.utils import agent_utils

names = [
    "get_stock_data",
    "get_indicators",
    "get_verified_market_snapshot",
    "get_fundamentals",
    "get_balance_sheet",
    "get_cashflow",
    "get_income_statement",
    "get_news",
    "get_global_news",
    "get_insider_transactions",
    "get_macro_indicators",
    "get_prediction_markets",
]
write(
    "internal/tools/specs.json",
    json.dumps(
        {n: tool_spec(getattr(agent_utils, n)) for n in names}, ensure_ascii=False, indent=2
    ),
)


# Capture literal/f-string pieces via AST, without rewording or normalizing whitespace.
def template(node):
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    if isinstance(node, ast.JoinedStr):
        return "".join(
            v.value if isinstance(v, ast.Constant) else "{{" + ast.unparse(v.value) + "}}"
            for v in node.values
        )
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
        return template(node.left) + template(node.right)
    if isinstance(node, ast.Call) and ast.unparse(node.func) == "get_language_instruction":
        return "{{language}}"
    if isinstance(node, ast.Name):
        return "{{" + node.id + "}}"
    raise ValueError(ast.dump(node))


for file in (ROOT / "tradingagents/agents").rglob("*.py"):
    tree = ast.parse(file.read_text(encoding="utf-8"))
    stem = file.stem
    for node in ast.walk(tree):
        if (
            isinstance(node, ast.Assign)
            and len(node.targets) == 1
            and isinstance(node.targets[0], ast.Name)
        ):
            name = node.targets[0].id
            if name in ("prompt", "system_message") and isinstance(
                node.value, (ast.Constant, ast.JoinedStr, ast.BinOp)
            ):
                write(f"internal/agents/prompts/{stem}_{name}.txt", template(node.value))
        if isinstance(node, ast.FunctionDef) and node.name == "_build_system_message":
            ret = next(n for n in ast.walk(node) if isinstance(n, ast.Return))
            write(
                "internal/agents/prompts/sentiment_analyst_system_message.txt",
                template(ret.value).replace("{{get_language_instruction()}}", "{{language}}"),
            )

# Trader's two messages are concatenated expressions, including dynamic grounding.
tree = ast.parse((ROOT / "tradingagents/agents/trader/trader.py").read_text(encoding="utf-8"))
for node in ast.walk(tree):
    if (
        isinstance(node, ast.Assign)
        and isinstance(node.targets[0], ast.Name)
        and node.targets[0].id == "messages"
    ):
        for d in node.value.elts:
            vals = {k.value: v for k, v in zip(d.keys, d.values, strict=True)}
            write(
                "internal/agents/prompts/trader_" + vals["role"].value + ".txt",
                template(vals["content"]),
            )
print("Exported defaults, tool/decision schemas and verbatim prompt templates.")

from yfinance.const import fundamentals_keys

from tradingagents.dataflows import fred, sec_edgar, symbol_utils

metadata = {
    "aliases": symbol_utils._ALIASES,
    "forex": sorted(symbol_utils._FOREX_CURRENCIES),
    "crypto": sorted(symbol_utils._CRYPTO_BASES),
    "macro": fred.MACRO_SERIES,
    "sec_statements": sec_edgar._STATEMENTS,
    "yahoo_financial_keys": fundamentals_keys,
}
tree = ast.parse((ROOT / "tradingagents/dataflows/y_finance.py").read_text(encoding="utf-8"))
for node in ast.walk(tree):
    if (
        isinstance(node, ast.Assign)
        and isinstance(node.targets[0], ast.Name)
        and node.targets[0].id == "best_ind_params"
    ):
        metadata["indicators"] = ast.literal_eval(node.value)
write("internal/dataflows/metadata.json", json.dumps(metadata, ensure_ascii=False, indent=2))
from tradingagents.llm_clients.model_catalog import MODEL_OPTIONS

write("internal/cli/models.json", json.dumps(MODEL_OPTIONS, ensure_ascii=False, indent=2))
from tradingagents.llm_clients.model_catalog import get_known_models

write("internal/llm/known_models.json", json.dumps(get_known_models(), ensure_ascii=False, indent=2))
