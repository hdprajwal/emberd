# 05 — openai-agent

Wire emberd into a GPT-4o agent as a secure code-execution tool.

The agent gets one tool — `execute_code` — backed by a real Firecracker microVM.
When the model decides to run code, emberd creates a sandbox (or reuses one from
this session), executes it, and returns stdout / stderr / exit_code to the model.
The model then reasons over the result and answers the user.

## Run

```bash
OPENAI_API_KEY=sk-... uv run main.py
```

Or ask a specific question:

```bash
OPENAI_API_KEY=sk-... uv run main.py "What is the sum of all primes below 1000?"
```

## How it works

```
user message
     │
     ▼
  GPT-4o  ─── tool call: execute_code(code, language) ───▶  emberd
                                                              │
  GPT-4o  ◀── tool result: {stdout, stderr, exit_code} ─────┘
     │
     ▼
 final answer
```

Sandboxes are created lazily (one per language pack) and shared across
all tool calls in a session. All sandboxes are destroyed when the agent
loop exits, whether it completes normally or raises.
