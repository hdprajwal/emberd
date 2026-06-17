# 01 — basic-exec

The smallest possible emberd program: create a sandbox, execute Python code, print the result, destroy the sandbox.

## Run

**Python:**

```bash
python main.py
```

**Shell (curl + jq):**

```bash
bash run.sh
```

## Expected output

```
Creating sandbox...
  sb_a1b2c3...

Executing code...
  exit_code : 0
  duration  : 14 ms
  stdout    :
  squares: [1, 4, 9, 16, 25, 36]
  roots:   [1.0, 2.0, 3.0, 4.0, 5.0, 6.0]
  sum of roots: 21.0

Destroying sandbox...
  done.
```
