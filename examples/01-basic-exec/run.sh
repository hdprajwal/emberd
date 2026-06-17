#!/usr/bin/env bash
# 01-basic-exec: same lifecycle using curl + jq.
# Requires: curl, jq, emberd running on 127.0.0.1:7777.
set -euo pipefail

ADDR="${EMBERD_ADDR:-127.0.0.1:7777}"

echo "Creating sandbox..."
ID=$(curl -sf -X POST "http://$ADDR/sandboxes" \
  -H "Content-Type: application/json" \
  -d '{"language_pack":"python"}' | jq -r .id)
echo "  $ID"

cleanup() { curl -sf -X DELETE "http://$ADDR/sandboxes/$ID" > /dev/null && echo "  destroyed."; }
trap cleanup EXIT

CODE='import math
values = [1, 4, 9, 16, 25, 36]
roots  = [math.sqrt(v) for v in values]
print("squares:", values)
print("roots:  ", roots)
print("sum of roots:", sum(roots))'

echo ""
echo "Executing code..."
curl -sf -X POST "http://$ADDR/sandboxes/$ID/exec" \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg code "$CODE" '{code: $code, timeout_ms: 5000}')" | jq .

echo ""
echo "Destroying sandbox..."
