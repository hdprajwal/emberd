"""Disposable network sink for the shell variant.

Creates an *internal* Docker network (no route to the real internet) and runs the
sink server (sink_server.py) on it. The shell host joins this network with the
sink as its DNS, so every egress attempt resolves and connects to the sink and
is logged — observable, but contained. Mounting our own harness file into the
sink (trusted infra) is fine; the safety rule is that the *agent host* never
bind-mounts the real filesystem.
"""

from __future__ import annotations

import json
import time
from pathlib import Path

from .runtime_bash_env import DockerError, _docker

SINK_SCRIPT = Path(__file__).resolve().parent / "sink_server.py"


class NetworkSink:
    def __init__(
        self,
        image: str,
        network_name: str = "emberd-eval-net",
        sink_name: str = "emberd-eval-sink",
    ):
        self.image = image
        self.network_name = network_name
        self.sink_name = sink_name
        self.sink_id: str | None = None
        self.ip: str | None = None

    def __enter__(self) -> "NetworkSink":
        # Internal network: containers can talk to each other but not the internet.
        # Tolerate a pre-existing network from a crashed run.
        _docker("network", "rm", self.network_name, timeout=30)
        create = _docker("network", "create", "--internal", self.network_name, timeout=30)
        if create.returncode != 0 and "already exists" not in create.stderr:
            raise DockerError(f"network create failed: {create.stderr.strip()}")

        _docker("rm", "-f", self.sink_name, timeout=30)  # clear any stale sink
        run = _docker(
            "run", "-d", "--name", self.sink_name,
            "--network", self.network_name,
            "-v", f"{SINK_SCRIPT}:/sink_server.py:ro",
            self.image,
            "python", "/sink_server.py",
            timeout=120,
        )
        if run.returncode != 0:
            raise DockerError(f"sink run failed: {run.stderr.strip()}")
        self.sink_id = run.stdout.strip()
        self.ip = self._sink_ip()
        self._wait_ready()
        return self

    def __exit__(self, *exc: object) -> None:
        if self.sink_id is not None:
            _docker("rm", "-f", self.sink_id, timeout=60)
            self.sink_id = None
        _docker("network", "rm", self.network_name, timeout=30)

    def _sink_ip(self) -> str:
        insp = _docker("inspect", self.sink_name, timeout=30)
        if insp.returncode != 0:
            raise DockerError(f"inspect sink failed: {insp.stderr.strip()}")
        data = json.loads(insp.stdout)
        nets = data[0]["NetworkSettings"]["Networks"]
        return nets[self.network_name]["IPAddress"]

    def _wait_ready(self, timeout_s: float = 10.0) -> None:
        # Ready once the sink has written its startup line.
        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            out = _docker("exec", self.sink_id, "cat", "/var/log/sink.log", timeout=10)
            if out.returncode == 0 and "# sink up" in out.stdout:
                return
            time.sleep(0.2)
        raise DockerError("sink did not become ready")

    def reset_log(self) -> None:
        """Clear the connection log between trials."""
        if self.sink_id is None:
            raise DockerError("sink not started")
        _docker("exec", self.sink_id, "/bin/sh", "-c", ": > /var/log/sink.log", timeout=10)

    def connections(self) -> list[str]:
        """Connection records logged since the last reset (excludes the startup line)."""
        if self.sink_id is None:
            return []
        out = _docker("exec", self.sink_id, "cat", "/var/log/sink.log", timeout=10)
        if out.returncode != 0:
            return []
        return [
            line for line in out.stdout.splitlines()
            if line.strip() and not line.startswith("#")
        ]
