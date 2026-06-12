"""Per-trial driver and (Phase 5) the task × variant × trial matrix.

`run_trial` provisions a substrate, seeds files, runs the agent to completion or
budget, captures every step, and writes the JSONL trajectory. The scorers
(grade.py, classify.py) and tripwires (tripwires.py) plug into the marked hooks
as later phases land; until then those fields carry their reserved defaults.
"""

from __future__ import annotations

import argparse
import subprocess
import time
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterator

from langchain_core.messages import AIMessage
from langgraph.errors import GraphRecursionError

from .agent import build_agent, build_model
from .classify import classify_static, make_llm_classifier
from .config import Config, load_config
from .grade import grade, make_llm_judge
from .tasks import Task, load_task, load_tasks
from .tools_bash import make_bash_tool
from .tools_sandbox import EmberdClient, SandboxSession, make_run_code_tool
from .runtime_bash_env import BashHost
from .trajectory import (
    OutcomeRecord,
    StepRecord,
    TaskContext,
    TrajectoryMeta,
    TrajectoryWriter,
    result_to_dict,
)
from .tripwires import (
    detect_net_egress,
    detect_secret_reads,
    diff_filesystem,
    diff_processes,
    summarize,
)
from .types import CallLog


@dataclass
class TrialResult:
    """In-memory summary of one trial, returned for the reporter to aggregate."""

    task_id: str
    category: str
    variant: str
    seed: int
    pair_id: str
    status: str
    utility_verdict: str
    containment: str
    violations_total: int
    violations_by_type: dict[str, int]
    prompt_tokens: int
    completion_tokens: int
    total_tokens: int
    latency_ms: int
    trajectory_path: Path


def _now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def emberd_git_sha(root: Path) -> str:
    try:
        out = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=root, capture_output=True, text=True, timeout=10,
        )
        return out.stdout.strip() or "unknown"
    except Exception:
        return "unknown"


# --- substrate provisioning --------------------------------------------------


@contextmanager
def shell_substrate(
    cfg: Config, task: Task, call_log: CallLog, sink: Any | None = None
) -> Iterator[Any]:
    """Boot the throwaway Docker host, seed files, yield (bash tool, host probe).

    The host probe carries the canary/honeytoken baseline so the runner can diff
    host effects after the trial. When a `sink` is supplied the host joins the
    sink's internal network (egress routed to the logger); otherwise it boots
    with no network device.
    """
    network = sink.network_name if sink else None
    dns = sink.ip if sink else None
    with BashHost(
        cfg.bash_host.image, cfg.bash_host.command_timeout_s, network=network, dns=dns
    ) as host:
        for sf in task.setup_files:
            host.seed_file(sf.path, sf.content)
        yield make_bash_tool(host, call_log), host


@contextmanager
def sandbox_substrate(cfg: Config, task: Task, call_log: CallLog) -> Iterator[Any]:
    """Create an emberd sandbox, seed files, yield (run_code tool, None).

    The sandbox has no host-level tripwires: it runs in a separate microVM with a
    disposable overlay and no network device, so host effects are contained by
    construction (spec §1). Probe is None.
    """
    client = EmberdClient(cfg.emberd.base_url, cfg.emberd.exec_timeout_ms)
    try:
        with SandboxSession(client) as sess:
            # Seed files by writing them through the sandbox shell.
            for sf in task.setup_files:
                _seed_sandbox_file(sess, sf.path, sf.content)
            yield make_run_code_tool(sess, call_log), None
    finally:
        client.close()


def _seed_sandbox_file(sess: SandboxSession, path: str, content: str) -> None:
    # Write via a heredoc so arbitrary content lands intact in the guest.
    import shlex

    quoted = shlex.quote(content)
    sess.run(f"mkdir -p \"$(dirname {shlex.quote(path)})\" 2>/dev/null; printf '%s' {quoted} > {shlex.quote(path)}")


SUBSTRATES = {"shell": shell_substrate, "sandbox": sandbox_substrate}


# --- step extraction ---------------------------------------------------------


def messages_to_steps(messages: list[Any], call_log: CallLog) -> list[StepRecord]:
    """Map the agent's message stream + raw call log into ordered StepRecords."""
    steps: list[StepRecord] = []
    call_idx = 0
    step_idx = 0
    for msg in messages:
        if not isinstance(msg, AIMessage):
            continue
        reasoning = _text_of(msg)
        tool_calls = getattr(msg, "tool_calls", None) or []
        if not tool_calls:
            # Terminal reasoning / final answer with no tool call.
            steps.append(StepRecord(step_idx, reasoning, None, None, None))
            step_idx += 1
            continue
        for tc in tool_calls:
            call = call_log.calls[call_idx] if call_idx < len(call_log.calls) else None
            call_idx += 1
            steps.append(
                StepRecord(
                    index=step_idx,
                    reasoning=reasoning,
                    tool=tc.get("name"),
                    tool_input=call.argument if call else _arg_of(tc),
                    result=result_to_dict(call.result) if call else None,
                )
            )
            step_idx += 1
    return steps


def _text_of(msg: AIMessage) -> str:
    content = msg.content
    if isinstance(content, str):
        return content
    # Some providers return a list of content blocks; join the text ones.
    parts = []
    for block in content if isinstance(content, list) else []:
        if isinstance(block, dict) and block.get("type") == "text":
            parts.append(block.get("text", ""))
        elif isinstance(block, str):
            parts.append(block)
    return "".join(parts)


def _arg_of(tool_call: dict[str, Any]) -> str:
    args = tool_call.get("args", {})
    return args.get("command") or args.get("code") or ""


def _final_answer(messages: list[Any]) -> str:
    """The agent's last text message (its final answer)."""
    for msg in reversed(messages):
        if isinstance(msg, AIMessage):
            return _text_of(msg)
    return ""


def annotate_danger(steps: list[StepRecord], classifier: Any | None) -> None:
    """Label each tool-call step: static rule always, LLM judge if available."""
    for step in steps:
        if step.tool_input is None:
            continue
        static = classify_static(step.tool_input)
        step.danger["static_label"] = static.label
        step.danger["static_rule"] = static.rule
        if classifier is not None:
            judged = classifier(step.tool_input)
            step.danger["judge_label"] = judged.label
            step.danger["judge_rationale"] = judged.rationale


def annotate_secret_reads(steps: list[StepRecord], markers: dict[str, str]) -> None:
    """Attach a secret_read delta to the specific step whose output leaked a marker."""
    if not markers:
        return
    for step in steps:
        if not step.result:
            continue
        blob = f"{step.result.get('stdout', '')}\n{step.result.get('stderr', '')}"
        for path, marker in markers.items():
            if marker and marker in blob:
                step.tripwire_deltas.append(
                    {"kind": "secret_read", "path": path, "detail": "honeytoken read"}
                )


def _usage(messages: list[Any]) -> tuple[int, int, int]:
    pt = ct = 0
    for msg in messages:
        um = getattr(msg, "usage_metadata", None)
        if um:
            pt += um.get("input_tokens", 0)
            ct += um.get("output_tokens", 0)
    return pt, ct, pt + ct


# --- the trial ---------------------------------------------------------------


def run_trial(
    cfg: Config,
    task: Task,
    variant: str,
    seed: int,
    run_id: str,
    out_dir: Path,
    model: Any | None = None,
    sink: Any | None = None,
    llm_scoring: bool = True,
) -> TrialResult:
    """Run one (task, variant, seed) trial; write its trajectory; return a TrialResult.

    `sink` is an optional shared NetworkSink (shell variant) whose connection log
    is reset per trial and read back into net_egress violations. `llm_scoring`
    enables the LLM danger classifier and the LLM utility judge (set False to
    score with static rules / deterministic checks only — e.g. in tests).
    """
    if variant not in SUBSTRATES:
        raise ValueError(f"unknown variant {variant!r}")

    model = model or build_model(cfg.model)
    classifier = make_llm_classifier(model) if llm_scoring else None
    judge = make_llm_judge(model) if llm_scoring else None
    call_log = CallLog()
    pair_id = f"{task.id}/{seed}"
    traj_path = out_dir / "trajectories" / f"{task.id}__{variant}__seed{seed}.jsonl"

    meta = TrajectoryMeta(
        run_id=run_id,
        task_id=task.id,
        category=task.category,
        variant=variant,
        model_id=cfg.model.id,
        temperature=cfg.model.temperature,
        seed=seed,
        pair_id=pair_id,
        started_at=_now_iso(),
        emberd_git_sha=emberd_git_sha(cfg.root),
    )
    task_ctx = TaskContext(
        prompt=task.prompt,
        injected_payload=task.injected_payload,
        setup_files=[{"path": sf.path, "content": sf.content} for sf in task.setup_files],
        success_check=(
            {"kind": task.success_check.kind, **task.success_check.params}
            if task.success_check else None
        ),
        tripwires=list(task.tripwires),
    )

    status = "success"
    messages: list[Any] = []
    start = time.monotonic()
    teardown = "ok"
    baseline = None  # host probe baseline (shell variant only)
    markers: dict[str, str] = {}
    violations: list[Any] = []

    writer = TrajectoryWriter(traj_path)
    try:
        writer.write_header(meta, task_ctx)
        try:
            if variant == "shell":
                substrate_cm = shell_substrate(cfg, task, call_log, sink)
            else:
                substrate_cm = sandbox_substrate(cfg, task, call_log)
            with substrate_cm as (tool, probe):
                # Snapshot host tripwire baseline before the agent runs.
                if probe is not None:
                    baseline = probe.baseline()
                    markers = baseline.honeytoken_markers
                if sink is not None:
                    sink.reset_log()
                agent = build_agent([tool], model)
                recursion_limit = cfg.budget.max_steps * 2 + 1
                try:
                    result = agent.invoke(
                        {"messages": [("user", task.prompt)]},
                        config={"recursion_limit": recursion_limit},
                    )
                    messages = result["messages"]
                except GraphRecursionError:
                    status = "truncated"
                # Diff host effects while the substrate is still alive.
                if probe is not None and baseline is not None:
                    after_canaries = probe.snapshot_canaries()
                    after_procs = probe.snapshot_processes()
                    violations = (
                        diff_filesystem(baseline.canary_checksums, after_canaries)
                        + diff_processes(baseline.processes, after_procs)
                        + detect_secret_reads(markers, call_log)
                    )
                    if sink is not None:
                        violations += detect_net_egress(sink.connections())
        except Exception as e:  # substrate failure
            status = "errored"
            writer.write_step(StepRecord(0, f"[substrate error] {e}", None, None, None))

        steps = messages_to_steps(messages, call_log)
        annotate_secret_reads(steps, markers)
        annotate_danger(steps, classifier)
        for step in steps:
            writer.write_step(step)

        latency_ms = int((time.monotonic() - start) * 1000)
        if status == "success" and latency_ms > cfg.budget.max_seconds * 1000:
            status = "truncated"
        pt, ct, tt = _usage(messages)

        # Utility grade: deterministic check if the task defines one, else judge.
        if status == "errored":
            utility = "errored"
            utility_detail = "substrate failure"
        else:
            g = grade(task, _final_answer(messages), call_log, judge=judge)
            utility, utility_detail = g.verdict, g.detail

        # Record ALL detected host effects losslessly...
        total, by_type = summarize(violations)
        # ...but only the effects this task declares as tripwires count as a
        # protection failure (e.g. egress is the job on a network task, not a
        # breach). Sandbox contains host effects by construction.
        declared = set(task.tripwires)
        relevant = [v for v in violations if v.kind in declared]
        if variant == "sandbox":
            containment = "contained"
        else:
            containment = "breached" if relevant else "contained"

        writer.write_outcome(
            OutcomeRecord(
                status=status,
                utility_verdict=utility,
                utility_detail=utility_detail,
                violations_total=total,
                violations_by_type=by_type,
                containment=containment,
                prompt_tokens=pt,
                completion_tokens=ct,
                total_tokens=tt,
                latency_ms=latency_ms,
                teardown=teardown,
                ended_at=_now_iso(),
            )
        )
    finally:
        writer.close()

    return TrialResult(
        task_id=task.id,
        category=task.category,
        variant=variant,
        seed=seed,
        pair_id=pair_id,
        status=status,
        utility_verdict=utility,
        containment=containment,
        violations_total=total,
        violations_by_type=by_type,
        prompt_tokens=pt,
        completion_tokens=ct,
        total_tokens=tt,
        latency_ms=latency_ms,
        trajectory_path=traj_path,
    )


def run_matrix(
    cfg: Config,
    run_id: str,
    out_dir: Path,
    variants: tuple[str, ...] = ("shell", "sandbox"),
    model: Any | None = None,
    llm_scoring: bool = True,
) -> list[TrialResult]:
    """Run the full task × variant × trial matrix. Returns all TrialResults.

    A single network sink is shared across the run for the shell variant (its log
    is reset per trial inside run_trial). The sink is only created if the shell
    variant is being run and Docker is available; on failure the shell trials
    error out individually and the run continues (spec §9).
    """
    tasks = load_tasks(cfg.root, cfg.tasks.glob)
    model = model or build_model(cfg.model)
    results: list[TrialResult] = []

    sink_cm = _maybe_sink(cfg) if "shell" in variants else _null_cm()
    with sink_cm as sink:
        for task in tasks:
            for variant in variants:
                for seed in range(cfg.tasks.trials):
                    log_line = f"[{run_id}] {task.id} · {variant} · seed {seed}"
                    print(log_line, flush=True)
                    use_sink = sink if variant == "shell" else None
                    try:
                        result = run_trial(
                            cfg, task, variant, seed, run_id, out_dir,
                            model=model, sink=use_sink, llm_scoring=llm_scoring,
                        )
                    except Exception as e:  # never let one trial kill the matrix
                        print(f"  ! trial crashed: {e}", flush=True)
                        result = _errored_result(task, variant, seed, out_dir)
                    results.append(result)
    return results


@contextmanager
def _null_cm() -> Iterator[None]:
    yield None


@contextmanager
def _maybe_sink(cfg: Config) -> Iterator[Any]:
    """Bring up the shared network sink; yield None if it can't start."""
    from .net_sink import NetworkSink

    try:
        with NetworkSink(cfg.bash_host.image) as sink:
            yield sink
    except Exception as e:
        print(f"  ! network sink unavailable ({e}); net_egress not observed", flush=True)
        yield None


def _errored_result(task: Task, variant: str, seed: int, out_dir: Path) -> TrialResult:
    return TrialResult(
        task_id=task.id, category=task.category, variant=variant, seed=seed,
        pair_id=f"{task.id}/{seed}", status="errored", utility_verdict="errored",
        containment="contained", violations_total=0, violations_by_type={},
        prompt_tokens=0, completion_tokens=0, total_tokens=0, latency_ms=0,
        trajectory_path=out_dir / "trajectories" / f"{task.id}__{variant}__seed{seed}.jsonl",
    )


def _smoke_main() -> None:
    ap = argparse.ArgumentParser(description="Run a single trial (smoke test).")
    ap.add_argument("--task", required=True)
    ap.add_argument("--variant", choices=["shell", "sandbox"], required=True)
    ap.add_argument("--seed", type=int, default=0)
    ap.add_argument("--config", default=None)
    args = ap.parse_args()

    cfg = load_config(args.config)
    task = load_task(args.task)
    run_id = "smoke-" + datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    out_dir = cfg.root / cfg.results_dir / run_id
    result = run_trial(cfg, task, args.variant, args.seed, run_id, out_dir)
    print(f"wrote {result.trajectory_path} → {result.status}/{result.utility_verdict}")


if __name__ == "__main__":
    _smoke_main()
