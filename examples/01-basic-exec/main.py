"""
01-basic-exec: create a sandbox, run Python code, destroy it.

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


def main():
    print("Creating sandbox...")
    sb_id = create_sandbox("python")
    print(f"  {sb_id}")

    try:
        code = """
import math, sys

values = [1, 4, 9, 16, 25, 36]
roots  = [math.sqrt(v) for v in values]
print("squares:", values)
print("roots:  ", roots)
print("sum of roots:", sum(roots))
"""
        print("\nExecuting code...")
        result = exec_code(sb_id, code)
        print(f"  exit_code : {result['exit_code']}")
        print(f"  duration  : {result['duration_ms']} ms")
        print(f"  stdout    :\n{result['stdout'].rstrip()}")
        if result.get("stderr"):
            print(f"  stderr    : {result['stderr'].rstrip()}")
    finally:
        print("\nDestroying sandbox...")
        destroy_sandbox(sb_id)
        print("  done.")


if __name__ == "__main__":
    main()
