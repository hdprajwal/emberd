"""End-to-end matrix + report over the real task suite (shell variant, Docker).

Gated behind EMBERD_EVAL_DOCKER=1. Uses a scripted no-op model so no API key is
needed; proves the orchestration loop and report generation, not model behavior.
"""

from __future__ import annotations

import csv
import io
import os
from dataclasses import replace
from pathlib import Path

import pytest
from langchain_core.messages import AIMessage

from harness.config import load_config
from harness.report import write_report
from harness.runner import run_matrix
from harness.tasks import CATEGORIES
from tests.scripted_model import ScriptedModel

pytestmark = pytest.mark.skipif(
    os.environ.get("EMBERD_EVAL_DOCKER") != "1",
    reason="set EMBERD_EVAL_DOCKER=1 to run Docker integration tests",
)

EVAL_ROOT = Path(__file__).resolve().parent.parent


def test_matrix_shell_then_report(tmp_path: Path):
    cfg = load_config(EVAL_ROOT / "config.yaml")
    cfg = replace(cfg, tasks=replace(cfg.tasks, trials=1))  # keep it quick
    model = ScriptedModel([AIMessage(content="I have completed the task.")])

    results = run_matrix(
        cfg, "testrun", tmp_path, variants=("shell",), model=model, llm_scoring=False
    )

    # one trial per task (4 categories), all on the shell variant.
    assert len(results) == len(CATEGORIES)
    assert {r.variant for r in results} == {"shell"}
    assert all(r.trajectory_path.exists() for r in results)

    write_report(results, "testrun", cfg.model.id, tmp_path)
    assert (tmp_path / "summary.md").exists()
    rows = list(csv.DictReader(io.StringIO((tmp_path / "scoreboard.csv").read_text())))
    assert len(rows) == len(CATEGORIES)
    assert "Capability parity" in (tmp_path / "summary.md").read_text()
