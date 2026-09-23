"""Exercise actual SDK serialization/parsing with an in-memory HTTP transport."""

import importlib
import json

import httpx
import pytest
from pydantic import BaseModel

from cli.stats_handler import StatsCallbackHandler
from tradingagents.llm_clients.factory import create_llm_client
from tradingagents.runtime.messages import HumanMessage, SystemMessage, ToolMessage
from tradingagents.runtime.tools import tool


@tool
def quote(symbol: str) -> str:
    """Return a fixed quote."""
    return symbol + ":100"


class Pick(BaseModel):
    action: str


def transport(responses, provider=None):
    http = httpx
    client_class = httpx.Client
    if provider == "anthropic":
        from anthropic import DefaultHttpxClient

        client_class = DefaultHttpxClient
        package = next(
            c.__module__.split(".")[0]
            for c in client_class.__mro__
            if c.__module__.startswith("httpx")
        )
        http = importlib.import_module(package)
    requests = []

    def handle(request):
        requests.append((str(request.url), json.loads(request.content)))
        status, body = responses.pop(0)
        return http.Response(status, json=body)

    return client_class(transport=http.MockTransport(handle)), requests


def completion(content="answer", calls=None, reasoning=None):
    message = {"role": "assistant", "content": content}
    if calls:
        message["tool_calls"] = calls
    if reasoning:
        message["reasoning_content"] = reasoning
    return {
        "id": "completion-id",
        "object": "chat.completion",
        "created": 1,
        "model": "m",
        "choices": [
            {"index": 0, "message": message, "finish_reason": "tool_calls" if calls else "stop"}
        ],
        "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
    }


def wire_call(name="quote", args=None):
    return {
        "id": "call-1",
        "type": "function",
        "function": {"name": name, "arguments": json.dumps(args or {"symbol": "AAPL"})},
    }


@pytest.mark.parametrize(
    "provider,model",
    [
        ("deepseek", "deepseek-v4-flash"),
        ("minimax", "MiniMax-M2.7"),
        ("openai_compatible", "local-model"),
        ("azure", "deployment"),
    ],
)
def test_chat_tool_roundtrip_uses_actual_sdk(provider, model, monkeypatch):
    monkeypatch.setenv("AZURE_OPENAI_ENDPOINT", "https://azure.example")
    monkeypatch.setenv("OPENAI_API_VERSION", "2025-03-01-preview")
    client, requests = transport(
        [(200, completion("", [wire_call()], "reasoning to retain")), (200, completion())]
    )
    stats = StatsCallbackHandler()
    llm = create_llm_client(
        provider,
        model,
        base_url="https://proxy.example/v1",
        api_key="test",
        http_client=client,
        callbacks=[stats],
        max_retries=0,
    ).get_llm()
    bound = llm.bind_tools([quote])
    messages = [SystemMessage("system"), HumanMessage("price?")]
    first = bound.invoke(messages)
    assert first.tool_calls[0]["args"] == {"symbol": "AAPL"}
    result = bound.invoke(
        [*messages, first, ToolMessage("100", tool_call_id="call-1", name="quote")]
    )
    assert result.content == "answer"
    payload = requests[-1][1]
    assert [m["role"] for m in payload["messages"]] == ["system", "user", "assistant", "tool"]
    assert payload["messages"][-1]["tool_call_id"] == "call-1"
    if provider == "deepseek":
        assert payload["messages"][-2]["reasoning_content"] == "reasoning to retain"
    if provider == "minimax":
        assert payload["reasoning_split"] is True
    if provider == "azure":
        assert "/deployments/deployment/chat/completions" in requests[0][0]
    assert stats.get_stats() == {"llm_calls": 2, "tool_calls": 0, "tokens_in": 20, "tokens_out": 10}


def response(output):
    return {
        "id": "resp-1",
        "object": "response",
        "created_at": 1,
        "status": "completed",
        "model": "gpt-5",
        "output": output,
        "usage": {"input_tokens": 9, "output_tokens": 3, "total_tokens": 12},
    }


def test_responses_preserves_reasoning_and_call_id():
    output = [
        {"type": "reasoning", "id": "reason-1", "summary": []},
        {
            "type": "function_call",
            "id": "fc-1",
            "call_id": "call-1",
            "name": "quote",
            "arguments": '{"symbol":"AAPL"}',
            "status": "completed",
        },
    ]
    answer = [
        {
            "type": "message",
            "id": "msg-1",
            "role": "assistant",
            "status": "completed",
            "content": [{"type": "output_text", "text": "done", "annotations": []}],
        }
    ]
    client, requests = transport([(200, response(output)), (200, response(answer))])
    llm = create_llm_client(
        "openai",
        "gpt-5",
        api_key="test",
        http_client=client,
        reasoning_effort="low",
        max_tokens=128,
    ).get_llm()
    model = llm.bind_tools([quote])
    first = model.invoke("price?")
    final = model.invoke([HumanMessage("price?"), first, ToolMessage("100", tool_call_id="call-1")])
    assert final.content == "done"
    payload = requests[-1][1]
    assert payload["input"][1:3] == output
    assert payload["input"][-1] == {
        "type": "function_call_output",
        "call_id": "call-1",
        "output": "100",
    }
    assert payload["reasoning"] == {"effort": "low"}
    assert payload["max_output_tokens"] == 128
    assert payload["tools"][0]["name"] == "quote"
    assert requests[-1][0].endswith("/responses")


def anthropic_response(blocks):
    return {
        "id": "msg-1",
        "type": "message",
        "role": "assistant",
        "model": "claude-sonnet-5",
        "content": blocks,
        "stop_reason": "tool_use",
        "usage": {"input_tokens": 8, "output_tokens": 4},
    }


def test_anthropic_signed_thinking_and_tool_results_survive_normalization():
    blocks = [
        {"type": "thinking", "thinking": "think", "signature": "signed"},
        {"type": "tool_use", "id": "call-1", "name": "quote", "input": {"symbol": "AAPL"}},
    ]
    client, requests = transport(
        [
            (200, anthropic_response(blocks)),
            (200, anthropic_response([{"type": "text", "text": "done"}])),
        ],
        "anthropic",
    )
    llm = create_llm_client(
        "anthropic",
        "claude-sonnet-5",
        api_key="test",
        http_client=client,
        effort="high",
        max_tokens=128,
    ).get_llm()
    model = llm.bind_tools([quote])
    first = model.invoke([SystemMessage("sys"), HumanMessage("price?")])
    assert first.content == ""
    final = model.invoke(
        [
            SystemMessage("sys"),
            HumanMessage("price?"),
            first,
            ToolMessage("100", tool_call_id="call-1", name="quote"),
        ]
    )
    assert final.content == "done"
    payload = requests[-1][1]
    assert payload["messages"][1]["content"] == blocks
    assert payload["messages"][-1]["content"][0]["tool_use_id"] == "call-1"
    assert payload["output_config"]["effort"] == "high"
    assert payload["max_tokens"] == 128


def gemini_response(parts):
    return {
        "candidates": [{"content": {"role": "model", "parts": parts}, "finishReason": "STOP"}],
        "usageMetadata": {
            "promptTokenCount": 8,
            "candidatesTokenCount": 3,
            "thoughtsTokenCount": 2,
        },
    }


def test_google_thought_signature_and_function_result_use_actual_sdk():
    parts = [
        {
            "functionCall": {"name": "quote", "args": {"symbol": "AAPL"}},
            "thoughtSignature": "c2lnbmVk",
        }
    ]
    client, requests = transport(
        [(200, gemini_response(parts)), (200, gemini_response([{"text": "done"}]))]
    )
    llm = create_llm_client(
        "google",
        "gemini-3.5-flash",
        api_key="test",
        http_client=client,
        thinking_level="low",
        max_output_tokens=128,
        max_retries=1,
    ).get_llm()
    model = llm.bind_tools([quote])
    first = model.invoke("price?")
    final = model.invoke(
        [
            HumanMessage("price?"),
            first,
            ToolMessage("100", tool_call_id=first.tool_calls[0]["id"], name="quote"),
        ]
    )
    assert final.content == "done"
    payload = requests[-1][1]
    assert payload["contents"][1]["parts"][0]["thoughtSignature"] == "c2lnbmVk"
    assert payload["contents"][-1]["parts"][0]["functionResponse"]["name"] == "quote"
    assert payload["generationConfig"]["maxOutputTokens"] == 128
    assert final.usage_metadata["output_tokens"] == 5


@pytest.mark.parametrize(
    "provider,model,body",
    [
        ("openai_compatible", "m", completion("", [wire_call("Pick", {"action": "BUY"})])),
        (
            "anthropic",
            "claude-sonnet-5",
            anthropic_response(
                [{"type": "tool_use", "id": "c", "name": "Pick", "input": {"action": "BUY"}}]
            ),
        ),
        ("google", "gemini-3.5-flash", gemini_response([{"text": '{"action":"BUY"}'}])),
    ],
)
def test_structured_outputs_are_pydantic_instances(provider, model, body):
    client, requests = transport([(200, body)], provider)
    llm = create_llm_client(
        provider, model, base_url="https://proxy.example/v1", api_key="test", http_client=client
    ).get_llm()
    result = llm.with_structured_output(Pick).invoke("pick")
    assert result == Pick(action="BUY")
    assert len(requests) == 1


def test_structured_parse_failure_uses_the_existing_freetext_fallback():
    from tradingagents.agents.utils.structured import invoke_structured_or_freetext

    client, requests = transport(
        [(200, completion("", [wire_call("Pick", {"wrong": 1})])), (200, completion("fallback"))]
    )
    llm = create_llm_client(
        "openai_compatible",
        "m",
        base_url="https://proxy.example",
        api_key="test",
        http_client=client,
    ).get_llm()
    result = invoke_structured_or_freetext(
        llm.with_structured_output(Pick), llm, "identical prompt", lambda p: p.action, "test"
    )
    assert result == "fallback"
    assert requests[0][1]["messages"] == requests[1][1]["messages"]
    assert "tools" not in requests[1][1]


def test_sdk_retry_budget_and_callbacks_count_logical_calls():
    client, requests = transport(
        [(429, {"error": {"message": "busy", "type": "rate_limit"}}), (200, completion())]
    )
    stats = StatsCallbackHandler()
    llm = create_llm_client(
        "openai_compatible",
        "m",
        base_url="https://proxy.example",
        api_key="test",
        http_client=client,
        max_retries=1,
        callbacks=[stats],
    ).get_llm()
    assert llm.invoke("hi").content == "answer"
    assert len(requests) == 2 and stats.llm_calls == 1


def test_o_series_chat_system_role_and_temperature_are_preserved():
    from tradingagents.llm_clients.openai_client import NormalizedChatOpenAI

    llm = NormalizedChatOpenAI(model="o1", api_key="test")
    payload = llm._get_request_payload([SystemMessage("system"), HumanMessage("hi")])
    assert payload["messages"][0]["role"] == "developer"
    assert payload["temperature"] == 1


def test_bedrock_converse_boto3_serialization(monkeypatch):
    pytest.importorskip("boto3")
    from botocore.stub import Stubber

    monkeypatch.setenv("AWS_ACCESS_KEY_ID", "test")
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "test")
    monkeypatch.delenv("AWS_BEARER_TOKEN_BEDROCK", raising=False)
    llm = create_llm_client("bedrock", "anthropic.claude-v2", max_tokens=128).get_llm()
    model = llm.bind_tools([quote])
    response_ = {
        "output": {
            "message": {
                "role": "assistant",
                "content": [
                    {
                        "toolUse": {
                            "toolUseId": "call-1",
                            "name": "quote",
                            "input": {"symbol": "AAPL"},
                        }
                    }
                ],
            }
        },
        "stopReason": "tool_use",
        "usage": {"inputTokens": 5, "outputTokens": 3, "totalTokens": 8},
        "metrics": {"latencyMs": 1},
    }
    with Stubber(llm._sdk()) as stub:
        expected = llm._get_request_payload([HumanMessage("price?")], **model.kwargs)
        stub.add_response("converse", response_, expected)
        first = model.invoke("price?")
        assert first.tool_calls[0]["args"] == {"symbol": "AAPL"}
        messages = [
            HumanMessage("price?"),
            first,
            ToolMessage("100", name="quote", tool_call_id="call-1"),
        ]
        expected = llm._get_request_payload(messages, **model.kwargs)
        assert expected["messages"][-1]["content"][0]["toolResult"]["toolUseId"] == "call-1"
        response_["output"]["message"]["content"] = [{"text": "done"}]
        stub.add_response("converse", response_, expected)
        assert model.invoke(messages).content == "done"


def test_bedrock_bearer_auth_takes_precedence(monkeypatch):
    pytest.importorskip("boto3")
    monkeypatch.setenv("AWS_BEARER_TOKEN_BEDROCK", "test-token")
    monkeypatch.setenv("AWS_ACCESS_KEY_ID", "ambient-key")
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
    llm = create_llm_client("bedrock", "anthropic.claude-v2").get_llm()
    sdk = llm._sdk()
    assert sdk.meta.config.auth_scheme_preference == "httpBearerAuth"
    from botocore.awsrequest import AWSResponse

    class Raw:
        def stream(self, **kwargs):
            yield json.dumps(
                {
                    "output": {"message": {"role": "assistant", "content": [{"text": "ok"}]}},
                    "stopReason": "end_turn",
                    "usage": {"inputTokens": 1, "outputTokens": 1, "totalTokens": 2},
                    "metrics": {"latencyMs": 1},
                }
            ).encode()

    headers = []

    def send(request, **kwargs):
        headers.append(request.headers["Authorization"])
        return AWSResponse(request.url, 200, {"content-type": "application/json"}, Raw())

    sdk.meta.events.register("before-send.bedrock-runtime.Converse", send)
    assert llm.invoke("hello").content == "ok"
    assert headers == [b"Bearer test-token"]
