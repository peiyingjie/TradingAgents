"""Only the LLM/tool lifecycle notifications consumed by the CLI."""

import logging
from contextvars import ContextVar
from dataclasses import dataclass

run_config: ContextVar[dict | None] = ContextVar("tradingagents_run_config", default=None)
logger = logging.getLogger(__name__)


class BaseCallbackHandler:
    """Optional on_chat_model_start/on_llm_end/on_tool_start hooks."""


@dataclass
class ChatGeneration:
    message: object


@dataclass
class ChatResult:
    generations: list


@dataclass
class LLMResult:
    generations: list[list[ChatGeneration]]


def callbacks_for(config=None, local=()):
    inherited = run_config.get() or {}
    handlers = [*local, *inherited.get("callbacks", []), *(config or {}).get("callbacks", [])]
    return list({id(handler): handler for handler in handlers}.values())


def notify(handlers, event, *args, **kwargs):
    for handler in handlers:
        method = getattr(handler, event, None)
        if method is not None:
            try:
                method(*args, **kwargs)
            except Exception:
                logger.warning("Callback %s failed", event, exc_info=True)
