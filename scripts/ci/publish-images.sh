#!/usr/bin/env bash
# Builds the three release images and optionally pushes them.
#
#   scripts/ci/publish-images.sh <version> [--push]
#
# Builds ku (root Dockerfile), ku-api (BE) and ku-web (UI) from the checked
# out commit, tagged <version> and latest under IMAGE_PREFIX, with the commit
# baked into the API as its /health version. --push logs in to REGISTRY with
# FORGEJO_ACTOR/FORGEJO_TOKEN and pushes <version>, plus latest when
# <version> is the newest vX.Y.Z tag (so a repaired or late older release
# never moves latest back). Needs a podman store
# (scripts/ci/podman-store.sh create) and the repository's tags for the job;
# the jobs that publish serialize on the ku-publish-images concurrency group.
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
	# Fail before minutes of image builds when the token cannot push: Forgejo's
	# automatic job token authenticates to the registry but has no package-write
	# scope, so every podman push ends in "authentication required". The token
	# must come from secrets.REGISTRY_PUSH_TOKEN (a user or bot token with
	# write:package) instead.
	code=$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 30 \
		--user "$FORGEJO_ACTOR:$FORGEJO_TOKEN" --request POST \
		"https://$REGISTRY/v2/${IMAGE_PREFIX#*/}/ku/blobs/uploads/" || echo 000)
	if [ "$code" = 401 ] || [ "$code" = 403 ]; then
		echo "::error::This token cannot push packages to $REGISTRY (HTTP $code on the blob-upload probe)." >&2
		echo "::error::Forgejo's automatic job token is registry-pull-only. Create an access token with" >&2
		echo "::error::write:package scope for a member of the Tools org (or the tools-bot once" >&2
		echo "::error::it is granted package write) and save it as the REGISTRY_PUSH_TOKEN repository" >&2
		echo "::error::secret; the Release job then uses it automatically." >&2
		exit 1
	fi
	echo "Registry push probe answered HTTP $code; continuing."
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
	# latest only moves forward: to a vX.Y.Z version at least as new as every
	# vX.Y.Z tag in the checkout (which needs all tags). Older or other
	# versions publish their version tag only.
	newest=$(git tag --list 'v[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/^v//' | sort -V | tail -n 1 || :)
	latest=false
	if [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] &&
		[ "$(printf '%s\n%s\n' "${newest:-0.0.0}" "${version#v}" | sort -V | tail -n 1)" = "${version#v}" ]; then
		latest=true
	else
		echo "Not moving latest: $version is not a vX.Y.Z release at least as new as v${newest:-0.0.0}."
	fi
	for name in ku ku-api ku-web; do
		image="${IMAGE_PREFIX}/${name}"
		podman push "${image}:${version}"
		if [ "$latest" = true ]; then
			podman push "${image}:latest"
		fi
	done
fi
