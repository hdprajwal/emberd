"""Throwaway Docker host for the shell variant.

Phase 1 provides the minimal lifecycle: boot a disposable container, seed the
workspace, run commands via `docker exec`, and tear it down. Phase 2 enriches
this same class with canaries/honeytokens, a network sink + connection logger,
the tripwire baseline, and resource caps.

Safety invariants (spec §9), already enforced here so they never regress:
  * no bind-mounts to the real filesystem,
  * container removed in __exit__ even on crash,
so dangerous commands and seeded honeytokens never touch the real machine.
"""

from __future__ import annotations

import subprocess
import time

from .types import ExecResult

WORKDIR = "/workspace"


class DockerError(RuntimeError):
    pass


def _docker(*args: str, input: str | None = None, timeout: float | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["docker", *args],
        input=input,
        capture_output=True,
        text=True,
        timeout=timeout,
    )


class BashHost:
    """A disposable Docker container the shell variant executes inside."""

    def __init__(self, image: str, command_timeout_s: int = 30):
        self.image = image
        self.command_timeout_s = command_timeout_s
        self.container_id: str | None = None

    def __enter__(self) -> "BashHost":
        # No host network and no bind-mounts (Phase 2 adds the instrumented sink).
        # `sleep infinity` keeps the container alive for repeated docker exec.
        proc = _docker(
            "run", "-d", "--network", "none",
            "-w", WORKDIR,
            self.image,
            "sleep", "infinity",
            timeout=120,
        )
        if proc.returncode != 0:
            raise DockerError(f"docker run failed: {proc.stderr.strip()}")
        self.container_id = proc.stdout.strip()
        mk = self._exec_raw(f"mkdir -p {WORKDIR}")
        if mk.exit_code != 0:
            raise DockerError(f"could not create workdir: {mk.stderr}")
        return self

    def __exit__(self, *exc: object) -> None:
        if self.container_id is not None:
            _docker("rm", "-f", self.container_id, timeout=60)
            self.container_id = None

    def seed_file(self, path: str, content: str) -> None:
        """Write a file into the workspace before the run."""
        if self.container_id is None:
            raise DockerError("host not started")
        # Resolve relative paths against the workspace; create parent dirs.
        target = path if path.startswith("/") else f"{WORKDIR}/{path}"
        parent = target.rsplit("/", 1)[0] or "/"
        proc = _docker(
            "exec", "-i", self.container_id,
            "/bin/sh", "-c", f"mkdir -p {parent} && cat > {target}",
            input=content,
            timeout=60,
        )
        if proc.returncode != 0:
            raise DockerError(f"seed_file {path} failed: {proc.stderr.strip()}")

    def _exec_raw(self, command: str) -> ExecResult:
        if self.container_id is None:
            raise DockerError("host not started")
        start = time.monotonic()
        try:
            proc = _docker(
                "exec", "-w", WORKDIR, self.container_id,
                "/bin/sh", "-c", command,
                timeout=self.command_timeout_s,
            )
        except subprocess.TimeoutExpired:
            dur = int((time.monotonic() - start) * 1000)
            return ExecResult("", "", -1, dur, error=f"timeout after {self.command_timeout_s}s")
        dur = int((time.monotonic() - start) * 1000)
        return ExecResult(
            stdout=proc.stdout,
            stderr=proc.stderr,
            exit_code=proc.returncode,
            duration_ms=dur,
        )

    def run(self, command: str) -> ExecResult:
        """Run a shell command inside the host workspace."""
        return self._exec_raw(command)
