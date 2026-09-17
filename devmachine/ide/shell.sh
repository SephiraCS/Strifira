#!/bin/sh
set -eu

if [ ! -t 0 ]; then
  exec /bin/bash "$@"
fi

export HISTCONTROL=ignoredups
export PROMPT_COMMAND='ku_status=$?; ku_command=$(history 1); KU_EXIT_CODE=$ku_status KU_COMMAND=$ku_command node /usr/local/lib/ku-command-event.js >/dev/null 2>&1 || true; history -a'
exec /bin/bash "$@"
