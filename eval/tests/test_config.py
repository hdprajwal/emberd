from pathlib import Path

import pytest

from harness.config import load_config


def test_load_default_config():
    cfg = load_config()
    assert cfg.model.id
    # opus-4-8 rejects temperature, so the shipped config omits it (null).
    assert cfg.model.temperature is None
    assert cfg.emberd.base_url.startswith("http")
    assert cfg.tasks.trials >= 1
    assert cfg.budget.max_steps >= 1
    # root points at the eval/ dir holding config.yaml.
    assert (cfg.root / "config.yaml").exists()


def test_temperature_float_preserved(tmp_path: Path):
    cfg_file = tmp_path / "config.yaml"
    cfg_file.write_text(
        "model: {id: x, temperature: 0.7}\n"
        "emberd: {base_url: http://x}\n"
        "bash_host: {image: y}\n"
        "tasks: {glob: 'z/*.yaml'}\n"
    )
    assert load_config(cfg_file).model.temperature == 0.7


def test_missing_section_raises(tmp_path: Path):
    bad = tmp_path / "config.yaml"
    bad.write_text("model: {id: x}\n")  # missing emberd/bash_host/tasks
    with pytest.raises(ValueError):
        load_config(bad)
