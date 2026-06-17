"""
02-shell-exec: use the shell language pack to run POSIX shell commands.

The shell pack runs submitted code with /bin/sh, so you can use any
shell built-ins and utilities present in the rootfs.

Requires: emberd running on 127.0.0.1:7777 (override with EMBERD_ADDR).
"""
import json
import os
import time
import urllib.error
import urllib.request

BASE_URL = f"http://{os.getenv('EMBERD_ADDR', '127.0.0.1:7777')}"


def _req(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        f"{BASE_URL}{path}",
        data=data,
        method=method,
        headers={"Content-Type": "application/json"} if data else {},
    )
    try:
        with urllib.request.urlopen(req) as resp:
            raw = resp.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"HTTP {e.code}: {e.read().decode()}") from e


def create_sandbox(language_pack="python"):
    return _req("POST", "/sandboxes", {"language_pack": language_pack})["id"]


def exec_code(sandbox_id, code, timeout_ms=5000, _retries=6):
    for attempt in range(_retries):
        try:
            return _req("POST", f"/sandboxes/{sandbox_id}/exec",
                        {"code": code, "timeout_ms": timeout_ms})
        except RuntimeError:
            if attempt == _retries - 1:
                raise
            time.sleep(0.3)


def destroy_sandbox(sandbox_id):
    _req("DELETE", f"/sandboxes/{sandbox_id}")


COMMANDS = [
    ("kernel version",   "uname -r"),
    ("current user",     "id"),
    ("process list",     "ps aux"),
    ("disk usage",       "df -h /"),
    ("network devices",  "ip link show 2>/dev/null || echo '(no ip command)'"),
]


def main():
    print("Creating shell sandbox...")
    sb_id = create_sandbox("shell")
    print(f"  {sb_id}\n")

    try:
        for label, cmd in COMMANDS:
            result = exec_code(sb_id, cmd)
            status = "ok" if result["exit_code"] == 0 else f"exit {result['exit_code']}"
            print(f"[{label}] ({status})")
            print(result["stdout"].rstrip() or "(no output)")
            if result.get("stderr"):
                print("  stderr:", result["stderr"].rstrip())
            print()
    finally:
        destroy_sandbox(sb_id)
        print("Sandbox destroyed.")


if __name__ == "__main__":
    main()
