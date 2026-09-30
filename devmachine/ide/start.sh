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

home_dir=${HOME:-/home/ku}
credential_helper=${KU_GIT_CREDENTIAL_HELPER:-$home_dir/.ku-git-credential}

install_git_credential_helper() {
  case "$credential_helper" in /* ) ;; * ) echo "invalid credential helper path" >&2; exit 1 ;; esac
  helper_dir=${credential_helper%/*}
  mkdir -p "$helper_dir"
  old_umask=$(umask)
  umask 077
  cat > "$credential_helper" <<'SCRIPT'
#!/bin/sh
case "${1:-get}" in
  get ) ;;
  store|erase ) exit 0 ;;
  * ) exit 0 ;;
esac
protocol=
host=
path=
while IFS= read -r line && [ -n "$line" ]; do
  case "$line" in
    protocol=*) protocol=${line#protocol=} ;;
    host=*) host=${line#host=} ;;
    path=*) path=${line#path=} ;;
  esac
done
[ "$protocol" = https ] || exit 0
path=${path%.git}
if [ -n "${FORGEJO_TOKEN:-}" ] && [ -n "${KU_FORGEJO_HOST:-}" ] && [ -n "${KU_GIT_REPOSITORY:-}" ] \
    && [ "$host" = "$KU_FORGEJO_HOST" ] && [ "$path" = "$KU_GIT_REPOSITORY" ]; then
  printf '%s\n' username=oauth2
  printf '%s\n' "password=$FORGEJO_TOKEN"
elif [ "$host" = github.com ] && [ -n "${GITHUB_TOKEN:-}" ] \
    && { [ -z "${KU_GIT_REPOSITORY:-}" ] || [ "$path" = "$KU_GIT_REPOSITORY" ]; }; then
  printf '%s\n' username=x-access-token
  printf '%s\n' "password=$GITHUB_TOKEN"
fi
SCRIPT
  chmod 0700 "$credential_helper"
  umask "$old_umask"
}

configure_git_helper() {
  git -C "$1" config --unset-all core.askPass >/dev/null 2>&1 || true
  git -C "$1" config --unset-all credential.helper >/dev/null 2>&1 || true
  git -C "$1" config credential.helper "$credential_helper"
}

configure_bare_git_helper() {
  git --git-dir="$1" config --unset-all core.askPass >/dev/null 2>&1 || true
  git --git-dir="$1" config --unset-all credential.helper >/dev/null 2>&1 || true
  git --git-dir="$1" config credential.helper "$credential_helper"
}

install_git_credential_helper
git config --global --unset-all core.askPass >/dev/null 2>&1 || true
git config --global --unset-all credential.helper >/dev/null 2>&1 || true
git config --global credential.helper "$credential_helper"
git config --global credential.useHttpPath true
export GIT_TERMINAL_PROMPT=0

if [ ! -d /workspace/.git ] && [ -n "${KU_REPO_URL:-}" ]; then
  git clone --branch "${KU_BASE_BRANCH:-main}" --single-branch "$KU_REPO_URL" /workspace
fi

mkdir -p /workspace/repos /workspace/tasks

if [ -d /workspace/.git ]; then
  configure_git_helper /workspace
fi
for bare_repository in /workspace/repos/*.git; do
  [ -d "$bare_repository" ] || continue
  configure_bare_git_helper "$bare_repository"
done
for checkout in /workspace/tasks/*; do
  [ -e "$checkout/.git" ] || continue
  configure_git_helper "$checkout"
done

if [ -d /workspace/.git ] && [ -n "${KU_WORKING_BRANCH:-}" ]; then
  git -C /workspace config --global --add safe.directory /workspace
  if git -C /workspace ls-remote --exit-code --heads origin "$KU_WORKING_BRANCH" >/dev/null 2>&1; then
    git -C /workspace fetch origin "$KU_WORKING_BRANCH"
    git -C /workspace checkout -B "$KU_WORKING_BRANCH" FETCH_HEAD
  else
    git -C /workspace checkout "$KU_WORKING_BRANCH" 2>/dev/null || git -C /workspace checkout -b "$KU_WORKING_BRANCH"
  fi
  hooks=/workspace/.git/hooks
  cat > "$hooks/post-commit" <<'SCRIPT'
#!/bin/sh
commit=$(git rev-parse HEAD)
curl -fsS -X POST -H 'Content-Type: application/json' --data "{\"source\":\"git\",\"event_type\":\"git.commit_created\",\"payload\":{\"commit\":\"$commit\"}}" "$KU_COLLECTOR_URL/event" >/dev/null 2>&1 || true
SCRIPT
  cat > "$hooks/post-checkout" <<'SCRIPT'
#!/bin/sh
branch=$(git branch --show-current)
curl -fsS -X POST -H 'Content-Type: application/json' --data "{\"source\":\"git\",\"event_type\":\"git.branch_changed\",\"payload\":{\"branch\":\"$branch\"}}" "$KU_COLLECTOR_URL/event" >/dev/null 2>&1 || true
SCRIPT
  cat > "$hooks/pre-push" <<'SCRIPT'
#!/bin/sh
curl -fsS -X POST -H 'Content-Type: application/json' --data '{"source":"git","event_type":"git.push_started","payload":{}}' "$KU_COLLECTOR_URL/event" >/dev/null 2>&1 || true
SCRIPT
  chmod 0755 "$hooks/post-commit" "$hooks/post-checkout" "$hooks/pre-push"
fi

default_workspace=/workspace/tasks
[ ! -d /workspace/.git ] || default_workspace=/workspace
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
