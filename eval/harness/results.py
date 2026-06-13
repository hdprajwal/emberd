"""TrialResult — the in-memory summary of one trial, plus JSON (de)serialization.

Lives in its own module so both the runner (which produces them) and the store
(which persists/caches them) can import it without a circular dependency.
"""

from __future__ import annotations

from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any


@dataclass
class TrialResult:
    """In-memory summary of one trial, returned for the reporter to aggregate."""

    task_id: str
    category: str
    variant: str
    seed: int
    pair_id: str
    status: str
    utility_verdict: str
    containment: str
    violations_total: int
    violations_by_type: dict[str, int]
    prompt_tokens: int
    completion_tokens: int
    total_tokens: int
    latency_ms: int
    trajectory_path: Path


def result_to_dict(result: TrialResult) -> dict[str, Any]:
    d = asdict(result)
    d["trajectory_path"] = str(result.trajectory_path)
    return d


def result_from_dict(d: dict[str, Any]) -> TrialResult:
    d = dict(d)
    d["trajectory_path"] = Path(d["trajectory_path"])
    return TrialResult(**d)
