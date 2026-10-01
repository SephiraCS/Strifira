#!/bin/sh
# Gives nested podman a per-job overlay store on the shared /cache volume.
#
#   scripts/ci/podman-store.sh create <name>
#   scripts/ci/podman-store.sh remove
#
# The job's root filesystem is itself overlayfs, so podman there falls back
# to the copying vfs driver: every pulled layer and build step copies a whole
# root filesystem through the runner's Docker storage. /cache is a plain
# filesystem that supports overlay, so pulls, builds, commits, and saves take
# seconds instead of minutes and stay off the Docker daemon's disk.
#
# `create` writes the store's configuration (image copies stage their blobs
# in the store too, not in /var/tmp) and exports it with PODMAN_STORE to
# later steps; `remove` (run with `if: always()`) deletes the store.
#
# Cancelled jobs skip `remove`, so `create` sweeps stores older than six
# hours. A store's directory is not touched while it is in use, so that age
# must stay above every job's timeout-minutes (at most 60 today).
set -eu

root=/cache/ci-tmp

# Deletes a store, first detaching any overlay mounts a build or container
# left behind (rm -rf alone fails with EBUSY on them). Failures only warn:
# a leftover store must not fail the job, and the sweep retries it later.
delete_store() {
	awk -v p="$1/" 'index($2, p) == 1 { print $2 }' /proc/mounts | sort -r | xargs -r umount -l 2>/dev/null || true
	rm -rf "$1" || echo "warning: could not delete $1" >&2
}

case "${1:-}" in
create)
	: "${2:?usage: $0 create <name>}"
	: "${GITHUB_ENV:?GITHUB_ENV is required}"
	mkdir -p "$root"
	find "$root" -mindepth 1 -maxdepth 1 -name 'ku-*' -mmin +360 | while IFS= read -r stale; do
		delete_store "$stale"
	done
	store="$root/ku-${GITHUB_RUN_ID:?}-$2"
	# A re-run reuses the run ID; never build on a cancelled attempt's store.
	[ ! -e "$store" ] || delete_store "$store"
	mkdir -p "$store/root" "$store/run" "$store/tmp"
	cat >"$store/storage.conf" <<-CONF
		[storage]
		driver = "overlay"
		graphroot = "$store/root"
		runroot = "$store/run"
	CONF
	cat >"$store/containers.conf" <<-CONF
		[engine]
		image_copy_tmp_dir = "$store/tmp"
	CONF
	{
		echo "CONTAINERS_STORAGE_CONF=$store/storage.conf"
		echo "CONTAINERS_CONF_OVERRIDE=$store/containers.conf"
		echo "PODMAN_STORE=$store"
	} >>"$GITHUB_ENV"
	;;
remove)
	store="${PODMAN_STORE:-}"
	[ -n "$store" ] && [ -d "$store" ] || exit 0
	case "$store" in
	"$root"/ku-*) ;;
	*)
		echo "refusing to remove $store: not a store under $root" >&2
		exit 1
		;;
	esac
	podman rm --all --force --time 0 >/dev/null 2>&1 || true
	delete_store "$store"
	;;
*)
	echo "usage: $0 create <name> | remove" >&2
	exit 2
	;;
esac
