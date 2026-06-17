"""
04-error-handling: handle non-zero exit codes, stderr, and exec errors.

emberd returns a normal 200 for any user program exit, including non-zero.
A non-zero exit_code is not an HTTP error — it is the program's exit status.
The exec response also carries an "error" field for guest-side failures
(e.g. interpreter not found), distinct from the program's own output.

This example demonstrates:
  - SyntaxError:    code that doesn't parse — Python writes to stderr, exits 1
  - RuntimeError:   code that raises an exception mid-run
  - Non-zero exit:  sys.exit(42)
  - Timeout:        code that runs longer than timeout_ms
  - stderr capture: code that writes to both stdout and stderr

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


def show(label, result):
    print(f"[{label}]")
    print(f"  exit_code : {result['exit_code']}")
    if result.get("stdout"):
        print(f"  stdout    : {result['stdout'].rstrip()}")
    if result.get("stderr"):
        print(f"  stderr    : {result['stderr'].rstrip()}")
    if result.get("error"):
        print(f"  error     : {result['error']}")
    print()


CASES = [
    ("syntax error",
     "def broken(\nprint('oops')",
     5000),

    ("runtime exception",
     "result = 1 / 0\nprint(result)",
     5000),

    ("explicit non-zero exit",
     "import sys\nprint('about to exit')\nsys.exit(42)",
     5000),

    ("mixed stdout + stderr",
     "import sys\nprint('to stdout')\nprint('to stderr', file=sys.stderr)\nprint('done')",
     5000),

    ("timeout",
     "import time\nprint('sleeping...')\ntime.sleep(60)",
     800),
]


def main():
    print("Creating sandbox...")
    sb_id = create_sandbox("python")
    print(f"  {sb_id}\n")

    try:
        for label, code, timeout_ms in CASES:
            result = exec_code(sb_id, code, timeout_ms=timeout_ms)
            show(label, result)
    finally:
        destroy_sandbox(sb_id)
        print("Sandbox destroyed.")


if __name__ == "__main__":
    main()
