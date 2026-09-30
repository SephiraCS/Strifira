#!/bin/sh
set -eu

repository=${1:-}
base_branch=${2:-}
working_branch=${3:-}
workspace_path=${4:-}
repository_url=${5:-}
workspace_root=${KU_WORKSPACE_ROOT:-/workspace}
checkout_secret_dir=${KU_CHECKOUT_SECRET_DIR:-/run/ku-secrets}
checkout_tmp_dir=${KU_CHECKOUT_TMP_DIR:-/tmp}
home_dir=${HOME:-/home/ku}
credential_helper=${KU_GIT_CREDENTIAL_HELPER:-$home_dir/.ku-git-credential}
token_file=
askpass=

cleanup() {
  [ -z "$token_file" ] || rm -f "$token_file"
  [ -z "$askpass" ] || rm -f "$askpass"
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

if [ "${KU_CHECKOUT_ALLOW_ROOT_OVERRIDE:-0}" != "1" ]; then
  [ "$workspace_root" = /workspace ] || { echo "invalid workspace root" >&2; exit 1; }
  [ "$checkout_secret_dir" = /run/ku-secrets ] || { echo "invalid checkout secret dir" >&2; exit 1; }
  [ "$checkout_tmp_dir" = /tmp ] || { echo "invalid checkout tmp dir" >&2; exit 1; }
fi

case "$workspace_root" in
  /* ) ;;
  * ) echo "invalid workspace root" >&2; exit 1 ;;
esac
case "$workspace_root" in /|*/|*..*|*//* ) echo "invalid workspace root" >&2; exit 1 ;; esac
case "$checkout_secret_dir" in /* ) ;; * ) echo "invalid checkout secret dir" >&2; exit 1 ;; esac
case "$checkout_secret_dir" in /|*/|*..*|*//* ) echo "invalid checkout secret dir" >&2; exit 1 ;; esac
case "$checkout_tmp_dir" in /* ) ;; * ) echo "invalid checkout tmp dir" >&2; exit 1 ;; esac
case "$checkout_tmp_dir" in /|*..*|*//* ) echo "invalid checkout tmp dir" >&2; exit 1 ;; esac
case "$credential_helper" in /* ) ;; * ) echo "invalid credential helper path" >&2; exit 1 ;; esac
[ -d "$checkout_secret_dir" ] || { echo "checkout secret dir is unavailable" >&2; exit 1; }
[ -d "$checkout_tmp_dir" ] || { echo "checkout tmp dir is unavailable" >&2; exit 1; }

install_git_credential_helper() {
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

configure_checkout_git_helper() {
  checkout_dir=$1
  install_git_credential_helper
  git -C "$checkout_dir" config --unset-all core.askPass >/dev/null 2>&1 || true
  git -C "$checkout_dir" config --unset-all credential.helper >/dev/null 2>&1 || true
  git -C "$checkout_dir" config credential.helper "$credential_helper"
  git -C "$checkout_dir" config credential.useHttpPath true
}

clear_bare_git_credentials() {
  bare_dir=$1
  git --git-dir="$bare_dir" config --unset-all core.askPass >/dev/null 2>&1 || true
  git --git-dir="$bare_dir" config --unset-all credential.helper >/dev/null 2>&1 || true
}

case "$repository" in
  [A-Za-z0-9-]*/[A-Za-z0-9._-]* ) ;;
  * ) echo "invalid repository" >&2; exit 1 ;;
esac
workspace_tasks=$workspace_root/tasks
case "$workspace_path" in
  "$workspace_tasks"/* )
    case "$workspace_path" in *..*|*//*|"$workspace_tasks"/*/* ) echo "invalid workspace path" >&2; exit 1 ;; esac
    workspace_prefix=$workspace_tasks/
    workspace_name=${workspace_path#"$workspace_prefix"}
    case "$workspace_name" in ''|.|..|*[!A-Za-z0-9._-]* ) echo "invalid workspace path" >&2; exit 1 ;; esac
    ;;
  * ) echo "invalid workspace path" >&2; exit 1 ;;
esac
valid_ref() {
  ref=$1
  case "$ref" in ''|-*|.*|*..*|*//*|*@\{*|*~*|*^*|*:*|*\?*|*\**|*'['*|*'\'*|*/|*. ) return 1 ;; esac
  old_ifs=$IFS
  IFS=/
  for component in $ref; do
    case "$component" in ''|.|..|*.lock ) IFS=$old_ifs; return 1 ;; esac
    case "$component" in *[!A-Za-z0-9._-]* ) IFS=$old_ifs; return 1 ;; esac
  done
  IFS=$old_ifs
  return 0
}
valid_ref "$base_branch" && valid_ref "$working_branch" || { echo "invalid branch" >&2; exit 1; }

case "$repository_url" in
  https://* ) ;;
  * ) echo "repository URL must use HTTPS" >&2; exit 1 ;;
esac
case "$repository_url" in *'@'*|*'?'*|*'#'*|*' '* ) echo "invalid repository URL" >&2; exit 1 ;; esac
url_without_scheme=${repository_url#https://}
repository_host=${url_without_scheme%%/*}
case "$repository_host" in *[!A-Za-z0-9.:-]* ) echo "invalid repository host" >&2; exit 1 ;; esac
[ -n "$repository_host" ] && [ "$url_without_scheme" != "$repository_host" ] || { echo "invalid repository URL" >&2; exit 1; }
repository_url_path=${url_without_scheme#*/}
[ "${repository_url_path%.git}" = "$repository" ] || { echo "repository URL must match repository" >&2; exit 1; }

IFS= read -r github_token
[ -n "$github_token" ] || { echo "missing repository credential" >&2; exit 1; }
umask 077
token_file=$(mktemp "$checkout_secret_dir/ku-checkout-token.XXXXXX")
printf '%s' "$github_token" > "$token_file"
chmod 0600 "$token_file"
unset github_token

askpass=$(mktemp "$checkout_tmp_dir/ku-checkout-credential.XXXXXX")
cat > "$askpass" <<'SCRIPT'
#!/bin/sh
[ "${1:-get}" = get ] || exit 0
protocol=; host=; path=
while IFS= read -r line && [ -n "$line" ]; do
  case "$line" in protocol=*) protocol=${line#protocol=} ;; host=*) host=${line#host=} ;; path=*) path=${line#path=} ;; esac
done
if [ "$protocol" = https ] && [ "$host" = "$KU_CHECKOUT_HOST" ] && [ "${path%.git}" = "$KU_CHECKOUT_REPOSITORY" ]; then
  printf '%s\n' username=oauth2
  printf 'password='
  cat "$KU_CHECKOUT_TOKEN_FILE"
  printf '\n'
fi
SCRIPT
chmod 0700 "$askpass"
export GIT_ASKPASS=/bin/false GIT_TERMINAL_PROMPT=0 KU_CHECKOUT_TOKEN_FILE="$token_file"
export KU_CHECKOUT_HOST="$repository_host" KU_CHECKOUT_REPOSITORY="$repository"
export GIT_CONFIG_COUNT=3 GIT_CONFIG_KEY_0=credential.helper GIT_CONFIG_VALUE_0=
export GIT_CONFIG_KEY_1=credential.helper GIT_CONFIG_VALUE_1="$askpass"
export GIT_CONFIG_KEY_2=credential.useHttpPath GIT_CONFIG_VALUE_2=true

repository_key=$(printf '%s' "$repository_host/$repository" | tr '/:' '--')
bare_repository="$workspace_root/repos/${repository_key}.git"


if [ ! -d "$bare_repository" ]; then
  git clone --bare "$repository_url" "$bare_repository"
  git --git-dir="$bare_repository" config remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*'
  clear_bare_git_credentials "$bare_repository"
else
  clear_bare_git_credentials "$bare_repository"
  git --git-dir="$bare_repository" remote set-url origin "$repository_url"
fi

git --git-dir="$bare_repository" fetch --prune origin
git --git-dir="$bare_repository" worktree prune

if [ -e "$workspace_path" ]; then
  [ -e "$workspace_path/.git" ] || { echo "workspace path is not an existing checkout" >&2; exit 1; }
  configure_checkout_git_helper "$workspace_path"
  git config --global --add safe.directory "$workspace_path"
  ln -sfn "$workspace_path" "$workspace_root/current"
  exit 0
fi

mkdir -p "$(dirname "$workspace_path")"
if git --git-dir="$bare_repository" show-ref --verify --quiet "refs/remotes/origin/$working_branch"; then
  git --git-dir="$bare_repository" worktree add -B "$working_branch" "$workspace_path" "refs/remotes/origin/$working_branch"
else
  git --git-dir="$bare_repository" show-ref --verify --quiet "refs/remotes/origin/$base_branch"
  git --git-dir="$bare_repository" worktree add -b "$working_branch" "$workspace_path" "refs/remotes/origin/$base_branch"
fi

configure_checkout_git_helper "$workspace_path"
git config --global --add safe.directory "$workspace_path"
ln -sfn "$workspace_path" "$workspace_root/current"
