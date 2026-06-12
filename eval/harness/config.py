"""Load and validate the eval harness config (config.yaml)."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml

# Repo-relative default: eval/config.yaml sits one level above harness/.
DEFAULT_CONFIG_PATH = Path(__file__).resolve().parent.parent / "config.yaml"


@dataclass(frozen=True)
class ModelConfig:
    id: str
    temperature: float = 0.0
    max_tokens: int = 4096


@dataclass(frozen=True)
class EmberdConfig:
    base_url: str
    exec_timeout_ms: int = 30000


@dataclass(frozen=True)
class BashHostConfig:
    image: str
    command_timeout_s: int = 30


@dataclass(frozen=True)
class TasksConfig:
    glob: str
    trials: int = 2


@dataclass(frozen=True)
class BudgetConfig:
    max_steps: int = 12
    max_seconds: int = 180


@dataclass(frozen=True)
class Config:
    model: ModelConfig
    emberd: EmberdConfig
    bash_host: BashHostConfig
    tasks: TasksConfig
    budget: BudgetConfig
    results_dir: str = "results"
    # Directory the config was loaded from; paths in the config resolve against it.
    root: Path = field(default=Path.cwd())


def load_config(path: str | Path | None = None) -> Config:
    """Parse config.yaml into a typed Config. Raises on missing required keys."""
    cfg_path = Path(path) if path is not None else DEFAULT_CONFIG_PATH
    raw: dict[str, Any] = yaml.safe_load(cfg_path.read_text()) or {}

    def section(name: str) -> dict[str, Any]:
        value = raw.get(name)
        if not isinstance(value, dict):
            raise ValueError(f"config: missing or invalid '{name}' section")
        return value

    model = section("model")
    emberd = section("emberd")
    bash_host = section("bash_host")
    tasks = section("tasks")
    budget = raw.get("budget") or {}

    return Config(
        model=ModelConfig(
            id=model["id"],
            temperature=float(model.get("temperature", 0.0)),
            max_tokens=int(model.get("max_tokens", 4096)),
        ),
        emberd=EmberdConfig(
            base_url=emberd["base_url"],
            exec_timeout_ms=int(emberd.get("exec_timeout_ms", 30000)),
        ),
        bash_host=BashHostConfig(
            image=bash_host["image"],
            command_timeout_s=int(bash_host.get("command_timeout_s", 30)),
        ),
        tasks=TasksConfig(
            glob=tasks["glob"],
            trials=int(tasks.get("trials", 2)),
        ),
        budget=BudgetConfig(
            max_steps=int(budget.get("max_steps", 12)),
            max_seconds=int(budget.get("max_seconds", 180)),
        ),
        results_dir=raw.get("results_dir", "results"),
        root=cfg_path.resolve().parent,
    )
