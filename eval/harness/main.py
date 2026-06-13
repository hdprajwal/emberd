"""Run the full eval matrix and write the report.

Usage:
    uv run python -m harness.main [--config config.yaml] [--variants shell,sandbox]
                                  [--no-llm-scoring]

Prerequisites for a real run: a Claude API key in the environment, Docker for the
shell variant, and a running emberd daemon for the sandbox variant. Missing
substrates cause individual trials to error (recorded), not the whole run.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone

from .config import load_config
from .report import write_report
from .runner import run_matrix
from .store import ResultStore


def main() -> None:
    ap = argparse.ArgumentParser(description="Run the emberd eval matrix.")
    ap.add_argument("--config", default=None)
    ap.add_argument(
        "--variants", default="shell,sandbox",
        help="comma-separated subset of {shell,sandbox}",
    )
    ap.add_argument(
        "--no-llm-scoring", action="store_true",
        help="disable the LLM danger classifier and utility judge",
    )
    ap.add_argument(
        "--force", action="store_true",
        help="re-run every trial, ignoring cached results in the store",
    )
    args = ap.parse_args()

    cfg = load_config(args.config)
    variants = tuple(v.strip() for v in args.variants.split(",") if v.strip())
    run_id = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    store = ResultStore(cfg.store_path)

    results = run_matrix(
        cfg, run_id, store, variants=variants,
        llm_scoring=not args.no_llm_scoring, force=args.force,
    )
    write_report(results, run_id, cfg.model.id, store.dir)
    print(f"\nrun complete: {len(results)} trial(s) in store → {store.dir}")
    print(f"  summary:    {store.dir / 'summary.md'}")
    print(f"  scoreboard: {store.dir / 'scoreboard.csv'}")


if __name__ == "__main__":
    main()
