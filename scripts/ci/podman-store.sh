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
# later steps; `remove` (run with `if: always()`) deletes
# the store. Stores a cancelled job never removed are swept after six hours.
set -eu

root=/cache/ci-tmp

case "${1:-}" in
create)
	: "${2:?usage: $0 create <name>}"
	: "${GITHUB_ENV:?GITHUB_ENV is required}"
	mkdir -p "$root"
	find "$root" -mindepth 1 -maxdepth 1 -name 'ku-*' -mmin +360 -exec rm -rf {} +
	store="$root/ku-${GITHUB_RUN_ID:?}-$2"
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
	podman rm --all --force --time 0 >/dev/null 2>&1 || true
	# Layers a build or container left mounted would block the delete.
	awk -v p="$store/" 'index($2, p) == 1 { print $2 }' /proc/mounts | sort -r | xargs -r umount -l
	rm -rf "$store"
	;;
*)
	echo "usage: $0 create <name> | remove" >&2
	exit 2
	;;
esac
