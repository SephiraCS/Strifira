package machine

import (
	"errors"
	"strings"
)

// RepositoryCandidate identifies retained objects in the protected, disk-backed
// repository. Publication never reconstructs a candidate from a live checkout.
type RepositoryCandidate struct {
	GitState
	Commit          string
	BaseCommit      string
	SourceCommit    string
	SourceIndexHash string
}

// Only the bounded Git helper can write this repository. Its configuration and
// objects are independent of the IDE's writable Git metadata. These scripts are
// run with isolatedGitCommand; authentication never consumes source config.
const protectedRepositoryConfig = `
[core]
    repositoryformatversion = 0
    bare = true
    hooksPath = /dev/null
    fsmonitor = false
[gc]
    auto = 0
[credential]
    helper =
[http]
    followRedirects = false
[protocol]
    allow = never
[protocol "https"]
    allow = always
[pack]
    threads = 1
    windowMemory = 32m
    window = 0
`

const protectedRepositoryFunctions = `set -eu
umask 077
repository_git() {
  git --git-dir="$repository_dir" "$@"
}
read_repository_credential() {
  IFS= read -r GITHUB_TOKEN
  [ -n "$GITHUB_TOKEN" ]
  export GITHUB_TOKEN KU_GITHUB_HOST="$expected_host" KU_GIT_REPOSITORY="$expected_repository"
}
credential_helper='!f() {
protocol=; host=; path=
while IFS= read -r line && [ -n "$line" ]; do
  case "$line" in protocol=*) protocol=${line#protocol=} ;; host=*) host=${line#host=} ;; path=*) path=${line#path=} ;; esac
done
if [ "$protocol" = https ] && [ "$host" = "$KU_GITHUB_HOST" ] && [ "${path%.git}" = "$KU_GIT_REPOSITORY" ]; then
  printf "%s\n" username=x-access-token "password=$GITHUB_TOKEN"
fi
}; f'
authenticated_git() {
  repository_git -c credential.helper="$credential_helper" -c credential.useHttpPath=true -c http.proxy="$proxy" "$@"
}
`

// Fetch only the selected base/working refs. The backend derives all arguments
// from the authorized repository, rather than source remotes or provider output.
// A run's base ref is pinned once, before any agent process starts.
const prepareProtectedRepositoryScript = protectedRepositoryFunctions + `
repository_dir=$1
repository_url=$2
base_branch=$3
working_branch=$4
expected_host=$5
expected_repository=$6
proxy=$7
run_key=$8
mkdir -p "$repository_dir"
cd "$repository_dir"
exec 9> "$repository_dir/ku.lock"
flock -w 30 9
[ ! -e "$repository_dir/ku-runs/$run_key/terminal-cleanup" ] || { echo 'agent run is terminal' >&2; exit 1; }
if [ ! -f "$repository_dir/HEAD" ]; then
  git -c init.defaultBranch=main init --quiet --bare --template= "$repository_dir"
fi
` + `cat > "$repository_dir/config" <<'CONFIG'
` + protectedRepositoryConfig + `CONFIG
git check-ref-format "refs/heads/$base_branch"
git check-ref-format "refs/heads/$working_branch"
case "$run_key" in ''|*[!A-Za-z0-9-]*) exit 1 ;; esac
if [ -f "$repository_dir/ku.repository-url" ]; then
  [ "$(cat "$repository_dir/ku.repository-url")" = "$repository_url" ]
else
  printf '%s\n' "$repository_url" > "$repository_dir/ku.repository-url"
fi
if repository_git show-ref --verify --quiet "refs/ku/runs/$run_key/base"; then
  repository_git show-ref --verify --quiet "refs/ku/runs/$run_key/parent"
  exit 0
fi
read_repository_credential
refs=$(authenticated_git ls-remote --heads -- "$repository_url" "refs/heads/$base_branch" "refs/heads/$working_branch")
if printf '%s\n' "$refs" | awk -v ref="refs/heads/$working_branch" '$2==ref { found=1 } END { exit !found }'; then
  authenticated_git fetch --quiet --no-tags -- "$repository_url" "+refs/heads/$base_branch:refs/remotes/ku/base" "+refs/heads/$working_branch:refs/remotes/ku/working"
else
  authenticated_git fetch --quiet --no-tags -- "$repository_url" "+refs/heads/$base_branch:refs/remotes/ku/base"
  repository_git update-ref -d refs/remotes/ku/working
fi
unset GITHUB_TOKEN KU_GITHUB_HOST KU_GIT_REPOSITORY
base=$(repository_git rev-parse --verify 'refs/remotes/ku/base^{commit}')
if repository_git show-ref --verify --quiet refs/remotes/ku/working; then
  parent=$(repository_git rev-parse --verify 'refs/remotes/ku/working^{commit}')
else
  parent=$base
fi
printf 'start\ncreate refs/ku/runs/%s/base %s\ncreate refs/ku/runs/%s/parent %s\nprepare\ncommit\n' "$run_key" "$base" "$run_key" "$parent" | repository_git update-ref --stdin > /dev/null
`

// Snapshot history uses local alternates while staging, then imports only objects
// absent from the trusted base/parent. No full-history copy or tmpfs clone is
// required. Authentication occurs later, after candidate objects are retained
// without any writable-source alternates in the protected repository.
const retainRepositoryCandidateScript = protectedRepositoryFunctions + `
workspace_path=$1
repository_dir=$2
run_key=$3
case "$run_key" in ''|*[!A-Za-z0-9-]*) exit 1 ;; esac
cd "$repository_dir"
exec 9> "$repository_dir/ku.lock"
flock -w 30 9
[ ! -e "$repository_dir/ku-runs/$run_key/terminal-cleanup" ] || { echo 'agent run is terminal' >&2; exit 1; }
base=$(repository_git rev-parse --verify "refs/ku/runs/$run_key/base^{commit}")
parent=$(repository_git rev-parse --verify "refs/ku/runs/$run_key/parent^{commit}")
run_dir="$repository_dir/ku-runs/$run_key"
if [ -f "$run_dir/candidate.info" ]; then
  cat "$run_dir/candidate.info"
  exit 0
fi
mkdir -p "$run_dir"
snapshot_dir=$(mktemp -d "$run_dir/snapshot.XXXXXXXX")
trap 'rm -rf "$snapshot_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# Refuse Git metadata outside this checkout. All new checkouts are standalone
# local clones of the protected store; an agent cannot relink one to that store.
source_git() {
  git -c core.hooksPath=/dev/null -c core.fsmonitor=false -c safe.directory="$workspace_path" -C "$workspace_path" "$@"
}
source_git_dir=$(source_git rev-parse --absolute-git-dir)
source_git_dir=$(realpath -e "$source_git_dir")
case "$source_git_dir" in "$workspace_path"/*) ;; *) echo 'checkout Git directory is outside its workspace' >&2; exit 1 ;; esac
source_common_dir=$(source_git rev-parse --path-format=absolute --git-common-dir)
source_common_dir=$(realpath -e "$source_common_dir")
[ "$source_common_dir" = "$source_git_dir" ] || { echo 'checkout common Git directory is outside its standalone Git directory' >&2; exit 1; }
source_index=$(source_git rev-parse --path-format=absolute --git-path index)
source_index=$(realpath -e "$source_index")
case "$source_index" in "$source_git_dir"/*) ;; *) echo 'checkout index is outside its Git directory' >&2; exit 1 ;; esac
source_index_hash=$(sha256sum "$source_index")
source_index_hash=${source_index_hash%% *}
git -c protocol.file.allow=always clone --quiet --bare --local --shared --template= -- "$workspace_path" "$snapshot_dir/repository"
snapshot_git_dir="$snapshot_dir/repository"
` + `cat > "$snapshot_git_dir/config" <<'CONFIG'
` + protectedRepositoryConfig + `CONFIG
printf '%s\n' "$repository_dir/objects" >> "$snapshot_git_dir/objects/info/alternates"
snapshot_git() {
  git -C "$workspace_path" --git-dir="$snapshot_git_dir" --work-tree="$workspace_path" -c core.bare=false "$@"
}
branch=$(snapshot_git symbolic-ref --short HEAD)
source_commit=$(snapshot_git rev-parse --verify 'HEAD^{commit}')
snapshot_git read-tree HEAD
snapshot_git add --all -- .
dirty=false
if ! snapshot_git diff --cached --quiet --no-ext-diff --no-textconv HEAD --; then dirty=true; fi
tree=$(snapshot_git write-tree)
candidate=$source_commit
if [ "$dirty" = true ]; then
  candidate=$(printf '%s\n' 'Complete delegated task' | snapshot_git -c user.name='Ku Agent' -c user.email='ku-agent@localhost' commit-tree "$tree" -p "$source_commit")
fi
# Pack the candidate before any credential is read, and validate its complete
# connectivity using only retained, protected objects. The volume's hard quota
# accounts for both the temporary pack and its final imported objects.
printf '%s\n' "$candidate" "^$base" "^$parent" | snapshot_git pack-objects --revs --stdout > "$snapshot_dir/candidate.pack"
repository_git index-pack --stdin < "$snapshot_dir/candidate.pack" > /dev/null
repository_git fsck --connectivity-only --no-dangling "$candidate" > /dev/null
repository_git update-ref "refs/ku/runs/$run_key/candidate" "$candidate"
merge_base=$(repository_git merge-base "$base" "$candidate")
commits=$(repository_git rev-list "$base..$candidate")
printf '%s\0%s\0%s\0%s\0%s\0%s\0%s\0' "$branch" "$commits" "$dirty" "$candidate" "$base" "$source_commit" "$source_index_hash" > "$snapshot_dir/candidate.info"
repository_git diff --no-ext-diff --no-textconv --no-renames --name-only -z "$merge_base" "$candidate" -- >> "$snapshot_dir/candidate.info"
mv "$snapshot_dir/candidate.info" "$run_dir/candidate.info"
cat "$run_dir/candidate.info"
`

// Publication uses the already verified candidate ref and leaves it retained
// after either success or failure. Source changes cannot alter the push payload.
const publishRetainedRepositoryCandidateScript = protectedRepositoryFunctions + `
repository_dir=$1
repository_url=$2
working_branch=$3
expected_host=$4
expected_repository=$5
proxy=$6
run_key=$7
expected_commit=$8
case "$run_key" in ''|*[!A-Za-z0-9-]*) exit 1 ;; esac
cd "$repository_dir"
exec 9> "$repository_dir/ku.lock"
flock -w 30 9
[ ! -e "$repository_dir/ku-runs/$run_key/terminal-cleanup" ] || { echo 'agent run is terminal' >&2; exit 1; }
[ "$(cat "$repository_dir/ku.repository-url")" = "$repository_url" ]
git check-ref-format "refs/heads/$working_branch"
candidate=$(repository_git rev-parse --verify "refs/ku/runs/$run_key/candidate^{commit}")
[ "$candidate" = "$expected_commit" ]
repository_git fsck --connectivity-only --no-dangling "$candidate" > /dev/null
read_repository_credential
authenticated_git push --quiet -- "$repository_url" "$candidate:refs/heads/$working_branch"
unset GITHUB_TOKEN KU_GITHUB_HOST KU_GIT_REPOSITORY
repository_git update-ref refs/remotes/ku/working "$candidate"
`

func parseRepositoryCandidate(output string) (RepositoryCandidate, error) {
	parts := strings.SplitN(output, "\x00", 8)
	if len(parts) != 8 {
		return RepositoryCandidate{}, errors.New("retained Git candidate metadata is incomplete")
	}
	state, err := parseGitSnapshot(strings.Join(parts[:3], "\x00") + "\x00" + parts[7])
	if err != nil {
		return RepositoryCandidate{}, err
	}
	if !validGitObjectID(parts[3]) || !validGitObjectID(parts[4]) || !validGitObjectID(parts[5]) || len(parts[6]) != 64 || !validGitObjectID(parts[6]) {
		return RepositoryCandidate{}, errors.New("retained Git candidate object or index identity is invalid")
	}
	return RepositoryCandidate{GitState: state, Commit: parts[3], BaseCommit: parts[4], SourceCommit: parts[5], SourceIndexHash: parts[6]}, nil
}
