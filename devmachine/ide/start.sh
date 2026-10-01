#!/bin/sh
set -eu

rm -f /workspace/.ku-ready
mkdir -p "$HOME/.local/share/code-server" "$HOME/.config/code-server"
if [ -d /opt/ku-home-template ] && [ ! -f "$HOME/.ku-template-seeded" ]; then
  cp -a /opt/ku-home-template/. "$HOME/"
  touch "$HOME/.ku-template-seeded"
fi
user_settings_dir="$HOME/.local/share/code-server/User"
if [ ! -e "$user_settings_dir/settings.json" ]; then
  mkdir -p "$user_settings_dir"
  printf '%s\n' '{"workbench.colorTheme":"Default Dark Modern"}' > "$user_settings_dir/settings.json"
fi

unset FORGEJO_TOKEN GITHUB_TOKEN KU_GIT_CREDENTIAL_HELPER KU_FORGEJO_HOST KU_GIT_REPOSITORY
export GIT_TERMINAL_PROMPT=0
mkdir -p /workspace/tasks

default_workspace=/workspace/tasks
[ ! -d /workspace/repository/.git ] || default_workspace=/workspace/repository
code-server --bind-addr 0.0.0.0:8080 --auth none --disable-telemetry "$default_workspace" &
code_server_pid=$!
ttyd --port 7681 --writable --url-arg --terminal-type xterm-256color /usr/local/bin/ku-terminal-session &
ttyd_pid=$!

cleanup() {
  kill "$code_server_pid" "$ttyd_pid" 2>/dev/null || true
  wait "$code_server_pid" "$ttyd_pid" 2>/dev/null || true
}
trap cleanup INT TERM EXIT

attempt=0
while ! curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 || ! kill -0 "$ttyd_pid" 2>/dev/null; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 300 ]; then
    echo "developer services did not become ready" >&2
    exit 1
  fi
  sleep 0.1
done
touch /workspace/.ku-ready

while kill -0 "$code_server_pid" 2>/dev/null && kill -0 "$ttyd_pid" 2>/dev/null; do
  sleep 1
done
exit 1
