"""End-to-end run_trial test against real Docker with a scripted model.

Gated behind EMBERD_EVAL_DOCKER=1. Proves the full glue: boot substrate, seed
files, run the agent graph, capture steps, write a well-formed trajectory, tear
down — without needing an API key.
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest
from langchain_core.messages import AIMessage

from harness.config import load_config
from harness.runner import run_trial
from harness.tasks import load_task
from harness.trajectory import read_trajectory
from tests.scripted_model import ScriptedModel

pytestmark = pytest.mark.skipif(
    os.environ.get("EMBERD_EVAL_DOCKER") != "1",
    reason="set EMBERD_EVAL_DOCKER=1 to run Docker integration tests",
)

EVAL_ROOT = Path(__file__).resolve().parent.parent


def test_shell_trial_end_to_end(tmp_path: Path):
    cfg = load_config(EVAL_ROOT / "config.yaml")
    task = load_task(EVAL_ROOT / "tasks/benign/wordcount.yaml")
    model = ScriptedModel(
        [
            AIMessage(
                content="I'll count whole-word, case-insensitive occurrences.",
                tool_calls=[
                    {
                        "name": "bash",
                        "args": {"command": "grep -owi ember data.txt | wc -l"},
                        "id": "c1",
                    }
                ],
            ),
            AIMessage(content="The count is 7."),
        ]
    )

    path = run_trial(cfg, task, "shell", seed=0, run_id="test", out_dir=tmp_path, model=model)
    records = read_trajectory(path)

    kinds = [r["record_type"] for r in records]
    assert kinds[0] == "header"
    assert kinds[-1] == "outcome"
    assert "step" in kinds

    header = records[0]
    assert header["meta"]["variant"] == "shell"
    assert header["task"]["setup_files"][0]["path"] == "data.txt"

    steps = [r for r in records if r["record_type"] == "step"]
    bash_steps = [s for s in steps if s["tool"] == "bash"]
    assert bash_steps, "expected a bash tool call"
    assert bash_steps[0]["tool_input"] == "grep -owi ember data.txt | wc -l"
    assert bash_steps[0]["result"]["exit_code"] == 0
    assert bash_steps[0]["result"]["stdout"].strip() == "7"

    outcome = records[-1]
    assert outcome["status"] == "success"
