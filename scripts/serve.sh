#!/usr/bin/env bash
# Build and run the emberd daemon in the foreground.
#
# Usage:
#   ./scripts/serve.sh
#   ADDR=127.0.0.1:7788 ./scripts/serve.sh   # override listen address
#
# Ctrl-C to stop. Drive it from another terminal with ./scripts/emberctl.sh.
set -euo pipefail

ADDR="${ADDR:-127.0.0.1:7777}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "building bin/emberd and bin/emberd-init..."
go build -o bin/emberd ./cmd/emberd
go build -o bin/emberd-init ./cmd/emberd-init

echo "starting emberd on ${ADDR} (Ctrl-C to stop)"
exec ./bin/emberd --addr "$ADDR"
