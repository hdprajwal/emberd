# emberd examples

Self-contained examples showing how to use emberd — from the basic HTTP API to wiring it into an OpenAI agent.

Each example is standalone. Examples 01-04 need only Python's standard library. Example 05 uses `uv` (`uv run main.py`). Run emberd first:

```bash
./bin/emberd   # listens on 127.0.0.1:7777
```

| # | Example | What it shows |
|---|---------|---------------|
| 01 | [basic-exec](./01-basic-exec/) | Create → exec Python → destroy |
| 02 | [shell-exec](./02-shell-exec/) | Shell language pack |
| 03 | [state-persistence](./03-state-persistence/) | Filesystem state between exec calls in one sandbox |
| 04 | [error-handling](./04-error-handling/) | Non-zero exit codes, stderr, exec errors |
| 05 | [openai-agent](./05-openai-agent/) | GPT-4o tool-calling agent backed by emberd |
