#!/bin/sh
# Shared tooling and security patches must match in the agent and IDE images.
set -eu
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
for name in NPM_VERSION CLAUDE_CODE_VERSION CODEX_VERSION OPENCODE_VERSION \
  BRACE_EXPANSION_VERSION IP_ADDRESS_VERSION JS_YAML_VERSION TAR_VERSION UNDICI_VERSION; do
  agent=$(sed -n "s/^ARG $name=//p" "$repo/devmachine/agent/Dockerfile")
  ide=$(sed -n "s/^ARG $name=//p" "$repo/devmachine/ide/Dockerfile")
  if [ -z "$agent" ] || [ "$agent" != "$ide" ]; then
    printf '%s must match in agent and IDE images (agent=%s ide=%s)\n' "$name" "$agent" "$ide" >&2
    exit 1
  fi
done
