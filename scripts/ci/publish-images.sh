#!/usr/bin/env bash
# Builds the three release images and optionally pushes them.
#
#   scripts/ci/publish-images.sh <version> [--push]
#
# Builds ku (root Dockerfile), ku-api (BE) and ku-web (UI) from the checked
# out commit, tagged <version> and latest under IMAGE_PREFIX, with the commit
# baked into the API as its /health version. --push logs in to REGISTRY with
# FORGEJO_ACTOR/FORGEJO_TOKEN and pushes both tags. Needs a podman store
# (scripts/ci/podman-store.sh create) for the job.
set -euo pipefail

version=${1:?usage: $0 <version> [--push]}
push=${2:-}
: "${IMAGE_PREFIX:?IMAGE_PREFIX is required}"
commit=$(git rev-parse HEAD)

if [ "$push" = --push ]; then
	: "${REGISTRY:?REGISTRY is required}" "${FORGEJO_TOKEN:?}" "${FORGEJO_ACTOR:?}"
	# The job environment presets REGISTRY_AUTH_FILE to a read-only,
	# pull-only Harbor authfile. Pushes use a merged copy with the Forgejo
	# registry credentials, so base-image pulls keep going through the Harbor
	# mirror.
	authfile="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/ci-registry-auth.json"
	cp "${REGISTRY_AUTH_FILE:-/dev/null}" "$authfile" 2>/dev/null || touch "$authfile"
	printf '%s' "$FORGEJO_TOKEN" | podman login --authfile "$authfile" "$REGISTRY" \
		--username "$FORGEJO_ACTOR" --password-stdin
	export REGISTRY_AUTH_FILE="$authfile"
fi

# The three images share no stages, so they build concurrently. Wait for
# every build, so none still writes to the store when the job moves on to
# removing it.
pids=()
for spec in ku:.:go,npm ku-api:./BE:go ku-web:./UI:go,npm; do
	IFS=: read -r name context caches <<<"$spec"
	image="${IMAGE_PREFIX}/${name}"
	scripts/ci/build-image.sh "$caches" --pull --build-arg "KU_VERSION=$commit" \
		--tag "${image}:latest" --tag "${image}:${version}" "$context" \
		> >(sed -u "s/^/[${name}] /") 2>&1 &
	pids+=("$!")
done
status=0
for pid in "${pids[@]}"; do
	wait "$pid" || status=1
done
[ "$status" = 0 ] || exit "$status"

if [ "$push" = --push ]; then
	for name in ku ku-api ku-web; do
		image="${IMAGE_PREFIX}/${name}"
		podman push "${image}:latest"
		podman push "${image}:${version}"
	done
fi
