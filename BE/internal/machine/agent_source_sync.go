package machine

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
)

type RepositorySourceSync struct {
	Status string
	Reason string
}

func (r *DockerRuntime) SynchronizeAgentCandidate(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, runID uuid.UUID, candidate RepositoryCandidate) (RepositorySourceSync, error) {
	repository, _, err := authorizedRunRepository(machine, checkout, runID)
	if err != nil {
		return RepositorySourceSync{}, err
	}
	if candidate.Branch != checkout.WorkingBranch || !validGitObjectID(candidate.Commit) || !validGitObjectID(candidate.SourceCommit) || len(candidate.SourceIndexHash) != 64 || !validGitObjectID(candidate.SourceIndexHash) {
		return RepositorySourceSync{}, errors.New("source synchronization requires the selected retained candidate and source identities")
	}
	stageKey, err := r.checkoutStageKey(ctx, machine, runID)
	if err != nil {
		return RepositorySourceSync{}, err
	}
	output, err := r.runImmutableGitHelper(ctx, machine, services, isolatedGitCommand(synchronizeRepositoryCandidateScript,
		"ku-sync-agent-source", checkout.WorkspacePath, repository, runID.String(), stageKey, candidate.Branch, candidate.Commit, candidate.SourceCommit, candidate.SourceIndexHash), "", repositoryHelperInvocation{RunID: runID, Checkout: true, SourceSync: true})
	if err != nil {
		return RepositorySourceSync{}, err
	}
	status, reason, _ := strings.Cut(strings.TrimSpace(output), "\n")
	if status != "updated" && status != "unchanged" && status != "conflict" || status == "conflict" && reason == "" || status != "conflict" && reason != "" {
		return RepositorySourceSync{}, errors.New("source synchronization returned an invalid result")
	}
	return RepositorySourceSync{Status: status, Reason: reason}, nil
}

// The helper writes only Git metadata, never worktree files. Git's standard
// locks protect HEAD, index and the selected branch. A durable transaction in
// the exact layout-authorized stage permits recovery between the two renames.
// The deterministic Docker reservation must be held before reclaiming locks.
const synchronizeRepositoryCandidateScript = atomicVolumeRenameFunctions + `set -eu
umask 077
workspace_path=$1
repository_dir=$2
run_key=$3
stage_key=$4
working_branch=$5
candidate=$6
source_commit=$7
source_index_hash=$8
recovery_only=${9:-false}
case "$recovery_only" in true|false) ;; *) exit 1 ;; esac
case "$run_key" in ''|*[!A-Za-z0-9-]*) exit 1 ;; esac
case "$stage_key" in *[!a-f0-9]*) exit 1 ;; esac
[ "${#stage_key}" -eq 64 ]
git check-ref-format "refs/heads/$working_branch"
[ "$(realpath -e "$workspace_path")" = "$workspace_path" ]
[ "$(realpath -e "$repository_dir")" = "$repository_dir" ]
exec 9> "$repository_dir/ku.lock"
flock -w 30 9
stage="$repository_dir/ku-runs/$run_key/source-sync-$stage_key"
[ ! -L "$stage" ]
if [ "$recovery_only" = true ]; then
  [ -f "$repository_dir/ku-runs/$run_key/terminal-cleanup" ]
  result_file="$repository_dir/ku-runs/$run_key/cleanup.source-result"
  [ ! -L "$result_file" ]
  if [ ! -e "$stage" ]; then
    if [ -f "$result_file" ]; then cat "$result_file"; else printf 'unchanged\n'; fi
    exit 0
  fi
  [ -d "$stage" ] && [ "$(realpath -e "$stage")" = "$stage" ]
  [ ! -L "$stage/transaction" ]
  if [ ! -e "$stage/transaction" ]; then rm -rf -- "$stage"; printf 'unchanged\n'; exit 0; fi
  [ -f "$stage/transaction" ]
  [ "$(cat "$stage/transaction")" = "ku-source-sync:$run_key:$stage_key:$working_branch:$candidate:$source_commit:$source_index_hash" ] || exit 1
else
  [ ! -e "$repository_dir/ku-runs/$run_key/terminal-cleanup" ] || { echo 'agent run is terminal' >&2; exit 1; }
fi
[ "$(git --git-dir="$repository_dir" rev-parse --verify "refs/ku/runs/$run_key/candidate^{commit}")" = "$candidate" ]
git_dir="$workspace_path/.git"
[ -d "$git_dir" ] && [ ! -L "$git_dir" ]
[ "$(realpath -e "$git_dir")" = "$git_dir" ]
for file in HEAD index packed-refs; do [ ! -L "$git_dir/$file" ]; done
source_git() {
  git --no-optional-locks --git-dir="$git_dir" -c core.hooksPath=/dev/null -c core.fsmonitor=false "$@"
}
[ "$(source_git rev-parse --path-format=absolute --git-common-dir)" = "$git_dir" ]
ref_storage=$(source_git config --get extensions.refStorage || true)
case "$ref_storage" in ''|files) ;; *) echo 'source synchronization requires file-based Git refs' >&2; exit 1 ;; esac
conflict() {
  if [ "$recovery_only" = true ]; then
    [ ! -L "$result_file.new" ]
    printf 'conflict\n%s\n' "$1" > "$result_file.new"
    mv -T -f -- "$result_file.new" "$result_file"
    sync -f "$repository_dir"
  fi
  printf 'conflict\n%s\n' "$1"; exit 0
}
if [ "$candidate" = "$source_commit" ]; then printf 'unchanged\n'; exit 0; fi
# Create ref parents only after validating each existing ancestor. Never follow
# a source-provided symlink into another checkout or the protected store.
ref_parent="$git_dir"
relative="refs/heads/$working_branch"
while [ "${relative#*/}" != "$relative" ]; do
  segment=${relative%%/*}; relative=${relative#*/}
  case "$segment" in ''|*[!A-Za-z0-9._-]*) exit 1 ;; esac
  ref_parent="$ref_parent/$segment"
  [ ! -L "$ref_parent" ]
  if [ ! -e "$ref_parent" ]; then mkdir "$ref_parent"; fi
  [ -d "$ref_parent" ] && [ "$(realpath -e "$ref_parent")" = "$ref_parent" ]
done
branch_ref="$git_dir/refs/heads/$working_branch"
[ ! -L "$branch_ref" ]
stage="$repository_dir/ku-runs/$run_key/source-sync-$stage_key"
[ ! -L "$stage" ]
if [ ! -e "$stage" ]; then mkdir -m 0700 "$stage"; fi
[ -d "$stage" ] && [ "$(realpath -e "$stage")" = "$stage" ]
marker="ku-source-sync:$run_key:$stage_key"
transaction="$marker:$working_branch:$candidate:$source_commit:$source_index_hash"
recovering=false
if [ -e "$stage/transaction" ]; then
  [ ! -L "$stage/transaction" ] && [ -f "$stage/transaction" ]
  [ "$(cat "$stage/transaction")" = "$transaction" ] || conflict 'source synchronization transaction changed'
  recovering=true
fi
if [ "$recovering" = false ]; then
  [ ! -L "$stage/transaction.new" ]
  printf '%s\n' "$transaction" > "$stage/transaction.new"
  mv -T -f -- "$stage/transaction.new" "$stage/transaction"
  sync -f "$workspace_path"
fi
head_locked=false
index_locked=false
branch_locked=false
recovery_required=false
cleanup() {
  for entry in "$git_dir/HEAD.lock:$head_locked" "$git_dir/index.lock:$index_locked" "$branch_ref.lock:$branch_locked"; do
    locked=${entry##*:}; lock=${entry%:*}
    if { [ "$locked" = true ] || [ "$recovering" = true ]; } && [ ! -L "$lock" ] && [ -f "$lock" ] && [ "$(cat "$lock")" = "$marker" ]; then rm -f -- "$lock"; fi
  done
  if [ "$recovery_required" = false ]; then rm -rf -- "$stage"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
[ "$(cat "$git_dir/HEAD")" = "ref: refs/heads/$working_branch" ] || conflict 'source HEAD changed after candidate retention'
acquire_lock() {
  lock=$1
  [ ! -L "$lock" ] || conflict 'source Git lock is a symlink'
  if [ -e "$lock" ]; then
    if [ "$recovering" = true ] && [ -f "$lock" ] && [ "$(cat "$lock")" = "$marker" ]; then rm -f -- "$lock";
    else conflict 'source Git locks are held by another operation'; fi
  fi
  (set -C; printf '%s\n' "$marker" > "$lock") || conflict 'source Git lock appeared during synchronization'
}
acquire_lock "$git_dir/HEAD.lock"; head_locked=true
acquire_lock "$git_dir/index.lock"; index_locked=true
acquire_lock "$branch_ref.lock"; branch_locked=true
[ "$(cat "$git_dir/HEAD")" = "ref: refs/heads/$working_branch" ] || conflict 'source HEAD changed after candidate retention'
head=$(source_git rev-parse --verify "refs/heads/$working_branch^{commit}")
index_hash=$(sha256sum "$git_dir/index"); index_hash=${index_hash%% *}
# A fresh index is constructed through protected objects/configuration. Source
# hooks, filters and index extensions cannot affect the intended index bytes.
for file in new-index new-index.lock new-ref transaction.new; do [ ! -L "$stage/$file" ]; rm -f -- "$stage/$file"; done
GIT_INDEX_FILE="$stage/new-index" git --git-dir="$repository_dir" read-tree "$candidate"
new_index_hash=$(sha256sum "$stage/new-index"); new_index_hash=${new_index_hash%% *}
if [ "$head" = "$candidate" ]; then
  if [ "$index_hash" = "$new_index_hash" ]; then printf 'unchanged\n'; exit 0; fi
  [ "$recovering" = true ] || conflict 'source branch changed after candidate retention'
  [ "$index_hash" = "$source_index_hash" ] || conflict 'source index changed during synchronization recovery'
elif [ "$head" = "$source_commit" ]; then
  [ "$index_hash" = "$source_index_hash" ] || conflict 'source index changed after candidate retention'
else
  conflict 'source branch changed after candidate retention'
fi
# Import only candidate objects absent from the source commit. All Git commands
# producing objects use trusted configuration, never source fetch/filter config.
[ ! -L "$git_dir/objects" ] && [ "$(realpath -e "$git_dir/objects")" = "$git_dir/objects" ]
[ ! -L "$git_dir/objects/pack" ]
[ ! -L "$stage/import" ]
rm -rf -- "$stage/import"
git init --quiet --bare --template= "$stage/import"
cat > "$stage/import/config" <<'CONFIG'
` + protectedRepositoryConfig + `CONFIG
[ ! -L "$stage/candidate.pack" ]
printf '%s\n' "$candidate" "^$source_commit" | git --git-dir="$repository_dir" pack-objects --revs --stdout > "$stage/candidate.pack"
GIT_OBJECT_DIRECTORY="$git_dir/objects" git --git-dir="$stage/import" index-pack --stdin < "$stage/candidate.pack" > /dev/null
source_git cat-file -e "$candidate^{commit}"
printf '%s\n' "$transaction" > "$stage/transaction.new"
mv -T -f -- "$stage/transaction.new" "$stage/transaction"
sync -f "$workspace_path"
# Keep the durable transaction if either metadata rename fails or a signal
# arrives between them. Even a normal EXIT trap must not erase recovery data.
recovery_required=true
if [ "$head" = "$source_commit" ]; then
  printf '%s\n' "$candidate" > "$stage/new-ref"
  atomic_volume_move -f "$stage/new-ref" "$branch_ref"
  sync -f "$workspace_path"
fi
atomic_volume_move -f "$stage/new-index" "$git_dir/index"
sync -f "$workspace_path"
recovery_required=false
printf 'updated\n'
`
