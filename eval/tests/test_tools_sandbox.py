"""Unit tests for the emberd client + run_code tool against a fake daemon."""

from __future__ import annotations

import json

import httpx

from harness.tools_sandbox import (
    EmberdClient,
    SandboxSession,
    make_run_code_tool,
)
from harness.types import CallLog


def fake_daemon() -> httpx.MockTransport:
    """A minimal in-memory emberd implementing the three-endpoint contract."""
    state = {"created": [], "deleted": []}

    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if request.method == "POST" and path == "/sandboxes":
            state["created"].append(json.loads(request.content))
            return httpx.Response(201, json={"id": "sb_test01"})
        if request.method == "POST" and path.endswith("/exec"):
            body = json.loads(request.content)
            # Echo the code length so we can assert the call went through.
            return httpx.Response(
                200,
                json={
                    "stdout": f"ran:{body['code']}",
                    "stderr": "",
                    "exit_code": 0,
                    "duration_ms": 5,
                },
            )
        if request.method == "DELETE" and path.startswith("/sandboxes/"):
            state["deleted"].append(path.rsplit("/", 1)[-1])
            return httpx.Response(204)
        return httpx.Response(404, text="not found")

    transport = httpx.MockTransport(handler)
    transport.state = state  # type: ignore[attr-defined]
    return transport


def make_client() -> EmberdClient:
    return EmberdClient("http://daemon", transport=fake_daemon())


def test_create_exec_delete_lifecycle():
    client = make_client()
    sid = client.create_sandbox("shell")
    assert sid == "sb_test01"
    res = client.exec(sid, "echo hi")
    assert res.ok
    assert res.stdout == "ran:echo hi"
    assert res.exit_code == 0
    client.delete_sandbox(sid)
    assert client._http._transport.state["deleted"] == ["sb_test01"]  # type: ignore[attr-defined]


def test_session_context_manager_creates_and_destroys():
    client = make_client()
    state = client._http._transport.state  # type: ignore[attr-defined]
    with SandboxSession(client) as sess:
        assert sess.sandbox_id == "sb_test01"
        sess.run("ls")
    assert state["deleted"] == ["sb_test01"]
    assert state["created"][0]["language_pack"] == "shell"


def test_run_code_tool_records_calls():
    client = make_client()
    log = CallLog()
    with SandboxSession(client) as sess:
        tool = make_run_code_tool(sess, log)
        out = tool.invoke({"code": "echo hello"})
    assert "ran:echo hello" in out
    assert len(log) == 1
    assert log.calls[0].tool == "run_code"
    assert log.calls[0].argument == "echo hello"


def test_exec_failure_surfaces_as_error_result():
    # A daemon that 500s on exec -> session.run returns an ExecResult with error set.
    def handler(request: httpx.Request) -> httpx.Response:
        if request.method == "POST" and request.url.path == "/sandboxes":
            return httpx.Response(201, json={"id": "sb_x"})
        if request.url.path.endswith("/exec"):
            return httpx.Response(500, text="boom")
        return httpx.Response(204)

    client = EmberdClient("http://daemon", transport=httpx.MockTransport(handler))
    with SandboxSession(client) as sess:
        res = sess.run("whatever")
    assert not res.ok
    assert res.error
