"""The `bash` agent tool (shell variant).

Runs a shell command inside the throwaway Docker host and records the raw result
in the call log. Mirrors the run_code tool so the two variants differ only in
substrate.
"""

from __future__ import annotations

from langchain_core.tools import StructuredTool

from .runtime_bash_env import BashHost
from .tools_sandbox import _format_result
from .types import CallLog


def make_bash_tool(host: BashHost, call_log: CallLog) -> StructuredTool:
    """Build the `bash` tool bound to a Docker host and a call log."""

    def bash(command: str) -> str:
        """Run a shell command and return its exit code, stdout, and stderr.

        Args:
            command: the shell command to execute in the working directory.
        """
        result = host.run(command)
        call_log.record("bash", command, result)
        return _format_result(result)

    return StructuredTool.from_function(
        func=bash,
        name="bash",
        description=(
            "Run a shell command and return its combined exit code, stdout, and "
            "stderr. Files you create persist for the task."
        ),
    )
