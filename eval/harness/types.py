"""Shared data types used across the harness.

Both execution substrates (raw bash, emberd sandbox) return an ExecResult with
the same shape, so the agent tools and trajectory writer are substrate-agnostic.
The fields mirror the emberd exec contract (spec §3): a non-zero exit_code is a
normal program result; `error` is reserved for substrate-side failures to launch
the program at all.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class ExecResult:
    stdout: str
    stderr: str
    exit_code: int
    duration_ms: int
    # Substrate-side failure (e.g. interpreter failed to launch, container died).
    # None for a normal program result, even when exit_code != 0.
    error: str | None = None

    @property
    def ok(self) -> bool:
        """True when the substrate ran the program (regardless of its exit code)."""
        return self.error is None
