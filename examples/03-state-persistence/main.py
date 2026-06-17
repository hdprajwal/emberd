"""
03-state-persistence: filesystem state persists between exec calls in the same sandbox.

The sandbox runs with an overlayfs (tmpfs upper layer over a read-only squashfs),
so any files written during one exec are still there for the next exec.
In-memory variables do not persist — each exec spawns a fresh interpreter —
but /tmp writes do.

This example:
  exec 1 — generate a dataset, write it to /tmp/data.json
  exec 2 — read /tmp/data.json, compute statistics, write /tmp/stats.json
  exec 3 — read /tmp/stats.json, print a formatted report

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


STEP_1 = """
import json, random

random.seed(42)
data = {
    "experiment": "temperature_readings",
    "unit": "celsius",
    "samples": [round(20 + random.gauss(0, 3), 2) for _ in range(50)],
}
with open("/tmp/data.json", "w") as f:
    json.dump(data, f)

print(f"wrote {len(data['samples'])} samples to /tmp/data.json")
print(f"first five: {data['samples'][:5]}")
"""

STEP_2 = """
import json, math, statistics

with open("/tmp/data.json") as f:
    data = json.load(f)

samples = data["samples"]
stats = {
    "experiment": data["experiment"],
    "n": len(samples),
    "mean": round(statistics.mean(samples), 4),
    "stdev": round(statistics.stdev(samples), 4),
    "min": min(samples),
    "max": max(samples),
    "median": round(statistics.median(samples), 4),
}
with open("/tmp/stats.json", "w") as f:
    json.dump(stats, f)

print("stats computed and written to /tmp/stats.json")
"""

STEP_3 = """
import json

with open("/tmp/stats.json") as f:
    s = json.load(f)

print(f"Experiment : {s['experiment']}")
print(f"Samples    : {s['n']}")
print(f"Mean       : {s['mean']} °C")
print(f"Std dev    : {s['stdev']} °C")
print(f"Min / Max  : {s['min']} / {s['max']} °C")
print(f"Median     : {s['median']} °C")
"""


def run_step(sb_id, step_num, code):
    print(f"[exec {step_num}] ", end="", flush=True)
    result = exec_code(sb_id, code)
    if result["exit_code"] != 0:
        raise RuntimeError(f"step {step_num} failed (exit {result['exit_code']}):\n{result['stderr']}")
    print(f"{result['duration_ms']} ms")
    print(result["stdout"].rstrip())
    print()


def main():
    print("Creating sandbox...")
    sb_id = create_sandbox("python")
    print(f"  {sb_id}\n")

    try:
        run_step(sb_id, 1, STEP_1)
        run_step(sb_id, 2, STEP_2)
        run_step(sb_id, 3, STEP_3)
    finally:
        destroy_sandbox(sb_id)
        print("Sandbox destroyed.")


if __name__ == "__main__":
    main()
