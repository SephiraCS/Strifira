package machine

import (
	"context"
	"errors"
	"strings"

	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/client"
)

func validateRepositoryProvision(machine *domain.DevMachine, provision *RepositoryProvision) error {
	if machine == nil {
		return errors.New("repository provisioning machine is unavailable")
	}
	if provision == nil {
		if machine.RepoOwner != "" || machine.RepoName != "" {
			return errors.New("selected repository requires backend provisioning")
		}
		return nil
	}
	checkout := provision.Checkout
	if checkout == nil || checkout.ID != machine.ID || checkout.MachineID != machine.ID || checkout.WorkspaceID != machine.WorkspaceID || checkout.WorkspacePath != "/workspace/repository" || checkout.RepositoryFullName != checkoutRepositoryFullName(machine, nil) || machine.RepoURL == "" || strings.TrimSuffix(checkout.RepositoryURL, ".git") != strings.TrimSuffix(machine.RepoURL, ".git") || checkout.BaseBranch != machine.BaseBranch || checkout.WorkingBranch != machine.WorkingBranch || provision.Token == "" || strings.ContainsAny(provision.Token, "\r\n\x00") {
		return errors.New("default repository provisioning authority is unavailable")
	}
	_, _, err := protectedCheckoutRepository(checkout)
	return err
}

func (r *DockerRuntime) provisionDefaultRepository(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, provision *RepositoryProvision) error {
	if err := validateRepositoryProvision(machine, provision); err != nil {
		return err
	}
	checkout := provision.Checkout
	repository, _, err := protectedCheckoutRepository(checkout)
	if err != nil {
		return err
	}
	stageKey, err := r.checkoutStageKey(ctx, machine, checkout.ID)
	if err != nil {
		return err
	}
	needsMigration, err := r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(`set -eu
root=$1; journal="$2/ku-default-checkouts/$3"
if [ -e "$root/.git" ] || [ -L "$root/.git" ] || { [ -d "$journal" ] && [ ! -f "$journal/completed" ]; }; then printf 'relocate\n'; fi
`, "ku-probe-default-checkout", "/workspace", repository, checkout.ID.String()), "", repositoryHelperInvocation{})
	if err != nil {
		return err
	}
	if needsMigration != "" {
		if needsMigration != "relocate\n" {
			return errors.New("default repository migration probe returned an invalid result")
		}
		if err := r.requireStoppedDefaultWorkspaceWriters(ctx, machine); err != nil {
			return err
		}
	}
	// PrepareCheckout's fetch receives credentials in a separate read-only-source
	// helper. Root migration itself always has an empty environment and no network.
	if err := r.PrepareCheckoutRepository(ctx, machine, services, checkout, provision.Token); err != nil {
		return err
	}
	_, err = r.runImmutableGitHelper(ctx, machine, services, isolatedGitCommand(migrateLegacyRootCheckoutScript, "ku-migrate-default-checkout", "/workspace", checkout.WorkspacePath, repository, checkout.ID.String(), stageKey), "", repositoryHelperInvocation{Checkout: true, ProvisionID: checkout.ID})
	if err != nil {
		return err
	}
	_, err = r.runCheckoutHelper(ctx, machine, services, isolatedGitCommand(installProtectedCheckoutScript, "ku-install-default-checkout", "/workspace", checkout.WorkspacePath, repository, checkout.RepositoryURL, checkout.WorkingBranch, "checkout-"+checkout.ID.String(), stageKey))
	return err
}

func (r *DockerRuntime) requireStoppedDefaultWorkspaceWriters(ctx context.Context, machine *domain.DevMachine) error {
	listed, err := r.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, item := range listed.Items {
		usesVolume := false
		for _, mounted := range item.Mounts {
			if mounted.Name == *machine.WorkspaceVolumeName && mounted.RW {
				usesVolume = true
				break
			}
		}
		if !usesVolume {
			continue
		}
		inspection, err := r.client.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if inspection.Container.HostConfig == nil || inspection.Container.State == nil {
			return errors.New("default migration workspace consumer is unavailable")
		}
		for _, mounted := range inspection.Container.HostConfig.Mounts {
			if mounted.Source != *machine.WorkspaceVolumeName || mounted.ReadOnly {
				continue
			}
			if mounted.VolumeOptions != nil && mounted.VolumeOptions.Subpath != "" && mounted.VolumeOptions.Subpath != "workspace" {
				continue
			}
			state := inspection.Container.State
			if state.Running || state.Paused || state.Restarting {
				return errors.New("stop the machine and its agents before relocating the legacy root checkout")
			}
		}
	}
	return nil
}

// The journal is in backend-only storage. Source filenames are stored as raw
// records, so tabs/newlines and symlinks retain their exact names and inodes.
// No move overwrites a destination, and SIGKILL leaves a recoverable journal.
const migrateLegacyRootCheckoutScript = atomicVolumeRenameFunctions + `set -eu
umask 077
root=$1
destination=$2
repository=$3
checkout_id=$4
stage_key=$5
[ "$(realpath -e "$root")" = "$root" ]
[ "$destination" = "$root/repository" ]
[ "$(realpath -e "$repository")" = "$repository" ]
case "$checkout_id" in ''|*[!a-f0-9-]*) exit 1 ;; esac
[ "${#checkout_id}" -eq 36 ]
case "$stage_key" in *[!a-f0-9]*) exit 1 ;; esac
[ "${#stage_key}" -eq 64 ]
exec 9> "$repository/ku.lock"
flock -w 30 9
journal_parent="$repository/ku-default-checkouts"
[ ! -L "$journal_parent" ]
if [ ! -e "$journal_parent" ]; then mkdir -m 0700 "$journal_parent"; fi
[ -d "$journal_parent" ]
journal="$journal_parent/$checkout_id"
stage="$root/.ku-root-$stage_key"
[ ! -L "$journal" ]
read_raw() { value=$(cat "$1"; printf .); value=${value%.}; }
inode() { stat -c '%d:%i' -- "$1"; }
exists() { [ -e "$1" ] || [ -L "$1" ]; }
entry_key() { digest=$(printf '%s' "$1" | sha256sum); printf '%s' "${digest%% *}"; }
reserved() {
 case "$1" in tasks|repos|cache|.cache|.ku-ready|.ku-checkout-locks|.ku-root-"$stage_key") return 0 ;; esac
 case "$1" in .ku-checkout-*) suffix=${1#.ku-checkout-}; case "$suffix" in *[!a-f0-9]*) return 1 ;; esac; [ "${#suffix}" -eq 64 ] && return 0 ;; esac
 return 1
}
if ! exists "$journal"; then
 if ! exists "$root/.git"; then exit 0; fi
 [ ! -L "$root/.git" ]
 if exists "$destination" || exists "$stage"; then echo 'default checkout path collides with existing workspace data' >&2; exit 1; fi
 source_git() { git --no-optional-locks -c core.hooksPath=/dev/null -c core.fsmonitor=false -c safe.directory="$root" -C "$root" "$@"; }
 # Infrastructure names cannot be split automatically when they are tracked by
 # the root repository. Leave everything in place and explain the collision.
 source_git ls-files -z -- tasks repos cache .cache repository .ku-ready .ku-checkout-locks '.ku-root-*' '.ku-checkout-*' > /tmp/ku-reserved-paths
 [ ! -s /tmp/ku-reserved-paths ] || { echo 'root repository tracks reserved workspace paths' >&2; exit 1; }
 git_dir=$(source_git rev-parse --absolute-git-dir); git_dir=$(realpath -e "$git_dir")
 if [ -d "$root/.git" ]; then
  [ "$git_dir" = "$root/.git" ]
  common=$(source_git rev-parse --path-format=absolute --git-common-dir); [ "$(realpath -e "$common")" = "$git_dir" ]
 else
  case "$git_dir" in "$root"/repos/*.git/worktrees/*) ;; *) echo 'root Git pointer escapes its workspace cache' >&2; exit 1 ;; esac
 fi
 # Do not create a committed journal until every linked pointer is validated.
 pending="$journal_parent/$checkout_id.pending"
 [ ! -L "$pending" ]
 if exists "$pending"; then [ -d "$pending" ]; rm -rf -- "$pending"; fi
 mkdir -m 0700 "$pending" "$pending/entries" "$pending/pointers"
 counter=0
 for admin in "$root/.git/worktrees"/*; do
  exists "$admin" || continue
  [ -d "$admin" ] && [ ! -L "$admin" ]
  [ "$(realpath -e "$admin")" = "$admin" ]
  pointer=$(cat "$admin/gitdir")
  printf '%s\n' "$pointer" | cmp -s - "$admin/gitdir"
  case "$pointer" in "$root"/tasks/*/.git) ;; *) echo 'linked worktree pointer is outside issue checkouts' >&2; exit 1 ;; esac
  segment=${pointer#"$root/tasks/"}; segment=${segment%/.git}
  case "$segment" in ''|*/*|.|..|*[!A-Za-z0-9._-]*) exit 1 ;; esac
  [ "$(realpath -e "${pointer%/.git}")" = "${pointer%/.git}" ] && [ ! -L "$pointer" ] && [ -f "$pointer" ]
  read_raw "$pointer"; old=$value
  # Git's pointer file has one trailing newline; command substitution removes
  # just that newline for comparison while names remain raw records above.
  old_line=$(cat "$pointer")
  [ "$old_line" = "gitdir: $admin" ]
  counter=$((counter+1)); record="$pending/pointers/$counter"; mkdir "$record"
  printf '%s' "$pointer" > "$record/path"
  cp -p -- "$pointer" "$record/old"
  printf 'gitdir: %s/.git/worktrees/%s\n' "$destination" "${admin##*/}" > "$record/new"
  inode "$pointer" > "$record/inode"
 done
 if [ -f "$root/.git" ]; then
  counter=$((counter+1)); record="$pending/pointers/$counter"; mkdir "$record"
  printf '%s' "$git_dir/gitdir" > "$record/path"
  cp -p -- "$git_dir/gitdir" "$record/old"
  [ "$(cat "$git_dir/gitdir")" = "$root/.git" ]
  printf '%s\n' "$destination/.git" > "$record/new"
  inode "$git_dir/gitdir" > "$record/inode"
 fi
 for entry in "$root"/* "$root"/.[!.]* "$root"/..?*; do
  exists "$entry" || continue
  name=${entry##*/}; reserved "$name" && continue
  record="$pending/entries/$(entry_key "$name")"; mkdir "$record"
  printf '%s' "$name" > "$record/name"; inode "$entry" > "$record/inode"
 done
 inode "$root" > "$pending/root-inode"
 printf '%s\n' "$stage_key" > "$pending/stage-key"
 sync -f "$repository"
 mv -T -n -- "$pending" "$journal"; [ ! -e "$pending" ]
 sync -f "$journal_parent"
fi
[ -d "$journal" ] && [ "$(realpath -e "$journal")" = "$journal" ]
[ "$(cat "$journal/root-inode")" = "$(inode "$root")" ]
[ "$(cat "$journal/stage-key")" = "$stage_key" ]
if [ -f "$journal/completed" ]; then exit 0; fi
if [ ! -f "$journal/stage-inode" ]; then
 if exists "$stage" || exists "$destination"; then echo 'default migration stage collided before ownership was saved' >&2; exit 1; fi
 [ ! -L "$journal/worktree" ]
 if ! exists "$journal/worktree"; then mkdir -m 0700 "$journal/worktree"; fi
 [ -d "$journal/worktree" ]
 inode "$journal/worktree" > "$journal/stage-inode"
 sync -f "$journal"
fi
if exists "$journal/worktree"; then
 [ ! -L "$journal/worktree" ] && [ "$(inode "$journal/worktree")" = "$(cat "$journal/stage-inode")" ]
 ! exists "$stage" && ! exists "$destination"
 atomic_volume_move -n "$journal/worktree" "$stage"
 sync -f "$root"
fi
if exists "$destination"; then
 [ ! -L "$destination" ] && [ "$(inode "$destination")" = "$(cat "$journal/stage-inode")" ]
 target=$destination
else
 [ ! -L "$stage" ] && [ -d "$stage" ] && [ "$(inode "$stage")" = "$(cat "$journal/stage-inode")" ]
 target=$stage
fi
# An owner may add an untracked root file while recovering from a killed
# migration. Journal the new inode before moving it, without replacing any
# earlier destination. Hash keys give a direct lookup even for raw filenames.
for entry in "$root"/* "$root"/.[!.]* "$root"/..?*; do
 exists "$entry" || continue
 name=${entry##*/}; reserved "$name" && continue
 [ "$entry" = "$destination" ] && [ "$target" = "$destination" ] && continue
 key=$(entry_key "$name"); record="$journal/entries/$key"
 if exists "$record"; then
  [ -d "$record" ] && [ ! -L "$record" ]
  read_raw "$record/name"; [ "$value" = "$name" ]
  continue
 fi
 ! exists "$target/$name" || { echo 'new root data collides with the default checkout' >&2; exit 1; }
 pending="$journal/.entry-$key.pending"
 [ ! -L "$pending" ]
 if exists "$pending"; then [ -d "$pending" ]; rm -rf -- "$pending"; fi
 mkdir -m 0700 "$pending"
 printf '%s' "$name" > "$pending/name"
 inode "$entry" > "$pending/inode"
 : > "$pending/late"
 sync -f "$pending"
 mv -T -n -- "$pending" "$record"; [ ! -e "$pending" ]
 sync -f "$journal/entries"
done
for record in "$journal/entries"/*; do
 read_raw "$record/name"; name=$value
 [ -n "$name" ]; case "$name" in */*|.|..) exit 1 ;; esac
 expected=$(cat "$record/inode")
 if exists "$root/$name"; then
  { [ "$target" = "$stage" ] || [ -f "$record/late" ]; } && [ "$(inode "$root/$name")" = "$expected" ]
  ! exists "$target/$name" || { echo 'default migration entry collided' >&2; exit 1; }
  mv -T -n -- "$root/$name" "$target/$name"
 fi
 [ "$(inode "$target/$name")" = "$expected" ]
done
# Refuse a concurrent writer that appeared after the journal pass above. A
# later stopped-writer retry can journal that inode and converge safely.
for entry in "$root"/* "$root"/.[!.]* "$root"/..?*; do
 exists "$entry" || continue
 name=${entry##*/}; reserved "$name" && continue
 [ "$entry" = "$destination" ] && [ "$target" = "$destination" ] && continue
 echo 'workspace changed during default repository relocation' >&2; exit 1
done
if [ "$target" = "$stage" ]; then
 atomic_volume_move -n "$stage" "$destination"; [ ! -e "$stage" ]
 sync -f "$root"
fi
for record in "$journal/pointers"/*; do
 [ -d "$record" ] || continue
 read_raw "$record/path"; pointer=$value
 [ ! -L "$pointer" ] && [ -f "$pointer" ]
 if cmp -s "$pointer" "$record/new"; then continue; fi
 [ "$(inode "$pointer")" = "$(cat "$record/inode")" ] && cmp -s "$pointer" "$record/old"
 # The replacement is staged in protected storage, then renamed through the
 # single-volume mount. No interrupted cross-mount copy can truncate a pointer.
 [ ! -L "$record/new-pointer" ]
 cp -p -- "$record/new" "$record/new-pointer"
 sync -f "$record/new-pointer"
 atomic_volume_move -f "$record/new-pointer" "$pointer"
 sync -f "$pointer"
done
printf 'complete\n' > "$journal/completed"
sync -f "$journal"
`
