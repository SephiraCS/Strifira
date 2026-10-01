#!/bin/sh
set -eu

provider=${1:-opencode}
mode=${2:---offline}
case "$provider" in claude-code) cli=claude ;; codex|opencode) cli=$provider ;; *) echo 'usage: smoke-test.sh [claude-code|codex|opencode] [--offline|--live]' >&2; exit 2 ;; esac
case "$mode" in --offline|--live) ;; *) echo 'mode must be --offline or --live' >&2; exit 2 ;; esac
command -v "$cli" >/dev/null 2>&1 || { echo "missing provider CLI: $cli" >&2; exit 1; }
"$cli" --version
command -v go >/dev/null 2>&1 || { echo 'missing Go toolchain' >&2; exit 1; }
command -v make >/dev/null 2>&1 || { echo 'missing Make' >&2; exit 1; }

smoke_dir=$(mktemp -d /tmp/ku-agent-smoke.XXXXXX)
cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    printf '%s\n' "$provider $mode smoke failed (exit $status)" >&2
  fi
  rm -rf "$smoke_dir"
}
trap cleanup EXIT
cd "$smoke_dir"
printf '%s\n' 'package main' 'import "fmt"' 'func main() { fmt.Println("ku-smoke-test-passed") }' > main.go
go build -o smoke-binary main.go
[ "$(./smoke-binary)" = ku-smoke-test-passed ] || exit 1
printf '%s\n' 'offline toolchain smoke passed'
[ "$mode" = --live ] || exit 0

# A disposable repository with no remote, hooks, dependencies or user files.
# Provider authentication is reused, but no credentials are read or displayed.
git init --quiet
git -c user.name=KuSmoke -c user.email=ku-smoke@localhost add main.go
git -c user.name=KuSmoke -c user.email=ku-smoke@localhost commit --quiet -m 'Initialize disposable smoke fixture'
printf '%s\n' '#!/bin/sh' 'printf "%s\n" old-value' > greeting.sh
printf '%s\n' '#!/bin/sh' 'set -eu' '[ "$(sh greeting.sh)" = ku-agent-live-ok ]' > test.sh
prompt='In this disposable repository, edit only greeting.sh so it prints exactly ku-agent-live-ok on one line, then run sh test.sh. Do not read credentials, inspect other directories, use the network, commit or push. Finish with a brief summary.'
case "$provider" in
  claude-code)
    timeout 120 claude --print --output-format stream-json --verbose --permission-mode dontAsk \
      --allowedTools 'Read,Edit,Write,Bash(sh test.sh)' --tools Read,Edit,Write,Bash \
      --setting-sources '' --strict-mcp-config --no-session-persistence --max-budget-usd 0.50 \
      -p "$prompt" < /dev/null > provider.jsonl 2> provider.stderr
    ;;
  codex)
    timeout 120 codex exec --json --ephemeral --ignore-rules --sandbox workspace-write -c 'approval_policy="never"' "$prompt" < /dev/null > provider.jsonl 2> provider.stderr
    ;;
  opencode)
    timeout 120 opencode run --format json "$prompt" < /dev/null > provider.jsonl 2> provider.stderr
    ;;
esac
sh test.sh
[ "$(sh greeting.sh)" = ku-agent-live-ok ]
# Keep live provider output out of console logs; assert terminal state only.
case "$provider" in
  claude-code) node -e 'const fs=require("fs");const events=fs.readFileSync("provider.jsonl","utf8").trim().split("\n").map(JSON.parse);if(!events.some(e=>e.type==="result"&&e.subtype==="success"&&!e.is_error))process.exit(1)' ;;
  codex) node -e 'const fs=require("fs");const events=fs.readFileSync("provider.jsonl","utf8").trim().split("\n").map(JSON.parse);if(!events.some(e=>e.type==="turn.completed")||events.some(e=>e.type==="turn.failed"))process.exit(1)' ;;
esac
printf '%s\n' "$provider live edit and independent test passed"
