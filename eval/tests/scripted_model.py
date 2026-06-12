"""A scripted chat model that replays a fixed sequence of AIMessages.

Lets the runner be tested end-to-end (real substrate, real agent graph) without
an API key. It ignores the input and returns its queued responses in order.
"""

from __future__ import annotations

from typing import Any, List

from langchain_core.language_models import BaseChatModel
from langchain_core.messages import AIMessage
from langchain_core.outputs import ChatGeneration, ChatResult
from pydantic import PrivateAttr


class ScriptedModel(BaseChatModel):
    _responses: List[AIMessage] = PrivateAttr()
    _i: int = PrivateAttr(default=0)

    def __init__(self, responses: List[AIMessage], **kw: Any):
        super().__init__(**kw)
        self._responses = responses
        self._i = 0

    def bind_tools(self, tools: Any, **kw: Any) -> "ScriptedModel":
        return self

    def _generate(self, messages, stop=None, run_manager=None, **kw) -> ChatResult:
        msg = self._responses[min(self._i, len(self._responses) - 1)]
        self._i += 1
        return ChatResult(generations=[ChatGeneration(message=msg)])

    @property
    def _llm_type(self) -> str:
        return "scripted"
