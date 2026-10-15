#!/bin/sh
set -eu

if [ ! -t 0 ]; then
  exec /bin/bash "$@"
fi

export HISTCONTROL=ignoredups
export PROMPT_COMMAND='strifira_status=$?; strifira_command=$(history 1); STRIFIRA_EXIT_CODE=$strifira_status STRIFIRA_COMMAND=$strifira_command node /usr/local/lib/strifira-command-event.js >/dev/null 2>&1 || true; history -a'
exec /bin/bash "$@"
