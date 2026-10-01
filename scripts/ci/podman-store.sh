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
root=$(realpath -m -- "$root")

validate_store() {
	[ ! -L "$1" ] || { echo "refusing to remove symlink: $1" >&2; return 1; }
	resolved=$(realpath -m -- "$1")
	[ "$(dirname -- "$resolved")" = "$root" ] || {
		echo "refusing to remove $resolved: not a store under $root" >&2
		return 1
	}
	case "$(basename -- "$resolved")" in
	ku-*) ;;
	*) echo "refusing to remove $resolved: invalid store name" >&2; return 1 ;;
	esac
	[ "${2:-}" = owned ] || [ -f "$resolved/storage.conf" ] || {
		echo "refusing to remove $resolved: no store configuration" >&2
		return 1
	}
	printf '%s\n' "$resolved"
}

# Deletes a store, first detaching any overlay mounts a build or container
# left behind (rm -rf alone fails with EBUSY on them). Return deletion
# failures so create can fail, while remove and stale sweeps only warn.
delete_store() {
	resolved=$(validate_store "$1" "${2:-}") || return 1
	awk -v p="$resolved/" 'index($2, p) == 1 { print $2 }' /proc/mounts | sort -r | xargs -r umount -l 2>/dev/null || true
	rm -rf -- "$resolved"
}

case "${1:-}" in
create)
	: "${2:?usage: $0 create <name>}"
	: "${GITHUB_ENV:?GITHUB_ENV is required}"
	case "$2" in
	*[!A-Za-z0-9._-]*|.|..) echo "invalid store name: $2" >&2; exit 2 ;;
	esac
	case "${GITHUB_RUN_ID:?}" in
	*[!0-9]*) echo "invalid GITHUB_RUN_ID" >&2; exit 2 ;;
	esac
	mkdir -p "$root"
	find "$root" -mindepth 1 -maxdepth 1 \( -type d -o -type l \) -name 'ku-*' -mmin +360 | while IFS= read -r stale; do
		if [ -L "$stale" ]; then
			rm -f -- "$stale" || echo "warning: could not unlink $stale" >&2
		else
			delete_store "$stale" owned || echo "warning: could not sweep $stale" >&2
		fi
	done
	store="$root/ku-${GITHUB_RUN_ID:?}-$2"
	# A re-run reuses the run ID; never build on a cancelled attempt's store.
	if [ -L "$store" ]; then
		rm -f -- "$store" || { echo "could not unlink $store" >&2; exit 1; }
	fi
	if [ -e "$store" ]; then
		delete_store "$store" owned || { echo "could not reset $store" >&2; exit 1; }
	fi
	[ ! -e "$store" ] || { echo "could not reset $store" >&2; exit 1; }
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
	store=$(validate_store "$store")
	CONTAINERS_STORAGE_CONF="$store/storage.conf" CONTAINERS_CONF_OVERRIDE="$store/containers.conf" \
		podman rm --all --force --time 0 >/dev/null 2>&1 || true
	delete_store "$store" || echo "warning: could not delete $store" >&2
	;;
*)
	echo "usage: $0 create <name> | remove" >&2
	exit 2
	;;
esac
