#!/usr/bin/env bash
# 02-shell-exec: shell language pack via curl + jq.
# Requires: curl, jq, emberd running on 127.0.0.1:7777.
set -euo pipefail

ADDR="${EMBERD_ADDR:-127.0.0.1:7777}"

echo "Creating shell sandbox..."
ID=$(curl -sf -X POST "http://$ADDR/sandboxes" \
  -H "Content-Type: application/json" \
  -d '{"language_pack":"shell"}' | jq -r .id)
echo "  $ID"
echo ""

cleanup() { curl -sf -X DELETE "http://$ADDR/sandboxes/$ID" > /dev/null && echo "Sandbox destroyed."; }
trap cleanup EXIT

run_cmd() {
  local label="$1" cmd="$2"
  echo "[$label]"
  curl -sf -X POST "http://$ADDR/sandboxes/$ID/exec" \
    -H "Content-Type: application/json" \
    -d "$(jq -n --arg code "$cmd" '{code: $code}')" | jq -r '.stdout // "(no output)"'
  echo ""
}

run_cmd "kernel version"  "uname -r"
run_cmd "current user"    "id"
run_cmd "process list"    "ps aux"
run_cmd "disk usage"      "df -h /"
run_cmd "network devices" "ip link show 2>/dev/null || echo '(no ip command)'"
