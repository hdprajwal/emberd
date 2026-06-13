"""Unit tests for the results store: fingerprinting, caching, persistence."""

from pathlib import Path

from harness.config import ModelConfig
from harness.results import TrialResult, result_from_dict, result_to_dict
from harness.store import ResultStore, fingerprint, trial_key
from harness.tasks import SuccessCheck, Task


def _task(prompt="do it"):
    return Task(id="t1", category="benign", prompt=prompt,
                success_check=SuccessCheck("stdout_contains", {"value": "7"}))


def _model(mid="anthropic:claude-opus-4-8"):
    return ModelConfig(id=mid, temperature=None, max_tokens=4096)


def _result(key="t1__shell__seed0"):
    return TrialResult(
        task_id="t1", category="benign", variant="shell", seed=0, pair_id="t1/0",
        status="success", utility_verdict="success", containment="contained",
        violations_total=0, violations_by_type={}, prompt_tokens=10,
        completion_tokens=2, total_tokens=12, latency_ms=100,
        trajectory_path=Path(f"trajectories/{key}.jsonl"),
    )


def test_trial_key_format():
    assert trial_key("t1", "shell", 0) == "t1__shell__seed0"


def test_fingerprint_changes_with_task_prompt():
    fp1 = fingerprint(_task("a"), "shell", 0, _model())
    fp2 = fingerprint(_task("b"), "shell", 0, _model())
    assert fp1 != fp2


def test_fingerprint_changes_with_model_and_variant_and_seed():
    base = fingerprint(_task(), "shell", 0, _model())
    assert base != fingerprint(_task(), "sandbox", 0, _model())
    assert base != fingerprint(_task(), "shell", 1, _model())
    assert base != fingerprint(_task(), "shell", 0, _model("anthropic:claude-sonnet-4-6"))


def test_fingerprint_stable_for_same_inputs():
    assert fingerprint(_task(), "shell", 0, _model()) == fingerprint(_task(), "shell", 0, _model())


def test_result_roundtrip():
    r = _result()
    back = result_from_dict(result_to_dict(r))
    assert back == r
    assert isinstance(back.trajectory_path, Path)


def test_store_caches_on_matching_fingerprint(tmp_path: Path):
    store = ResultStore(tmp_path / "store")
    index = store.load()
    assert index == {}
    key, fp = "t1__shell__seed0", "fp-abc"
    store.put(index, key, fp, _result())
    store.save(index)

    reloaded = store.load()
    assert store.cached(reloaded, key, fp) is not None       # fingerprint matches
    assert store.cached(reloaded, key, "different") is None   # fingerprint changed
    assert store.cached(reloaded, "other-key", fp) is None    # absent


def test_all_results_sorted(tmp_path: Path):
    store = ResultStore(tmp_path / "store")
    index = {}
    store.put(index, "b__shell__seed0", "fp", _result("b__shell__seed0"))
    store.put(index, "a__shell__seed0", "fp", _result("a__shell__seed0"))
    out = store.all_results(index)
    assert [r.task_id for r in out] == ["t1", "t1"]  # both have task_id t1 here
    assert len(out) == 2
