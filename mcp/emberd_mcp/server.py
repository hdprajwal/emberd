#!/usr/bin/env python3
"""MCP server that exposes emberd Firecracker microVM sandboxes as agent tools."""

import os

import httpx
from mcp.server.fastmcp import FastMCP

EMBERD_URL = os.environ.get("EMBERD_URL", "http://127.0.0.1:7777")

mcp = FastMCP(
    "emberd",
    instructions=(
        "You have access to isolated Firecracker microVM sandboxes via emberd. "
        "Workflow: create a sandbox → exec code in it (multiple times if needed) → delete it. "
        "Sandboxes are stateful: variables, files, and installed packages survive between exec calls. "
        "Always delete sandboxes when finished — they are not garbage-collected automatically. "
        "Available language packs: 'python' (Python 3) and 'shell' (/bin/sh)."
    ),
)


def _http() -> httpx.Client:
    # 70s timeout: covers the max exec wall-clock (timeout_ms + 10s grace) plus headroom
    return httpx.Client(base_url=EMBERD_URL, timeout=70.0)


@mcp.tool()
def create_sandbox(language_pack: str = "python") -> str:
    """Create an isolated Firecracker microVM sandbox.

    Args:
        language_pack: Runtime for the sandbox. "python" runs Python 3; "shell" runs /bin/sh.
                       Defaults to "python".

    Returns:
        Sandbox ID string (e.g. "sb_a1b2c3d4e5f6"). Pass this to exec_code and delete_sandbox.

    Raises:
        ValueError: If language_pack is unknown or emberd returns an error.
    """
    with _http() as c:
        resp = c.post("/sandboxes", json={"language_pack": language_pack})
    if resp.status_code == 400:
        raise ValueError(resp.json().get("error", "bad request"))
    resp.raise_for_status()
    return resp.json()["id"]


@mcp.tool()
def exec_code(
    sandbox_id: str,
    code: str,
    stdin: str = "",
    timeout_ms: int = 5000,
) -> dict:
    """Execute code inside a sandbox.

    The sandbox is stateful — imports, variable assignments, file writes, and pip installs
    from previous exec_code calls on the same sandbox are still present.

    Args:
        sandbox_id: ID returned by create_sandbox.
        code:       Source code to run. Python or shell depending on the sandbox's language_pack.
        stdin:      Text to pipe to the program's standard input. Empty string means no stdin.
        timeout_ms: Execution wall-clock timeout in milliseconds. Default 5000. Use 0 for no limit.

    Returns:
        dict with:
          stdout (str)      — program's standard output
          stderr (str)      — program's standard error
          exit_code (int)   — 0 on success, non-zero on failure, -1 on timeout or crash
          duration_ms (int) — actual wall-clock execution time in milliseconds
          error (str)       — non-empty only if the guest agent itself failed (not user code errors)

    Raises:
        ValueError: If sandbox_id does not exist or the request is malformed.
    """
    payload: dict = {"code": code, "timeout_ms": timeout_ms}
    if stdin:
        payload["stdin"] = stdin
    with _http() as c:
        resp = c.post(f"/sandboxes/{sandbox_id}/exec", json=payload)
    if resp.status_code == 404:
        raise ValueError(f"sandbox {sandbox_id!r} not found")
    if resp.status_code == 400:
        raise ValueError(resp.json().get("error", "bad request"))
    resp.raise_for_status()
    return resp.json()


@mcp.tool()
def delete_sandbox(sandbox_id: str) -> str:
    """Destroy a sandbox and release its microVM resources.

    Call this when you're done with a sandbox. Skipping it leaks a running Firecracker VM.

    Args:
        sandbox_id: ID returned by create_sandbox.

    Returns:
        Confirmation string.

    Raises:
        ValueError: If sandbox_id does not exist.
    """
    with _http() as c:
        resp = c.delete(f"/sandboxes/{sandbox_id}")
    if resp.status_code == 404:
        raise ValueError(f"sandbox {sandbox_id!r} not found")
    resp.raise_for_status()
    return f"deleted {sandbox_id}"


def main() -> None:
    mcp.run()


if __name__ == "__main__":
    main()
