"""Unit tests for message→step mapping (no model, no substrate)."""

from __future__ import annotations

from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

from harness.runner import _usage, messages_to_steps
from harness.types import CallLog, ExecResult


def _ai_with_tool_call(text, name, call_id, usage=None):
    msg = AIMessage(
        content=text,
        tool_calls=[{"name": name, "args": {"command": "echo hi"}, "id": call_id}],
    )
    if usage:
        msg.usage_metadata = {"input_tokens": usage[0], "output_tokens": usage[1], "total_tokens": sum(usage)}
    return msg


def test_maps_tool_call_to_raw_result():
    log = CallLog()
    log.record("bash", "echo hi", ExecResult("hi\n", "", 0, 3))

    messages = [
        HumanMessage(content="do it"),
        _ai_with_tool_call("Let me run it.", "bash", "c1"),
        ToolMessage(content="exit_code: 0", tool_call_id="c1"),
        AIMessage(content="Done. The answer is hi."),
    ]
    steps = messages_to_steps(messages, log)
    assert len(steps) == 2
    assert steps[0].tool == "bash"
    assert steps[0].tool_input == "echo hi"  # verbatim from call log
    assert steps[0].result["exit_code"] == 0
    assert steps[0].reasoning == "Let me run it."
    # terminal step: final answer, no tool call
    assert steps[1].tool is None
    assert "Done" in steps[1].reasoning


def test_usage_aggregates_across_ai_messages():
    messages = [
        _ai_with_tool_call("a", "bash", "c1", usage=(100, 10)),
        AIMessage(content="b"),
        _ai_with_tool_call("c", "bash", "c2", usage=(50, 5)),
    ]
    pt, ct, tt = _usage(messages)
    assert (pt, ct, tt) == (150, 15, 165)
