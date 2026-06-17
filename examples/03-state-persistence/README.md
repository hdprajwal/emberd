# 03 — state-persistence

Filesystem state persists between `exec` calls within the same sandbox.

The sandbox runs with an overlayfs (tmpfs upper layer over a read-only squashfs),
so any file written during one exec is still there for the next one.
In-memory variables do **not** persist — each exec spawns a fresh interpreter —
but writes to `/tmp` (or anywhere on the overlay) do.

This example runs three sequential execs against one sandbox:

1. Generate a dataset, write it to `/tmp/data.json`
2. Read `/tmp/data.json`, compute statistics, write `/tmp/stats.json`
3. Read `/tmp/stats.json`, print a formatted report

## Run

```bash
python main.py
```

## Expected output

```
Creating sandbox...
  sb_...

[exec 1] 18 ms
wrote 50 samples to /tmp/data.json
first five: [22.99, 18.29, 22.55, 20.37, 18.51]

[exec 2] 11 ms
stats computed and written to /tmp/stats.json

[exec 3] 10 ms
Experiment : temperature_readings
Samples    : 50
Mean       : 19.9964 °C
Std dev    : 3.1073 °C
Min / Max  : 12.32 / 26.73 °C
Median     : 20.245 °C

Sandbox destroyed.
```
