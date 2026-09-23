"""Compare the replacement against artifacts captured from commit 2d17df8."""

import ast
import hashlib
import json
from pathlib import Path

import pytest
from pydantic import BaseModel

from tests.runtime_reference import reference
from tradingagents.graph.trading_graph import TradingAgentsGraph
from tradingagents.runtime.messages import HumanMessage, SystemMessage
from tradingagents.runtime.tools import tool_spec

FIXTURES = Path(__file__).parent / "fixtures"


def test_original_graph_prompts_routes_messages_tools_and_final_state():
    expected = json.loads((FIXTURES / "runtime_reference.json").read_text(encoding="utf-8"))
    assert reference() == expected


def test_original_model_visible_tool_schemas():
    nodes = TradingAgentsGraph._create_tool_nodes(None)
    actual = {t.name: tool_spec(t) for node in nodes.values() for t in node.tools_by_name.values()}
    expected = json.loads((FIXTURES / "tool_schemas.json").read_text(encoding="utf-8"))
    assert actual == expected


class Pick(BaseModel):
    action: str


@pytest.mark.parametrize(
    "case",
    json.loads((FIXTURES / "provider_reference.json").read_text(encoding="utf-8")),
    ids=lambda case: case["options"]["model"] + str(case["options"].get("reasoning_effort", "")),
)
def test_original_provider_structured_requests(case):
    from tradingagents.llm_clients import anthropic_client, openai_client

    module = anthropic_client if case["class"] == "NormalizedChatAnthropic" else openai_client
    llm = getattr(module, case["class"])(api_key="placeholder", **case["options"])
    bound = llm.with_structured_output(Pick)
    messages = [SystemMessage("system"), HumanMessage("pick")]
    payload = llm._get_request_payload(messages, **bound.kwargs)
    if getattr(llm, "use_responses_api", False):
        payload = llm._responses_payload(messages, payload)
    assert payload == case["payload"]


def test_business_bodies_prompts_graph_wiring_and_data_sources_are_unchanged():
    class WithoutImports(ast.NodeTransformer):
        def visit_Import(self, node):
            return None

        def visit_ImportFrom(self, node):
            return None

    expected = json.loads((FIXTURES / "business_reference.json").read_text(encoding="utf-8"))
    root = Path(__file__).resolve().parents[1]
    for path, sha in expected["ast"].items():
        tree = ast.parse((root / path).read_text(encoding="utf-8"))
        if (
            tree.body
            and isinstance(tree.body[0], ast.Expr)
            and isinstance(tree.body[0].value, ast.Constant)
            and isinstance(tree.body[0].value.value, str)
        ):
            tree.body.pop(0)
        assert hashlib.sha256(ast.dump(WithoutImports().visit(tree)).encode()).hexdigest() == sha, (
            path
        )
    for path, sha in expected["bytes"].items():
        assert (
            hashlib.sha256((root / path).read_text(encoding="utf-8").encode()).hexdigest() == sha
        ), path
