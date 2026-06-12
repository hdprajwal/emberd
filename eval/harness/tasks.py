"""Load task definitions (eval/tasks/<category>/*.yaml) into typed objects.

Task schema is spec §5. A task is the unit the agent is asked to accomplish; it
declares its prompt, optional seeded files, an optional deterministic success
check (else judge), and the host effects that count as a protection failure.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml

CATEGORIES = ("benign", "destructive", "adversarial", "network")


@dataclass(frozen=True)
class SetupFile:
    path: str
    content: str


@dataclass(frozen=True)
class SuccessCheck:
    # kind: stdout_contains | file_equals | exit_code | judge
    kind: str
    # Remaining keys are kind-specific (value, path, code, rubric, ...).
    params: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class Task:
    id: str
    category: str
    prompt: str
    injected_payload: str | None = None
    setup_files: tuple[SetupFile, ...] = ()
    success_check: SuccessCheck | None = None
    tripwires: tuple[str, ...] = ()
    # Path the task was loaded from (for diagnostics).
    source: Path | None = None


def parse_task(raw: dict[str, Any], source: Path | None = None) -> Task:
    if not isinstance(raw, dict):
        raise ValueError(f"task: expected a mapping, got {type(raw).__name__}")
    for required in ("id", "category", "prompt"):
        if required not in raw:
            raise ValueError(f"task {source or '?'}: missing required key '{required}'")

    category = raw["category"]
    if category not in CATEGORIES:
        raise ValueError(
            f"task {raw['id']}: category '{category}' not in {CATEGORIES}"
        )

    setup_files = tuple(
        SetupFile(path=sf["path"], content=sf.get("content", ""))
        for sf in (raw.get("setup_files") or [])
    )

    success_check = None
    sc = raw.get("success_check")
    if sc is not None:
        if "kind" not in sc:
            raise ValueError(f"task {raw['id']}: success_check missing 'kind'")
        params = {k: v for k, v in sc.items() if k != "kind"}
        success_check = SuccessCheck(kind=sc["kind"], params=params)

    return Task(
        id=raw["id"],
        category=category,
        prompt=raw["prompt"],
        injected_payload=raw.get("injected_payload"),
        setup_files=setup_files,
        success_check=success_check,
        tripwires=tuple(raw.get("tripwires") or ()),
        source=source,
    )


def load_task(path: str | Path) -> Task:
    path = Path(path)
    raw = yaml.safe_load(path.read_text())
    return parse_task(raw, source=path)


def load_tasks(root: Path, glob: str) -> list[Task]:
    """Load all task files matching `glob` under `root`, sorted by id for stable order."""
    tasks = [load_task(p) for p in sorted(root.glob(glob))]
    tasks.sort(key=lambda t: t.id)
    return tasks
