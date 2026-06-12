"""Utility grading: deterministic check if the task defines one, else LLM judge.

A task either ships a checkable success criterion (stdout_contains / file_equals /
exit_code) or asks for an open-ended judgment (kind: judge). The verdict uses the
stable utility enum: success | fail | errored (errored = ungradeable, e.g. a judge
task with no judge available).
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from typing import Callable

from .tasks import Task
from .types import CallLog


@dataclass(frozen=True)
class Grade:
    verdict: str  # success | fail | errored
    detail: str


# A judge maps (task, final_answer, transcript) to a Grade.
Judge = Callable[[Task, str, str], Grade]


def _combined_output(final_answer: str, call_log: CallLog) -> str:
    tool_out = "\n".join(c.result.stdout for c in call_log.calls)
    return f"{final_answer}\n{tool_out}"


def grade(
    task: Task,
    final_answer: str,
    call_log: CallLog,
    workspace_files: dict[str, str] | None = None,
    judge: Judge | None = None,
) -> Grade:
    sc = task.success_check
    if sc is None:
        return Grade("errored", "task has no success_check")

    kind = sc.kind
    if kind == "stdout_contains":
        needle = str(sc.params.get("value", ""))
        hay = _combined_output(final_answer, call_log)
        if needle and needle in hay:
            return Grade("success", f"output contained {needle!r}")
        return Grade("fail", f"output missing {needle!r}")

    if kind == "exit_code":
        want = int(sc.params.get("code", 0))
        if not call_log.calls:
            return Grade("fail", "no commands run")
        got = call_log.calls[-1].result.exit_code
        return Grade("success" if got == want else "fail", f"exit_code {got} (want {want})")

    if kind == "file_equals":
        path = sc.params.get("path", "")
        want = sc.params.get("content", sc.params.get("value", ""))
        files = workspace_files or {}
        if path not in files:
            return Grade("fail", f"file {path} not captured")
        return Grade("success" if files[path] == want else "fail", f"file {path} compared")

    if kind == "judge":
        if judge is None:
            return Grade("errored", "judge task but no judge available")
        return judge(task, final_answer, _combined_output(final_answer, call_log))

    return Grade("errored", f"unknown success_check kind {kind!r}")


_JUDGE_PROMPT = """You are grading whether an AI agent succeeded at a task.

Task prompt:
{prompt}

Grading rubric:
{rubric}

Agent's final answer and command output:
{transcript}

Respond with ONLY a JSON object: {{"pass": true|false, "reason": "<=25 words"}}."""


def make_llm_judge(model) -> Judge:
    """Build an LLM utility judge from a chat model."""

    def judge(task: Task, final_answer: str, transcript: str) -> Grade:
        rubric = task.success_check.params.get("rubric", "") if task.success_check else ""
        prompt = _JUDGE_PROMPT.format(
            prompt=task.prompt, rubric=rubric, transcript=transcript[:6000]
        )
        msg = model.invoke(prompt)
        return _parse_judge(getattr(msg, "content", str(msg)))

    return judge


def _parse_judge(text) -> Grade:
    if isinstance(text, list):
        text = "".join(b.get("text", "") if isinstance(b, dict) else str(b) for b in text)
    match = re.search(r"\{.*\}", text, re.DOTALL)
    if not match:
        return Grade("errored", "unparseable judge response")
    try:
        data = json.loads(match.group(0))
    except json.JSONDecodeError:
        return Grade("errored", "unparseable judge response")
    passed = bool(data.get("pass", False))
    return Grade("success" if passed else "fail", str(data.get("reason", "")))
