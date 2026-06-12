"""The per-trial JSONL trajectory artifact (spec §7).

One JSONL file per trial: a header record, one step record per agent step, and a
final outcome record. The schema is a **frozen contract** for a future analyzer —
every field below is reserved from the start; later build phases populate the
ones they compute (danger labels in Phase 4, tripwire deltas in Phase 2) without
changing field names or shapes. Bump SCHEMA_VERSION on any breaking change.

Field names and enums are stable so the file doubles as the analysis-ready layer:
  record_type ∈ {header, step, outcome}
  variant     ∈ {shell, sandbox}
  category    ∈ {benign, destructive, adversarial, network}
  danger      ∈ {none, low, medium, high, unknown}
  utility     ∈ {success, fail, errored, truncated}
  containment ∈ {contained, breached, n/a}
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

from .types import ExecResult

SCHEMA_VERSION = "1.0"
HARNESS_VERSION = "0.1.0"


# --- record payloads ---------------------------------------------------------


@dataclass
class TrajectoryMeta:
    run_id: str
    task_id: str
    category: str
    variant: str  # "shell" | "sandbox"
    model_id: str
    temperature: float
    seed: int
    pair_id: str  # shared by the shell+sandbox trajectories for one (task, seed)
    started_at: str  # ISO-8601
    emberd_git_sha: str
    harness_version: str = HARNESS_VERSION
    schema_version: str = SCHEMA_VERSION


@dataclass
class TaskContext:
    prompt: str
    injected_payload: str | None
    setup_files: list[dict[str, str]]
    success_check: dict[str, Any] | None
    tripwires: list[str]


@dataclass
class DangerLabel:
    # static rules (Phase 4); judge filled by the LLM classifier (Phase 4).
    static_label: str = "unknown"  # none|low|medium|high|unknown
    static_rule: str | None = None
    judge_label: str = "unknown"
    judge_rationale: str | None = None


@dataclass
class StepRecord:
    index: int
    reasoning: str  # full visible model text for this step
    tool: str | None  # "bash" | "run_code" | None (no tool call)
    tool_input: str | None  # verbatim command/code string
    result: dict[str, Any] | None  # ExecResult as dict
    danger: dict[str, Any] = field(default_factory=lambda: asdict(DangerLabel()))
    tripwire_deltas: list[dict[str, Any]] = field(default_factory=list)


@dataclass
class OutcomeRecord:
    status: str  # success|fail|errored|truncated
    utility_verdict: str  # success|fail|errored|truncated
    utility_detail: str | None
    violations_total: int
    violations_by_type: dict[str, int]
    containment: str  # contained|breached|n/a
    prompt_tokens: int
    completion_tokens: int
    total_tokens: int
    latency_ms: int
    teardown: str  # ok|failed
    ended_at: str


def result_to_dict(result: ExecResult | None) -> dict[str, Any] | None:
    if result is None:
        return None
    return {
        "stdout": result.stdout,
        "stderr": result.stderr,
        "exit_code": result.exit_code,
        "duration_ms": result.duration_ms,
        "error": result.error,
    }


# --- writer ------------------------------------------------------------------


class TrajectoryWriter:
    """Append-only JSONL writer enforcing header → steps → outcome ordering."""

    def __init__(self, path: str | Path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._fh = self.path.open("w", encoding="utf-8")
        self._step_count = 0
        self._wrote_header = False
        self._wrote_outcome = False

    def _emit(self, obj: dict[str, Any]) -> None:
        self._fh.write(json.dumps(obj, ensure_ascii=False) + "\n")
        self._fh.flush()

    def write_header(self, meta: TrajectoryMeta, task: TaskContext) -> None:
        if self._wrote_header:
            raise RuntimeError("header already written")
        self._emit({"record_type": "header", "meta": asdict(meta), "task": asdict(task)})
        self._wrote_header = True

    def write_step(self, step: StepRecord) -> None:
        if not self._wrote_header:
            raise RuntimeError("write_header must precede steps")
        if self._wrote_outcome:
            raise RuntimeError("cannot write steps after outcome")
        payload = asdict(step)
        payload["record_type"] = "step"
        self._emit(payload)
        self._step_count += 1

    def write_outcome(self, outcome: OutcomeRecord) -> None:
        if not self._wrote_header:
            raise RuntimeError("write_header must precede outcome")
        if self._wrote_outcome:
            raise RuntimeError("outcome already written")
        payload = asdict(outcome)
        payload["record_type"] = "outcome"
        self._emit(payload)
        self._wrote_outcome = True

    def close(self) -> None:
        self._fh.close()

    def __enter__(self) -> "TrajectoryWriter":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()


def read_trajectory(path: str | Path) -> list[dict[str, Any]]:
    """Parse a trajectory file back into a list of records (for tests/tools)."""
    records = []
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        if line.strip():
            records.append(json.loads(line))
    return records
