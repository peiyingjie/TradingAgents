"""Capture CLI helper behavior from retained Python; no network or credentials."""

import json
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from cli.utils import select_openrouter_model
from tradingagents.graph.analyst_execution import (
    AnalystWallTimeTracker,
    build_analyst_execution_plan,
    sync_analyst_tracker_from_chunk,
)
from tradingagents.llm_clients.model_catalog import get_known_models
from tradingagents.llm_clients.validators import validate_model


def capture():
    tracker = AnalystWallTimeTracker(build_analyst_execution_plan(["market", "news"]))
    events = [
        {"kind": "start", "key": "market", "now": 100},
        {"kind": "sync", "state": {}, "now": 101},
        {"kind": "sync", "state": {"market_report": "0"}, "now": 110},
        {"kind": "sync", "state": {"market_report": "0", "news_report": "news"}, "now": 112},
        {"kind": "sync", "state": {"market_report": "0", "news_report": "news"}, "now": 120},
    ]
    for event in events:
        if event["kind"] == "start":
            tracker.mark_started(event["key"], event["now"])
        else:
            sync_analyst_tracker_from_chunk(tracker, event["state"], event["now"])
        event["summary"] = tracker.format_summary()
        event["times"] = tracker.get_wall_times()

    models = [
        {"id": "niche/new", "created": 999},
        {"id": "openai/old", "created": 1},
        {"id": "~openai/alias", "created": 1000},
        {"id": "google/latest", "name": "Latest", "created": 2},
    ]
    choices = []

    def picker(*args, **kwargs):
        choices.extend({"Label": c.title, "Value": c.value} for c in kwargs["choices"])
        return SimpleNamespace(ask=lambda: kwargs["choices"][0].value)

    response = SimpleNamespace(raise_for_status=lambda: None, json=lambda: {"data": models})
    with patch("requests.get", return_value=response), patch("cli.utils.questionary.select", side_effect=picker):
        selected = select_openrouter_model("quick")
    validation = [
        {"provider": provider, "model": model, "valid": validate_model(provider, model)}
        for provider, known in get_known_models().items()
        for model in known + ["unknown-future-model"]
    ]
    return {"timing": events, "models": models, "choices": choices, "selected": selected, "validation": validation}


if __name__ == "__main__":
    destination = Path(__file__).resolve().parents[1] / "internal/cli/testdata/python_cli.json"
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(capture(), ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
