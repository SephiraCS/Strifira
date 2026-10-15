package machine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func protectedCheckoutRepository(checkout *domain.DevMachineCheckout) (string, *url.URL, error) {
	if checkout == nil || checkout.ID == uuid.Nil {
		return "", nil, errors.New("checkout identity is unavailable")
	}
	parsed, err := url.Parse(checkout.RepositoryURL)
	owner, name, valid := splitRepositoryFullName(checkout.RepositoryFullName)
	validSegment := func(value string) bool {
		if value == "." || value == ".." {
			return false
		}
		for _, char := range value {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-", char) {
				return false
			}
		}
		return value != ""
	}
	if err != nil || !valid || !validSegment(owner) || !validSegment(name) || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.EscapedPath() != parsed.Path || strings.TrimSuffix(parsed.Path, ".git") != "/"+checkout.RepositoryFullName {
		return "", nil, errors.New("checkout requires a credential-free HTTPS URL for the selected repository")
	}
	path := checkout.WorkspacePath
	if path != "/workspace/repository" {
		segment, found := strings.CutPrefix(path, "/workspace/tasks/")
		if !found || !validSegment(segment) || strings.Contains(segment, "..") {
			return "", nil, errors.New("checkout path is outside its selected workspace")
		}
	}
	for _, branch := range []string{checkout.BaseBranch, checkout.WorkingBranch} {
		if branch == "" || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") {
			return "", nil, errors.New("checkout branch is invalid")
		}
		for component := range strings.SplitSeq(branch, "/") {
			if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
				return "", nil, errors.New("checkout branch is invalid")
			}
		}
		for _, char := range branch {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-/", char) {
				return "", nil, errors.New("checkout branch is invalid")
			}
		}
	}
	key := sha256.Sum256([]byte(strings.TrimSuffix(checkout.RepositoryURL, ".git")))
	return "/repositories/" + hex.EncodeToString(key[:]) + ".git", parsed, nil
}

func (r *DockerRuntime) PrepareCheckout(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, token string) error {
	if machine == nil {
		return errors.New("checkout machine is unavailable")
	}
	repository, _, err := protectedCheckoutRepository(checkout)
	if err != nil {
		return err
	}
	if err := r.PrepareCheckoutRepository(ctx, machine, services, checkout, token); err != nil {
		return err
	}
	runKey := "checkout-" + checkout.ID.String()
	repositoryURL := strings.TrimSuffix(checkout.RepositoryURL, ".git")
	stageKey, err := r.checkoutStageKey(ctx, machine, checkout.ID)
	if err != nil {
		return err
	}
	_, err = r.runCheckoutHelper(ctx, machine, services, isolatedGitCommand(installProtectedCheckoutScript,
		"strifira-install-checkout", "/workspace", checkout.WorkspacePath, repository, repositoryURL, checkout.WorkingBranch, runKey, stageKey))
	return err
}

// Credentialed fetch is separate from every writable-source installation.
func (r *DockerRuntime) PrepareCheckoutRepository(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, token string) error {
	repository, parsed, err := protectedCheckoutRepository(checkout)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("repository credential is unavailable")
	}
	runKey := "checkout-" + checkout.ID.String()
	proxy := "http://" + machine.RoutingKey + "-egress:3128"
	repositoryURL := strings.TrimSuffix(checkout.RepositoryURL, ".git")
	_, err = r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(prepareProtectedRepositoryScript,
		"strifira-prepare-checkout", repository, repositoryURL, checkout.BaseBranch, checkout.WorkingBranch, parsed.Host, checkout.RepositoryFullName, proxy, runKey), token, repositoryHelperInvocation{})
	return err
}

// Derive one recoverable staging name from Docker-held layout authority and the
// checkout identity. Workspace markers and glob matches never authorize cleanup.
// The same checkout keeps this name across cancellation, backend restart and
// successful retries; the layout record survives until its volume is removed.
func (r *DockerRuntime) checkoutStageKey(ctx context.Context, machine *domain.DevMachine, checkoutID uuid.UUID) (string, error) {
	if machine == nil || machine.ID == uuid.Nil || machine.WorkspaceVolumeName == nil || *machine.WorkspaceVolumeName == "" || checkoutID == uuid.Nil {
		return "", errors.New("checkout staging authority is unavailable")
	}
	inspection, err := r.client.ContainerInspect(ctx, workspaceLayoutName(machine), client.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	key, err := validateWorkspaceLayoutRecord(inspection.Container, machine, *machine.WorkspaceVolumeName)
	if err != nil {
		return "", err
	}
	stageKey := sha256.Sum256([]byte("strifira-checkout-stage-v1\x00" + key + "\x00" + checkoutID.String()))
	return hex.EncodeToString(stageKey[:]), nil
}

// The installer has no credential, no network, a read-only protected store and
// a writable workspace subpath. Existing worktrees retain their files, HEAD and
// exact index bytes. Only Git metadata is converted, never reset or checked out.
const installProtectedCheckoutScript = `set -eu
umask 077
workspace_root=$1
workspace_path=$2
repository_dir=$3
repository_url=$4
working_branch=$5
checkout_branch=$working_branch
run_key=$6
stage_key=$7
workspace_root=$(realpath -e "$workspace_root")
repository_dir=$(realpath -e "$repository_dir")
case "$workspace_path" in "$workspace_root"/tasks/*|"$workspace_root"/repository) ;; *) exit 1 ;; esac
relative=${workspace_path#"$workspace_root"/}
case "$relative" in *..*|*//*|*[!A-Za-z0-9/._-]*) exit 1 ;; esac
case "$relative" in tasks/*/*) exit 1 ;; esac
case "$run_key" in ''|*[!A-Za-z0-9-]*) exit 1 ;; esac
case "$stage_key" in *[!a-f0-9]*) exit 1 ;; esac
[ "${#stage_key}" -eq 64 ]
git check-ref-format "refs/heads/$working_branch"
case "$working_branch" in *[!A-Za-z0-9/._-]*) exit 1 ;; esac
case "$repository_url" in https://*) ;; *) exit 1 ;; esac
case "$repository_url" in *[!A-Za-z0-9/.:_-]*) exit 1 ;; esac
[ "$(cat "$repository_dir/strifira.repository-url")" = "$repository_url" ]
base=$(git --git-dir="$repository_dir" rev-parse --verify "refs/strifira/runs/$run_key/base^{commit}")
parent=$(git --git-dir="$repository_dir" rev-parse --verify "refs/strifira/runs/$run_key/parent^{commit}")
parent_directory=${workspace_path%/*}
if [ ! -e "$parent_directory" ] && [ ! -L "$parent_directory" ]; then mkdir -p "$parent_directory"; fi
[ "$(realpath -e "$parent_directory")" = "$parent_directory" ]
[ ! -L "$workspace_path" ]
locks="$workspace_root/.strifira-checkout-locks"
if [ ! -e "$locks" ] && [ ! -L "$locks" ]; then mkdir -m 0700 "$locks"; fi
[ -d "$locks" ] && [ ! -L "$locks" ] || exit 1
[ ! -L "$locks/$run_key" ]
exec 8>> "$locks/$run_key"
flock -w 30 8
# Only this checkout's exact backend-authorized name can be reclaimed. A killed
# installer can leave its stage, but an unrelated agent-writable glob is never
# removed. Keeping the stage on this volume also keeps final Git renames atomic.
stage="$workspace_root/.strifira-checkout-$stage_key"
if [ -e "$stage" ] || [ -L "$stage" ]; then
  [ -d "$stage" ] && [ ! -L "$stage" ] || exit 1
  [ "$(realpath -e "$stage")" = "$stage" ]
  rm -rf -- "$stage"
fi
mkdir -m 0700 "$stage"
index_lock=
head_lock=
branch_lock=
config_lock=
cleanup() {
  [ -z "$index_lock" ] || rm -f -- "$index_lock"
  [ -z "$head_lock" ] || rm -f -- "$head_lock"
  [ -z "$branch_lock" ] || rm -f -- "$branch_lock"
  [ -z "$config_lock" ] || rm -f -- "$config_lock"
  rm -rf -- "$stage"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
write_config() {
  destination=$1
  acquire_git_lock "$destination/config.lock"
  config_lock="$destination/config.lock"
  (
  set -C
  cat > "$stage/config.new" <<CONFIG
[core]
    repositoryformatversion = 0
    bare = false
    hooksPath = /dev/null
    fsmonitor = false
[gc]
    auto = 0
[credential]
    helper =
[http]
    followRedirects = false
[remote "origin"]
    url = $repository_url
    fetch = +refs/heads/*:refs/remotes/origin/*
[branch "$checkout_branch"]
    remote = origin
    merge = refs/heads/$checkout_branch
CONFIG
  )
  mv -T -f -- "$stage/config.new" "$destination/config"
  rm -f -- "$config_lock"
  config_lock=
}
acquire_git_lock() {
  candidate_lock=$1
  [ ! -L "$candidate_lock" ]
  if [ -f "$candidate_lock" ] && [ "$(cat "$candidate_lock")" = "strifira-checkout:$run_key:$stage_key" ]; then
    rm -f -- "$candidate_lock"
  fi
  (set -C; printf '%s\n' "strifira-checkout:$run_key:$stage_key" > "$candidate_lock")
}
source_git() {
  git --no-optional-locks -c core.hooksPath=/dev/null -c core.fsmonitor=false -c safe.directory="$workspace_path" -C "$workspace_path" "$@"
}
if [ ! -e "$workspace_path" ]; then
  git -c protocol.file.allow=always clone --quiet --no-checkout --local --shared --template= -- "$repository_dir" "$stage/new"
  write_config "$stage/new/.git"
  git --git-dir="$stage/new/.git" update-ref "refs/heads/$working_branch" "$parent"
  git --git-dir="$stage/new/.git" symbolic-ref HEAD "refs/heads/$checkout_branch"
  git --git-dir="$stage/new/.git" update-ref "refs/remotes/origin/$working_branch" "$parent"
  git --git-dir="$stage/new/.git" --work-tree="$stage/new" read-tree "$parent"
  git --git-dir="$stage/new/.git" --work-tree="$stage/new" checkout-index --all
  mv -T -n -- "$stage/new" "$workspace_path"
  [ ! -e "$stage/new" ] || { echo 'checkout path appeared during installation' >&2; exit 1; }
else
  [ -d "$workspace_path" ] && [ "$(realpath -e "$workspace_path")" = "$workspace_path" ] || exit 1
  # A killed linked-worktree conversion may have parked its pointer immediately
  # before installing the replacement directory. Restore that original pointer
  # before retrying. It never contains credentials or grants repository authority.
  backup="$workspace_path/.git.strifira-linked-backup"
  if [ ! -e "$workspace_path/.git" ] && [ ! -L "$workspace_path/.git" ]; then
    [ -f "$backup" ] && [ ! -L "$backup" ] || exit 1
    mv -T -n -- "$backup" "$workspace_path/.git"
    [ ! -e "$backup" ]
  fi
  [ ! -L "$workspace_path/.git" ]
  git_directory=$(source_git rev-parse --absolute-git-dir)
  git_directory=$(realpath -e "$git_directory")
  common_directory=$(source_git rev-parse --path-format=absolute --git-common-dir)
  common_directory=$(realpath -e "$common_directory")
  source_index=$(source_git rev-parse --path-format=absolute --git-path index)
  source_index=$(realpath -e "$source_index")
  case "$source_index" in "$git_directory"/*) ;; *) echo 'checkout index escapes its Git directory' >&2; exit 1 ;; esac
  source_branch=$(source_git symbolic-ref --short HEAD)
  if [ "$workspace_path" = "$workspace_root/repository" ]; then
    # Machine settings may now describe an issue checkout. Preserve the owner's
    # existing default branch while sanitizing or converting its Git metadata.
    git check-ref-format "refs/heads/$source_branch"
    case "$source_branch" in *[!A-Za-z0-9/._-]*) exit 1 ;; esac
    checkout_branch=$source_branch
  else
    [ "$source_branch" = "$working_branch" ] || { echo 'checkout working branch changed' >&2; exit 1; }
  fi
  if [ -d "$workspace_path/.git" ]; then
    [ "$git_directory" = "$workspace_path/.git" ] && [ "$common_directory" = "$git_directory" ] || exit 1
    write_config "$git_directory"
  else
    [ -f "$workspace_path/.git" ]
    # Only the old writable cache inside this same workspace can be converted.
    # Protected storage, symlinked caches and external Git directories are refused.
    case "$common_directory" in "$workspace_root"/repos/*.git) ;; *) echo 'linked repository escapes the legacy workspace cache' >&2; exit 1 ;; esac
    cache_name=${common_directory#"$workspace_root"/repos/}
    case "$cache_name" in */*) exit 1 ;; esac
    [ "$(realpath -e "$workspace_root/repos")" = "$workspace_root/repos" ]
    case "$git_directory" in "$common_directory"/worktrees/*) ;; *) exit 1 ;; esac
    worktree_name=${git_directory#"$common_directory"/worktrees/}
    case "$worktree_name" in ''|*/*) exit 1 ;; esac
    [ ! -e "$backup" ] && [ ! -L "$backup" ] || exit 1
    # Git's standard lock names stop concurrent index/HEAD/branch mutations.
    # A user's ordinary later file edits remain in place throughout conversion.
    candidate_lock="$source_index.lock"
    acquire_git_lock "$candidate_lock"
    index_lock=$candidate_lock
    candidate_lock="$git_directory/HEAD.lock"
    acquire_git_lock "$candidate_lock"
    head_lock=$candidate_lock
    candidate_lock="$common_directory/refs/heads/$checkout_branch.lock"
    mkdir -p "${candidate_lock%/*}"
    acquire_git_lock "$candidate_lock"
    branch_lock=$candidate_lock
    head=$(source_git rev-parse --verify 'HEAD^{commit}')
    index_hash=$(sha256sum "$source_index"); index_hash=${index_hash%% *}
    pointer_hash=$(sha256sum "$workspace_path/.git"); pointer_hash=${pointer_hash%% *}
    git -c protocol.file.allow=always clone --quiet --bare --local --shared --template= -- "$workspace_path" "$stage/snapshot"
    printf '%s\n' "$repository_dir/objects" >> "$stage/snapshot/objects/info/alternates"
    # Do not carry source config, hooks, credentials or other executable metadata
    # into the standalone checkout. Preserve only history and exact index bytes.
    cat > "$stage/snapshot/config" <<'CONFIG'
` + protectedRepositoryConfig + `CONFIG
    git -c init.defaultBranch=main init --quiet --bare --template= "$stage/git"
    mkdir -p "$stage/git/objects/info"
    printf '%s\n' "$repository_dir/objects" > "$stage/git/objects/info/alternates"
    # An exact copied index can reference staged blobs absent from HEAD. Import
    # every index stage too, including conflicts and symlinks; gitlinks belong
    # to separate submodule repositories and do not authorize object imports.
    source_git ls-files --format='%(objectmode) %(objectname)' > "$stage/index.entries"
    awk '$1 != "160000" {print $2}' "$stage/index.entries" > "$stage/index-objects"
    { printf '%s\n' "$head"; cat "$stage/index-objects"; printf '%s\n' "^$base" "^$parent"; } > "$stage/revisions"
    git --git-dir="$stage/snapshot" pack-objects --revs --stdout < "$stage/revisions" > "$stage/history.pack"
    git --git-dir="$stage/git" index-pack --stdin < "$stage/history.pack" > /dev/null
    git --git-dir="$stage/git" fsck --connectivity-only --no-dangling "$head" > /dev/null
    write_config "$stage/git"
    git --git-dir="$stage/git" update-ref "refs/heads/$checkout_branch" "$head"
    git --git-dir="$stage/git" symbolic-ref HEAD "refs/heads/$checkout_branch"
    git --git-dir="$stage/git" update-ref "refs/remotes/origin/$working_branch" "$parent"
    cp -p -- "$source_index" "$stage/git/index"
    git --git-dir="$stage/git" fsck --connectivity-only --no-dangling > /dev/null
    [ "$(source_git rev-parse --verify 'HEAD^{commit}')" = "$head" ]
    [ "$(source_git symbolic-ref --short HEAD)" = "$checkout_branch" ]
    current_index_hash=$(sha256sum "$source_index"); current_index_hash=${current_index_hash%% *}
    current_pointer_hash=$(sha256sum "$workspace_path/.git"); current_pointer_hash=${current_pointer_hash%% *}
    [ "$current_index_hash" = "$index_hash" ] && [ "$current_pointer_hash" = "$pointer_hash" ] || exit 1
    sync -f "$workspace_root"
    mv -T -n -- "$workspace_path/.git" "$backup"
    [ ! -e "$workspace_path/.git" ]
    mv -T -n -- "$stage/git" "$workspace_path/.git"
    [ ! -e "$stage/git" ]
    sync -f "$workspace_root"
    rm -f -- "$backup"
  fi
  if [ -e "$backup" ] || [ -L "$backup" ]; then
    [ -f "$backup" ] && [ ! -L "$backup" ] || exit 1
    IFS= read -r old_pointer < "$backup"
    case "$old_pointer" in "gitdir: $workspace_root"/repos/*.git/worktrees/*) ;; *) exit 1 ;; esac
    old_git_directory=${old_pointer#gitdir: }
    old_git_directory=$(realpath -e "$old_git_directory")
    old_common_directory=$(git -c core.hooksPath=/dev/null -c core.fsmonitor=false --git-dir="$old_git_directory" rev-parse --path-format=absolute --git-common-dir)
    old_common_directory=$(realpath -e "$old_common_directory")
    case "$old_common_directory" in "$workspace_root"/repos/*.git) ;; *) exit 1 ;; esac
    old_cache_name=${old_common_directory#"$workspace_root"/repos/}
    case "$old_cache_name" in */*) exit 1 ;; esac
    case "$old_git_directory" in "$old_common_directory"/worktrees/*) ;; *) exit 1 ;; esac
    # The installed standalone checkout may have newer commits, staged changes
    # or file edits. Verify its history and index instead of comparing them with
    # the parked legacy pointer. Neither history nor later work is reset.
    old_head=$(git -c core.hooksPath=/dev/null -c core.fsmonitor=false --git-dir="$old_git_directory" rev-parse --verify 'HEAD^{commit}')
    git --git-dir="$workspace_path/.git" cat-file -e "$old_head^{commit}"
    git --git-dir="$workspace_path/.git" fsck --connectivity-only --no-dangling > /dev/null
    old_branch=$(git --git-dir="$old_git_directory" symbolic-ref --short HEAD)
    git check-ref-format "refs/heads/$old_branch"
    for old_lock in "$old_git_directory/index.lock" "$old_git_directory/HEAD.lock" "$old_common_directory/refs/heads/$old_branch.lock"; do
      [ ! -L "$old_lock" ]
      if [ -f "$old_lock" ] && [ "$(cat "$old_lock")" = "strifira-checkout:$run_key:$stage_key" ]; then rm -f -- "$old_lock"; fi
    done
    rm -f -- "$backup"
  fi
fi
git --git-dir="$workspace_path/.git" update-ref "refs/remotes/origin/${working_branch}" "$parent"
git --git-dir="$workspace_path/.git" fsck --connectivity-only --no-dangling > /dev/null
`
