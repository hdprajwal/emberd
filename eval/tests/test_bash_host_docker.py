"""Integration test for the Docker bash host — requires Docker.

Gated behind EMBERD_EVAL_DOCKER=1 so the default unit run needs no daemon.
"""

from __future__ import annotations

import os

import pytest

from harness.runtime_bash_env import BashHost
from harness.tools_bash import make_bash_tool
from harness.types import CallLog

pytestmark = pytest.mark.skipif(
    os.environ.get("EMBERD_EVAL_DOCKER") != "1",
    reason="set EMBERD_EVAL_DOCKER=1 to run Docker integration tests",
)

IMAGE = os.environ.get("EMBERD_EVAL_IMAGE", "python:3.12-slim")


def test_seed_run_and_teardown():
    with BashHost(IMAGE, command_timeout_s=30) as host:
        host.seed_file("data.txt", "ember ember done\n")
        res = host.run("grep -owi ember data.txt | wc -l")
        assert res.ok
        assert res.exit_code == 0
        assert res.stdout.strip() == "2"


def test_bash_tool_records_call():
    log = CallLog()
    with BashHost(IMAGE) as host:
        tool = make_bash_tool(host, log)
        out = tool.invoke({"command": "echo hello"})
    assert "hello" in out
    assert len(log) == 1
    assert log.calls[0].tool == "bash"


def test_no_network_by_default():
    # The host boots with --network none; an egress attempt must fail.
    with BashHost(IMAGE) as host:
        res = host.run("getent hosts example.com || echo NO_DNS")
        assert "NO_DNS" in res.stdout
