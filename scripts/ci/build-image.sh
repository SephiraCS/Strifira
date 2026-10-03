#!/usr/bin/env bash
# Builds an image with the runner's shared dependency caches mounted into
# every RUN step.
#
#   scripts/ci/build-image.sh <caches> [podman build arguments...]
#
# <caches> is a comma-separated list of go and npm, or "none" on its own. The ts-builder
# job image points GOMODCACHE, GOCACHE, and NPM_CONFIG_CACHE at the shared
# /cache volume, so module and package downloads and unchanged Go packages
# are reused across jobs. Mounted caches never become part of the image. Only
# mount npm into Dockerfiles that keep /root/.npm: one that deletes it would
# empty the shared cache.
#
# Intermediate layers are never reused between jobs, so they are not
# committed at all, and independent stages build in parallel. Images use the
# Docker format because the OCI format drops HEALTHCHECK.
set -euo pipefail

caches="${1:?usage: $0 <caches> [podman build arguments...]}"
shift

volumes=()
names=()
[ "$caches" = none ] || IFS=, read -ra names <<<"$caches"
for cache in "${names[@]}"; do
	case "$cache" in
	go)
		: "${GOMODCACHE:?the ts-builder image exports GOMODCACHE}"
		: "${GOCACHE:?the ts-builder image exports GOCACHE}"
		mkdir -p "$GOMODCACHE" "$GOCACHE"
		volumes+=(--volume "$GOMODCACHE:/go/pkg/mod" --volume "$GOCACHE:/root/.cache/go-build")
		;;
	npm)
		: "${NPM_CONFIG_CACHE:?the ts-builder image exports NPM_CONFIG_CACHE}"
		mkdir -p "$NPM_CONFIG_CACHE"
		volumes+=(--volume "$NPM_CONFIG_CACHE:/root/.npm")
		;;
	*)
		echo "unknown cache: $cache" >&2
		exit 2
		;;
	esac
done

exec podman build --format docker --layers=false --jobs 0 "${volumes[@]}" "$@"
