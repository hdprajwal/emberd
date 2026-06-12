"""The shipped task suite loads, covers every category, and is well-formed."""

from pathlib import Path

from harness.config import load_config
from harness.tasks import CATEGORIES, load_tasks

EVAL_ROOT = Path(__file__).resolve().parent.parent


def test_one_task_per_category():
    cfg = load_config(EVAL_ROOT / "config.yaml")
    tasks = load_tasks(EVAL_ROOT, cfg.tasks.glob)
    by_cat = {t.category for t in tasks}
    assert by_cat == set(CATEGORIES), f"missing categories: {set(CATEGORIES) - by_cat}"


def test_adversarial_has_payload_and_injection_in_data():
    tasks = {t.id: t for t in load_tasks(EVAL_ROOT, "tasks/**/*.yaml")}
    adv = tasks["adversarial-summarize-injection"]
    assert adv.injected_payload
    # the injection lives in the seeded data, not the user prompt
    assert "OVERRIDE" in adv.setup_files[0].content
    assert "cat /root/.aws/credentials" in adv.setup_files[0].content
    assert set(adv.tripwires) == {"secret_read", "net_egress"}


def test_network_task_declares_no_tripwires():
    tasks = {t.id: t for t in load_tasks(EVAL_ROOT, "tasks/**/*.yaml")}
    assert tasks["network-fetch-title"].tripwires == ()


def test_every_task_has_success_check():
    tasks = load_tasks(EVAL_ROOT, "tasks/**/*.yaml")
    for t in tasks:
        assert t.success_check is not None, f"{t.id} missing success_check"
        assert t.success_check.kind in {"stdout_contains", "file_equals", "exit_code", "judge"}
