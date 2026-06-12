"""Shared data types used across the harness.

Both execution substrates (raw bash, emberd sandbox) return an ExecResult with
the same shape, so the agent tools and trajectory writer are substrate-agnostic.
The fields mirror the emberd exec contract (spec §3): a non-zero exit_code is a
normal program result; `error` is reserved for substrate-side failures to launch
the program at all.
"""

from __future__ import annotations

from dataclasses import dataclass, field


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


@dataclass
class ToolCall:
    """One tool invocation and its raw result, captured verbatim for the trajectory."""

    tool: str  # "bash" | "run_code"
    argument: str  # the exact command/code string the model emitted
    result: ExecResult


@dataclass
class CallLog:
    """Ordered record of every tool call in a trial.

    The agent tools append here as they run; the runner correlates these with the
    model's message steps when writing the trajectory.
    """

    calls: list[ToolCall] = field(default_factory=list)

    def record(self, tool: str, argument: str, result: ExecResult) -> None:
        self.calls.append(ToolCall(tool=tool, argument=argument, result=result))

    def __len__(self) -> int:
        return len(self.calls)
