#!/usr/bin/env bash
# Builds the release image and optionally pushes it.
#
#   scripts/ci/publish-images.sh <version> [--push]
#
# Builds ku (the root Dockerfile) from the checked out commit, tagged with
# the full commit sha, its first 12 hex characters and <version> under
# IMAGE_PREFIX/ku (registry.mariogk.com/homelab/ku on CI; Docker references
# must be lowercase), with the commit baked into the API as its /health
# version. --push logs in to REGISTRY with HARBOR_USER/HARBOR_TOKEN and
# pushes the tags. Needs the repository's tags for the job (checkout with
# fetch-depth: 0).
#
# homelab-deployer rolls alpha to the release commit once the image exists
# (it discovers it by the full-sha tag and treats a [skip ci] commit with a
# published image as deployable); the deployer alone moves :latest, after
# its health gate, so this script never pushes :latest.
set -euo pipefail

version=${1:?usage: $0 <version> [--push]}
push=${2:-}
: "${IMAGE_PREFIX:?IMAGE_PREFIX is required}"
commit=$(git rev-parse HEAD)
sha12=${commit:0:12}
image="${IMAGE_PREFIX}/ku"

if [ "$push" = --push ]; then
	: "${REGISTRY:?REGISTRY is required}" "${HARBOR_USER:?}" "${HARBOR_TOKEN:?}"
	# Fail before minutes of image build when the credentials are wrong;
	# the probe also catches a token without push access to the project.
	code=$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 30 \
		--user "$HARBOR_USER:$HARBOR_TOKEN" "https://$REGISTRY/v2/" || echo 000)
	if [ "$code" != 200 ]; then
		echo "::error::Harbor authentication failed (HTTP $code on the /v2/ probe)." >&2
		echo "::error::Check the HARBOR_USER and HARBOR_TOKEN repository secrets and the" >&2
		echo "::error::robot account's permissions for the homelab project." >&2
		exit 1
	fi
	printf '%s' "$HARBOR_TOKEN" | docker login "$REGISTRY" \
		--username "$HARBOR_USER" --password-stdin
	echo "Registry auth probe answered HTTP $code; continuing."
fi

docker build --pull --build-arg "KU_VERSION=$commit" \
	--tag "${image}:${commit}" --tag "${image}:${sha12}" \
	--tag "${image}:${version}" .

if [ "$push" = --push ]; then
	docker push "${image}:${commit}"
	docker push "${image}:${sha12}"
	docker push "${image}:${version}"
fi
