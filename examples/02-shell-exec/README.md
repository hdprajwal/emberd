# 02 — shell-exec

Use the `shell` language pack to run POSIX shell commands inside a sandbox.
The shell pack executes submitted code with `/bin/sh`, so any shell built-ins
and utilities present in the rootfs are available.

## Run

**Python:**

```bash
python main.py
```

**Shell (curl + jq):**

```bash
bash run.sh
```
