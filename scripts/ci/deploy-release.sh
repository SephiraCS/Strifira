#!/usr/bin/env bash
# Rolls alpha forward from a deployed commit to its release commit.
#
#   scripts/ci/deploy-release.sh <health-url> <image> <release-sha> <deployed-sha>
#
# Run by the Release job of ci.yml after publish-images.sh built <image>
# (the ku image of the release commit, built like Stage Image builds it).
# The release commit only bumps versions and the release manifest that the
# UI serves, so without this alpha would show the previous release until the
# next deploy. It is staged and rolled with scripts/alpha-deploy.sh and
# checked with verify-deploy.sh, exactly like Deploy to Alpha.
#
# Alpha is only rolled when it still runs <deployed-sha>: a resumed release
# of an older commit, or alpha already serving something newer, is left
# alone so this never rolls alpha backwards. No loop: rolling alpha starts
# no workflow.
set -euo pipefail

url=${1:?usage: $0 <health-url> <image> <release-sha> <deployed-sha>}
image=${2:?usage: $0 <health-url> <image> <release-sha> <deployed-sha>}
release=${3:?usage: $0 <health-url> <image> <release-sha> <deployed-sha>}
deployed=${4:?usage: $0 <health-url> <image> <release-sha> <deployed-sha>}

running=$(curl --silent --fail --max-time 10 "$url" | jq -r '.version // empty' 2>/dev/null || :)
if [ "$running" = "$release" ]; then
	echo "Alpha already runs the release commit $release."
	exit 0
fi
if [ "$running" != "$deployed" ]; then
	echo "::warning::Alpha runs ${running:-an unknown commit}, not $deployed; leaving it there instead of rolling to the release commit $release."
	exit 0
fi

echo "Rolling alpha from $deployed to the release commit $release."
podman tag "$image" "ku:$release"
podman save --format docker-archive "ku:$release" | scripts/alpha-deploy.sh stage "$release"
scripts/alpha-deploy.sh roll "$release"
scripts/ci/verify-deploy.sh "$url" "$release" "$deployed"
