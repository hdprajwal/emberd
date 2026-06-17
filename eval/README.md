# emberd eval harness

A demonstration that **emberd** lets an AI agent (a) keep capability parity with
raw shell and (b) contain harmful, destructive, or injection-induced actions that
raw shell would let through to the host. The model is held constant; only the
execution substrate differs — raw `bash` (in a disposable Docker host) vs an
emberd `run_code` sandbox (microVM). See `../docs/emberd-eval-spec.md` for the
full design.

> This is a demonstration of emberd, not a neutral third-party benchmark. The
> protection result is partly true by construction (v0.1 sandboxes have no
> network device and a disposable overlay). The report restates this caveat.

## Layout

```
harness/   the harness (config, agent, tools, runtime, tripwires, scorers, runner, report)
tasks/     one task per category: benign / destructive / adversarial / network
results/   per-run output: trajectories/*.jsonl, summary.md, scoreboard.csv, ANALYSIS_GUIDE.md
tests/     unit tests (no model/VM) + Docker-gated integration tests
config.yaml model id, emberd addr, task glob, trial counts, budgets
```

## Prerequisites

- A Claude API key in the environment (`ANTHROPIC_API_KEY`) for a real run.
- Docker, for the throwaway shell host and the network sink.
- A running emberd daemon (KVM + Firecracker) for the sandbox variant. Without
  it, sandbox trials are recorded as `errored` and the run continues.

## Run

```bash
uv sync                                  # install deps
uv run python -m harness.main            # full matrix → results/<run-id>/
uv run python -m harness.main --variants shell        # shell only (no daemon)
uv run python -m harness.main --no-llm-scoring        # static danger + deterministic grading only

# single trial, smoke:
uv run python -m harness.runner --task tasks/benign/wordcount.yaml --variant shell
```

Scale by editing `config.yaml` (`tasks.trials`, `tasks.glob`, budgets, model id).

## Tests

```bash
uv run pytest                            # unit tests (fast, no Docker/model)
EMBERD_EVAL_DOCKER=1 uv run pytest       # + Docker integration tests
```

## How it works

For each `task × {shell, sandbox} × trial` (data flow, spec §6): provision the
substrate, seed the task files, snapshot the tripwire baseline (canaries +
honeytokens + processes), run a LangGraph agent to completion or budget, capture
every step, diff host effects into a violation list, grade utility (deterministic
check or LLM judge), classify each tool call's danger (static rules + LLM judge),
write a JSONL trajectory, and tear the substrate down. `report.py` aggregates the
trial results into `summary.md` + `scoreboard.csv`.

The shell variant runs inside a disposable Docker container with **no bind-mounts
to the real filesystem**; egress is routed to an internal-network sink (no real
internet) that logs connection attempts. Dangerous commands therefore only ever
touch the throwaway host or the emberd sandbox — never the real machine.
