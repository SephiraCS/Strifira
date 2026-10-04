#!/bin/sh
# Polls the public health endpoint until it reports the deployed commit.
#
#   scripts/ci/verify-deploy.sh <health-url> <sha> [previous-version]
#
# The server reports the commit it was built from as "version" (baked in by
# the Dockerfile's KU_VERSION build argument). ku-deploy already rolls back
# an image that never answers /health on alpha's loopback; this checks the
# public path end to end and that the new commit, not a stale or
# rolled-back one, is what users get. On timeout it prints the previous
# commit and how to roll back to it, then fails.
set -eu

url=${1:?usage: $0 <health-url> <sha> [previous-version]}
sha=${2:?usage: $0 <health-url> <sha> [previous-version]}
previous=${3:-unknown}
timeout=${VERIFY_TIMEOUT_SECONDS:-300}

deadline=$(($(date +%s) + timeout))
seen=
while :; do
	seen=$(curl --silent --fail --max-time 10 "$url" | jq -r '.version // "none"' 2>/dev/null) || seen=unreachable
	if [ "$seen" = "$sha" ]; then
		echo "$url reports $sha."
		exit 0
	fi
	if [ "$(date +%s)" -ge "$deadline" ]; then
		break
	fi
	echo "$url reports ${seen:-nothing}; waiting for $sha"
	sleep 10
done

cat >&2 <<EOF
::error::$url still reports ${seen:-nothing} after ${timeout}s, not the deployed $sha.
The commit served before this deploy was: $previous
To roll back (ku-deploy keeps the last 10 staged commits), on alpha run:
  sudo -u ku-deploy /usr/local/sbin/ku-deploy roll <previous 40-hex sha>
or from a job holding ALPHA_DEPLOY_SSH_KEY:
  scripts/alpha-deploy.sh roll <previous 40-hex sha>
EOF
exit 1
