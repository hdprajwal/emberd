#!/usr/bin/env bash
# Drive a running emberd daemon: run individual commands or a full lifecycle
# test. Start the daemon first with ./scripts/serve.sh (in another terminal).
#
# Usage:
#   ./scripts/emberctl.sh <command> [args]
#
# Commands:
#   create [language_pack]   Create a sandbox (boots a microVM). Prints the id.
#   inspect <id>             Show the microVM process and tail its boot log.
#   exec <id> <code...>      Run code in a sandbox (currently returns 501).
#   rm <id>                  Destroy a sandbox.
#   ls                       List running emberd microVM processes.
#   ping                     Check the daemon is reachable.
#   test                     Full create -> inspect -> exec -> delete cycle with assertions.
#
# Env:
#   ADDR   daemon address (default 127.0.0.1:7777)
set -euo pipefail

ADDR="${ADDR:-127.0.0.1:7777}"
BASE="http://${ADDR}"
WORKDIR="${TMPDIR:-/tmp}/emberd"

if [ -t 1 ]; then
  GREEN=$'\033[32m'; RED=$'\033[31m'; DIM=$'\033[2m'; BOLD=$'\033[1m'; RESET=$'\033[0m'
else
  GREEN=""; RED=""; DIM=""; BOLD=""; RESET=""
fi

die() { printf '%serror:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

# fcid <sandbox-id> -> firecracker instance id (underscores become hyphens)
fcid() { printf '%s' "${1//_/-}"; }

require_server() {
  curl -s -o /dev/null --max-time 2 -X DELETE "$BASE/sandboxes/_probe" \
    || die "daemon not reachable at $BASE — start it with ./scripts/serve.sh"
}

cmd_ping() {
  require_server
  printf '%sok%s daemon reachable at %s\n' "$GREEN" "$RESET" "$BASE"
}

cmd_create() {
  require_server
  local pack="${1:-python}"
  local resp body code id
  resp="$(curl -s -X POST "$BASE/sandboxes" -d "{\"language_pack\":\"${pack}\"}" -w '\n%{http_code}')"
  body="$(printf '%s' "$resp" | sed '$d')"
  code="$(printf '%s' "$resp" | tail -1)"
  [ "$code" = "201" ] || die "create failed (HTTP $code): $body"
  id="$(printf '%s' "$body" | grep -o 'sb_[a-f0-9]*')"
  echo "$id"
}

cmd_inspect() {
  local id="${1:?usage: inspect <id>}" fc proc
  fc="$(fcid "$id")"
  proc="$(pgrep -af "id $fc" | grep -v ' pgrep ' || true)"
  if [ -n "$proc" ]; then
    printf '%smicroVM running:%s\n  %s\n' "$BOLD" "$RESET" "$proc"
  else
    printf '%sno microVM process found for %s%s\n' "$DIM" "$id" "$RESET"
  fi
  local log="$WORKDIR/$id/vm.log"
  if [ -f "$log" ]; then
    printf '%sboot log (%s):%s\n' "$BOLD" "$log" "$RESET"
    grep -m1 "Linux version" "$log" | sed 's/^/  /' || true
  fi
}

cmd_exec() {
  local id="${1:?usage: exec <id> <code...>}"; shift
  local code="$*"
  require_server
  curl -s -X POST "$BASE/sandboxes/$id/exec" \
    -d "$(printf '{"code":%s}' "$(json_str "$code")")" \
    -w '\nHTTP %{http_code}\n'
}

cmd_rm() {
  local id="${1:?usage: rm <id>}"
  require_server
  curl -s -o /dev/null -X DELETE "$BASE/sandboxes/$id" -w 'HTTP %{http_code}\n'
}

cmd_ls() {
  pgrep -af 'firecracker --api-sock' | grep "$WORKDIR" || echo "(no emberd microVMs running)"
}

# json_str <text> -> minimally-escaped JSON string literal
json_str() {
  local s="$1"
  s="${s//\\/\\\\}"; s="${s//\"/\\\"}"
  printf '"%s"' "$s"
}

# ---- full lifecycle test --------------------------------------------------
cmd_test() {
  require_server
  local pass=0 fail=0
  ok()  { printf '  %sPASS%s %s\n' "$GREEN" "$RESET" "$*"; pass=$((pass+1)); }
  bad() { printf '  %sFAIL%s %s\n' "$RED" "$RESET" "$*"; fail=$((fail+1)); }
  ac()  { [ "$2" = "$1" ] && ok "$3 -> HTTP $2" || bad "$3 -> HTTP $2 (expected $1)"; }

  printf '%s==> create%s\n' "$BOLD" "$RESET"
  local resp body code id fc
  resp="$(curl -s -X POST "$BASE/sandboxes" -d '{"language_pack":"python"}' -w '\n%{http_code}')"
  body="$(printf '%s' "$resp" | sed '$d')"; code="$(printf '%s' "$resp" | tail -1)"
  printf '  %s%s%s\n' "$DIM" "$body" "$RESET"
  ac 201 "$code" "create"
  id="$(printf '%s' "$body" | grep -o 'sb_[a-f0-9]*' || true)"
  [ -n "$id" ] && ok "got id $id" || { bad "no id returned"; printf '\n%d passed, %d failed\n' "$pass" "$fail"; return 1; }
  fc="$(fcid "$id")"

  printf '%s==> inspect%s\n' "$BOLD" "$RESET"
  pgrep -af "id $fc" | grep -qv ' pgrep ' && ok "microVM process running" || bad "no microVM process"
  grep -q "Linux version" "$WORKDIR/$id/vm.log" 2>/dev/null && ok "guest kernel booted" || bad "no kernel boot in vm.log"

  printf '%s==> exec%s\n' "$BOLD" "$RESET"
  # Wait for the guest agent to come up, then run real code.
  local execout=""
  for _ in $(seq 1 50); do
    execout="$(curl -s -X POST "$BASE/sandboxes/$id/exec" -d '{"code":"print(6*7)"}')"
    case "$execout" in *'"stdout":"42'*) break ;; esac
    sleep 0.2
  done
  printf '  %s%s%s\n' "$DIM" "$execout" "$RESET"
  case "$execout" in
    *'"stdout":"42'*) ok "exec returned stdout 42" ;;
    *) bad "exec did not return 42 (got: $execout)" ;;
  esac
  case "$execout" in *'"exit_code":0'*) ok "exit_code 0" ;; *) bad "exit_code not 0" ;; esac
  local exiterr
  exiterr="$(curl -s -X POST "$BASE/sandboxes/$id/exec" -d '{"code":"import sys;sys.exit(7)"}')"
  case "$exiterr" in *'"exit_code":7'*) ok "non-zero exit propagated (7)" ;; *) bad "exit code 7 not propagated (got: $exiterr)" ;; esac
  ac 404 "$(curl -s -o /dev/null -X POST "$BASE/sandboxes/sb_missing/exec" -d '{"code":"x"}' -w '%{http_code}')" "exec on unknown sandbox"

  printf '%s==> delete%s\n' "$BOLD" "$RESET"
  ac 204 "$(curl -s -o /dev/null -X DELETE "$BASE/sandboxes/$id" -w '%{http_code}')" "delete"
  pgrep -af "id $fc" | grep -qv ' pgrep ' && bad "process still running after delete" || ok "microVM process gone"
  [ -d "$WORKDIR/$id" ] && bad "work dir not cleaned: $WORKDIR/$id" || ok "work dir cleaned"
  ac 404 "$(curl -s -o /dev/null -X DELETE "$BASE/sandboxes/$id" -w '%{http_code}')" "delete again"

  printf '\n%s%d passed%s, %s%d failed%s\n' "$GREEN" "$pass" "$RESET" "$RED" "$fail" "$RESET"
  [ "$fail" -eq 0 ]
}

# ---- dispatch -------------------------------------------------------------
sub="${1:-}"; shift || true
case "$sub" in
  create)  cmd_create "$@" ;;
  inspect) cmd_inspect "$@" ;;
  exec)    cmd_exec "$@" ;;
  rm|delete) cmd_rm "$@" ;;
  ls|ps)   cmd_ls ;;
  ping)    cmd_ping ;;
  test)    cmd_test ;;
  ""|-h|--help|help)
    sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//' ;;
  *) die "unknown command: $sub (try: ./scripts/emberctl.sh help)" ;;
esac
