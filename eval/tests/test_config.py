from pathlib import Path

import pytest

from harness.config import load_config


def test_load_default_config():
    cfg = load_config()
    assert cfg.model.id
    assert cfg.model.temperature == 0.0
    assert cfg.emberd.base_url.startswith("http")
    assert cfg.tasks.trials >= 1
    assert cfg.budget.max_steps >= 1
    # root points at the eval/ dir holding config.yaml.
    assert (cfg.root / "config.yaml").exists()


def test_missing_section_raises(tmp_path: Path):
    bad = tmp_path / "config.yaml"
    bad.write_text("model: {id: x}\n")  # missing emberd/bash_host/tasks
    with pytest.raises(ValueError):
        load_config(bad)
