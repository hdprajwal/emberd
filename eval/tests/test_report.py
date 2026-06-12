"""Unit tests for the reporter (synthetic TrialResults, no Docker/model)."""

import csv
import io
from pathlib import Path

from harness.report import build_scoreboard_csv, build_summary, write_report
from harness.runner import TrialResult


def _tr(task, cat, variant, seed, utility, containment, by_type=None, **kw):
    return TrialResult(
        task_id=task, category=cat, variant=variant, seed=seed,
        pair_id=f"{task}/{seed}", status="success", utility_verdict=utility,
        containment=containment, violations_total=sum((by_type or {}).values()),
        violations_by_type=by_type or {}, prompt_tokens=kw.get("pt", 100),
        completion_tokens=kw.get("ct", 20), total_tokens=kw.get("tt", 120),
        latency_ms=kw.get("lat", 1000),
        trajectory_path=Path(f"trajectories/{task}__{variant}__seed{seed}.jsonl"),
    )


def _sample() -> list[TrialResult]:
    return [
        # benign: both succeed
        _tr("benign-wc", "benign", "shell", 0, "success", "contained"),
        _tr("benign-wc", "benign", "sandbox", 0, "success", "contained"),
        # destructive: shell breached (fs+secret), sandbox contained
        _tr("destr", "destructive", "shell", 0, "fail", "breached",
            {"fs_delete_outside_workspace": 1, "secret_read": 1}),
        _tr("destr", "destructive", "sandbox", 0, "fail", "contained"),
        # network: shell succeeds, sandbox fails closed
        _tr("net", "network", "shell", 0, "success", "contained"),
        _tr("net", "network", "sandbox", 0, "errored", "contained"),
    ]


def test_summary_has_sections_and_numbers():
    md = build_summary(_sample(), "run1", "anthropic:claude-opus-4-8")
    assert "Capability parity" in md
    assert "Protection" in md
    assert "Tripwire breakdown" in md
    assert "Network tradeoff" in md
    assert "Caveat" in md
    # shell breached 1/3, sandbox 0/3
    assert "1/3" in md  # shell breached count
    assert "fails closed by design" in md
    # by-construction caveat present
    assert "by construction" in md


def test_scoreboard_csv_one_row_per_trial_with_tripwire_columns():
    csv_text = build_scoreboard_csv(_sample(), "run1")
    rows = list(csv.DictReader(io.StringIO(csv_text)))
    assert len(rows) == 6
    assert "fs_delete_outside_workspace" in rows[0]
    destr_shell = next(r for r in rows if r["task_id"] == "destr" and r["variant"] == "shell")
    assert destr_shell["containment"] == "breached"
    assert destr_shell["secret_read"] == "1"
    assert destr_shell["violations_total"] == "2"


def test_write_report_creates_three_files(tmp_path: Path):
    write_report(_sample(), "run1", "anthropic:claude-opus-4-8", tmp_path)
    assert (tmp_path / "summary.md").exists()
    assert (tmp_path / "scoreboard.csv").exists()
    assert (tmp_path / "ANALYSIS_GUIDE.md").exists()
    assert "frozen contract" in (tmp_path / "ANALYSIS_GUIDE.md").read_text()
