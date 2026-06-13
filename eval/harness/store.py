"""Persistent results store with fingerprint-based caching.

A single stable directory accumulates results across runs so re-running the
harness only executes trials it doesn't already have. Each trial is keyed by
`<task>__<variant>__seed<n>` and tagged with a fingerprint of everything that
would change its outcome (task definition + variant + seed + model). A cached
trial is reused when its fingerprint matches; change a task or the model and the
fingerprint changes, forcing that cell to re-run. `--force` bypasses the cache.

Layout (the "accessible from outside" location):
    <store>/index.json          manifest: key -> {fingerprint, result}
    <store>/trajectories/*.jsonl one per cell (latest run of that cell)
    <store>/summary.md           regenerated over the WHOLE store each run
    <store>/scoreboard.csv
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

from .config import ModelConfig
from .results import TrialResult, result_from_dict, result_to_dict
from .tasks import Task

INDEX_SCHEMA = 1


def trial_key(task_id: str, variant: str, seed: int) -> str:
    return f"{task_id}__{variant}__seed{seed}"


def fingerprint(task: Task, variant: str, seed: int, model: ModelConfig) -> str:
    """Stable hash of everything that determines a trial's result."""
    payload = {
        "task": {
            "id": task.id,
            "category": task.category,
            "prompt": task.prompt,
            "injected_payload": task.injected_payload,
            "setup_files": [[sf.path, sf.content] for sf in task.setup_files],
            "success_check": (
                {"kind": task.success_check.kind, "params": task.success_check.params}
                if task.success_check else None
            ),
            "tripwires": list(task.tripwires),
        },
        "variant": variant,
        "seed": seed,
        "model": {
            "id": model.id,
            "temperature": model.temperature,
            "max_tokens": model.max_tokens,
        },
    }
    blob = json.dumps(payload, sort_keys=True, ensure_ascii=False)
    return hashlib.sha256(blob.encode("utf-8")).hexdigest()


class ResultStore:
    """Reads/writes the persistent trial index and trajectories."""

    def __init__(self, store_dir: str | Path):
        self.dir = Path(store_dir)
        self.index_path = self.dir / "index.json"
        self.trajectories_dir = self.dir / "trajectories"

    def load(self) -> dict[str, dict[str, Any]]:
        """Return the trials map {key: {fingerprint, result}} (empty if none)."""
        if not self.index_path.exists():
            return {}
        data = json.loads(self.index_path.read_text())
        return data.get("trials", {})

    def cached(
        self, index: dict[str, dict[str, Any]], key: str, fp: str
    ) -> TrialResult | None:
        """Return the stored result if present with a matching fingerprint."""
        entry = index.get(key)
        if entry and entry.get("fingerprint") == fp:
            return result_from_dict(entry["result"])
        return None

    def put(
        self, index: dict[str, dict[str, Any]], key: str, fp: str, result: TrialResult
    ) -> None:
        index[key] = {"fingerprint": fp, "result": result_to_dict(result)}

    def save(self, index: dict[str, dict[str, Any]]) -> None:
        self.dir.mkdir(parents=True, exist_ok=True)
        self.index_path.write_text(
            json.dumps({"schema": INDEX_SCHEMA, "trials": index}, indent=2)
        )

    def all_results(self, index: dict[str, dict[str, Any]]) -> list[TrialResult]:
        """Every stored result, sorted for stable report/scoreboard ordering."""
        results = [result_from_dict(e["result"]) for e in index.values()]
        results.sort(key=lambda r: (r.category, r.task_id, r.variant, r.seed))
        return results
