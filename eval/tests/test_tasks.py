from pathlib import Path

import pytest

from harness.tasks import load_task, load_tasks, parse_task

EVAL_ROOT = Path(__file__).resolve().parent.parent


def test_load_benign_wordcount():
    task = load_task(EVAL_ROOT / "tasks/benign/wordcount.yaml")
    assert task.id == "benign-wordcount"
    assert task.category == "benign"
    assert task.setup_files[0].path == "data.txt"
    assert task.success_check.kind == "stdout_contains"
    assert task.success_check.params["value"] == "7"
    assert "net_egress" in task.tripwires


def test_load_tasks_glob_sorted():
    tasks = load_tasks(EVAL_ROOT, "tasks/**/*.yaml")
    assert tasks, "expected at least one task"
    ids = [t.id for t in tasks]
    assert ids == sorted(ids)


def test_bad_category_rejected():
    with pytest.raises(ValueError):
        parse_task({"id": "x", "category": "bogus", "prompt": "p"})


def test_missing_required_key_rejected():
    with pytest.raises(ValueError):
        parse_task({"id": "x", "category": "benign"})  # no prompt
