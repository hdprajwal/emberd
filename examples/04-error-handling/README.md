# 04 — error-handling

How to handle the different failure modes emberd surfaces:

| Scenario | What you see |
|---|---|
| Syntax error | `exit_code: 1`, Python traceback in `stderr` |
| Runtime exception | `exit_code: 1`, exception + traceback in `stderr` |
| `sys.exit(N)` | `exit_code: N`, stdout still captured |
| Mixed stdout + stderr | Both fields populated in the response |
| Timeout | `exit_code` non-zero, `error` field set by the guest |

A non-zero `exit_code` is **not** an HTTP error — emberd always returns 200
for a completed exec. The `error` field on the response (distinct from `stderr`)
signals a guest-side failure (e.g. interpreter not found, timeout kill).

## Run

```bash
python main.py
```
