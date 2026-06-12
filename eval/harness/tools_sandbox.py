"""emberd HTTP client + the `run_code` agent tool (sandbox variant).

One sandbox is created per task and destroyed when the trial ends (spec §3, §6).
The sandbox uses the `shell` language pack so `run_code` runs shell commands —
the same capability surface as the bash variant, leaving the execution substrate
(emberd microVM vs Docker) as the only variable.
"""

from __future__ import annotations

from typing import Any

import httpx
from langchain_core.tools import StructuredTool

from .types import CallLog, ExecResult

# Cap tool output handed back to the model so one runaway command can't blow the
# context window. The raw (untruncated) result still goes to the trajectory.
MAX_TOOL_OUTPUT_CHARS = 8000


class EmberdError(RuntimeError):
    """A substrate-side failure talking to the emberd daemon."""


class EmberdClient:
    """Thin sync client over the emberd HTTP contract."""

    def __init__(
        self,
        base_url: str,
        exec_timeout_ms: int = 30000,
        transport: httpx.BaseTransport | None = None,
    ):
        self.base_url = base_url.rstrip("/")
        self.exec_timeout_ms = exec_timeout_ms
        # HTTP timeout is the exec budget plus headroom for daemon round-trip.
        http_timeout = exec_timeout_ms / 1000 + 15
        # `transport` lets tests inject a fake daemon (httpx.MockTransport).
        self._http = httpx.Client(
            base_url=self.base_url, timeout=http_timeout, transport=transport
        )

    def create_sandbox(self, language_pack: str = "shell") -> str:
        resp = self._http.post("/sandboxes", json={"language_pack": language_pack})
        if resp.status_code != 201:
            raise EmberdError(
                f"create sandbox failed: {resp.status_code} {resp.text}"
            )
        return resp.json()["id"]

    def exec(self, sandbox_id: str, code: str, stdin: str = "") -> ExecResult:
        body: dict[str, Any] = {
            "code": code,
            "stdin": stdin,
            "timeout_ms": self.exec_timeout_ms,
        }
        resp = self._http.post(f"/sandboxes/{sandbox_id}/exec", json=body)
        if resp.status_code != 200:
            raise EmberdError(f"exec failed: {resp.status_code} {resp.text}")
        data = resp.json()
        return ExecResult(
            stdout=data.get("stdout", ""),
            stderr=data.get("stderr", ""),
            exit_code=int(data.get("exit_code", 0)),
            duration_ms=int(data.get("duration_ms", 0)),
            error=data.get("error") or None,
        )

    def delete_sandbox(self, sandbox_id: str) -> None:
        resp = self._http.delete(f"/sandboxes/{sandbox_id}")
        # 204 expected; 404 (already gone) is benign on teardown.
        if resp.status_code not in (204, 404):
            raise EmberdError(f"delete failed: {resp.status_code} {resp.text}")

    def close(self) -> None:
        self._http.close()


class SandboxSession:
    """Owns one emberd sandbox for the lifetime of a single trial."""

    def __init__(self, client: EmberdClient, language_pack: str = "shell"):
        self.client = client
        self.language_pack = language_pack
        self.sandbox_id: str | None = None

    def __enter__(self) -> "SandboxSession":
        self.sandbox_id = self.client.create_sandbox(self.language_pack)
        return self

    def __exit__(self, *exc: object) -> None:
        if self.sandbox_id is not None:
            try:
                self.client.delete_sandbox(self.sandbox_id)
            finally:
                self.sandbox_id = None

    def run(self, code: str) -> ExecResult:
        if self.sandbox_id is None:
            raise EmberdError("sandbox session not started")
        try:
            return self.client.exec(self.sandbox_id, code)
        except (httpx.HTTPError, EmberdError) as e:
            # Surface substrate failure as an ExecResult so the trial can record
            # it as `errored` rather than crashing the whole run.
            return ExecResult(
                stdout="", stderr="", exit_code=-1, duration_ms=0, error=str(e)
            )


def _format_result(result: ExecResult) -> str:
    """Render an ExecResult as the string the model sees."""
    if result.error:
        return f"[substrate error] {result.error}"
    out = result.stdout
    err = result.stderr
    body = f"exit_code: {result.exit_code}\n--- stdout ---\n{out}"
    if err:
        body += f"\n--- stderr ---\n{err}"
    if len(body) > MAX_TOOL_OUTPUT_CHARS:
        body = body[:MAX_TOOL_OUTPUT_CHARS] + "\n…[output truncated]"
    return body


def make_run_code_tool(session: SandboxSession, call_log: CallLog) -> StructuredTool:
    """Build the `run_code` tool bound to a sandbox session and a call log."""

    def run_code(code: str) -> str:
        """Run shell code inside the isolated emberd sandbox and return its output.

        Args:
            code: shell commands to execute in the sandbox working directory.
        """
        result = session.run(code)
        call_log.record("run_code", code, result)
        return _format_result(result)

    return StructuredTool.from_function(
        func=run_code,
        name="run_code",
        description=(
            "Run shell code inside an isolated sandbox and return its combined "
            "exit code, stdout, and stderr. Files you create persist for the task."
        ),
    )
