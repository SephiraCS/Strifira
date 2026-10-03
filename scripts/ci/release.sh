#!/bin/sh
# Cuts the next patch release of a commit that was just deployed to alpha.
#
#   scripts/ci/release.sh <deployed-sha>
#
# Run by the Release job of ci.yml after Deploy to Alpha succeeds on main.
# Versions are never edited by hand (AGENTS.md: the version is bumped
# automatically); pull requests leave UI/package.json and the OpenAPI
# info.version alone. This script:
#
#   1. picks the next version: the patch after the higher of the latest
#      vX.Y.Z tag and UI/package.json (minor and major bumps stay manual:
#      set the version in a pull request and the next release continues
#      from it);
#   2. writes it to UI/package.json, UI/package-lock.json, the OpenAPI
#      info.version, and the public release manifest UI/static/releases.json;
#   3. commits that as "chore(release): vX.Y.Z [skip ci]" on top of the
#      deployed commit, tags it, and pushes both atomically to main;
#   4. creates the Forgejo release with the first-parent commit subjects
#      since the previous tag as notes;
#   5. writes version=vX.Y.Z and sha=<release commit> to $GITHUB_OUTPUT.
#
# No loop: the push uses the job token, and Forgejo never starts workflows
# for changes authored with it; "[skip ci]" also keeps CI off that commit if
# a personal token is ever used instead. The release commit only moves
# version fields, so it needs no deploy of its own.
#
# When main has moved past the deployed commit (another merge is queued
# behind this run), the release is left to the newer commit's run so the
# release commit never lands on top of undeployed code. A re-run of a run
# whose commit was already released does nothing.
set -eu

sha=${1:?usage: $0 <deployed-sha>}
: "${FORGEJO_TOKEN:?FORGEJO_TOKEN is required}"
: "${GITHUB_SERVER_URL:?}" "${GITHUB_REPOSITORY:?}"
api="$GITHUB_SERVER_URL/api/v1/repos/$GITHUB_REPOSITORY"
host=${GITHUB_SERVER_URL#https://}
output=${GITHUB_OUTPUT:-/dev/null}

# Credentials stay out of the remote configuration and the logs; git's
# helper shell expands the token from the environment.
# shellcheck disable=SC2016
git config --local credential.helper \
	'!f() { echo username=forgejo-actions; echo "password=$FORGEJO_TOKEN"; }; f'
remote=${RELEASE_REMOTE:-"https://$host/$GITHUB_REPOSITORY.git"}
git fetch --quiet --tags --force "$remote" "+refs/heads/main:refs/remotes/release/main"

main=$(git rev-parse release/main)
if [ "$main" != "$sha" ]; then
	echo "main is at $main, past the deployed $sha; the newer commit's run releases."
	exit 0
fi
if tagged=$(git describe --exact-match --tags --match 'v[0-9]*' "$sha" 2>/dev/null); then
	echo "$sha is already released as $tagged."
	exit 0
fi
git checkout --quiet --detach "$sha"

latest=$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' | sed 's/^v//' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1)
current=$(jq -r .version UI/package.json)
case "$current" in
*[!0-9.]* | '') echo "UI/package.json has an unexpected version: $current" >&2; exit 1 ;;
esac
base=$(printf '%s\n%s\n' "${latest:-0.0.0}" "$current" | sort -V | tail -n 1)
next=$(echo "$base" | awk -F. '{ printf "%d.%d.%d", $1, $2, $3 + 1 }')
tag="v$next"
echo "Releasing $tag (latest tag ${latest:-none}, UI/package.json $current)."

# Release notes: what landed on main since the previous release.
if [ -n "$latest" ]; then
	range="v$latest..$sha"
else
	range=$sha
fi
notes=$(git log --first-parent --format='- %s' --max-count=100 "$range" | grep -v '^- chore(release): ' || :)
[ -n "$notes" ] || notes="- No changes recorded."
notes=$(printf 'Deployed to alpha as %s.\n\n%s\n' "$sha" "$notes")

(cd UI && npm version "$next" --no-git-tag-version --allow-same-version >/dev/null)
# info.version is the first two-space-indented "version:" in the file.
sed -i "0,/^  version: .*/s//  version: $next/" BE/internal/apidocs/openapi.yaml
grep -q "^  version: $next\$" BE/internal/apidocs/openapi.yaml || {
	echo "could not set info.version in BE/internal/apidocs/openapi.yaml" >&2
	exit 1
}

# The manifest lists published releases newest first, this one included.
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl --silent --show-error --fail --max-time 30 \
	--header "Authorization: token $FORGEJO_TOKEN" "$api/releases?limit=100" >"${TMPDIR:-/tmp}/releases.json"
jq --arg tag "$tag" --arg url "$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/releases/tag/$tag" \
	--arg body "$notes" --arg now "$now" '
	def entry: {
		tag_name, html_url, body, published_at: .created_at,
		prerelease: (.prerelease // false), force_upgrade: false,
		minimum_supported_version: null, upgrade_url: .html_url, upgrade_message: null
	};
	[{tag_name: $tag, html_url: $url, body: $body, created_at: $now, prerelease: false} | entry]
	+ [.[] | select(.draft == false and .tag_name != $tag) | entry]' \
	"${TMPDIR:-/tmp}/releases.json" >UI/static/releases.json

git -c user.name='forgejo-actions[bot]' -c user.email=noreply@git.terrascale.tech \
	commit --quiet -m "chore(release): $tag [skip ci]" \
	-m "Automatic version bump after $sha was deployed to alpha." \
	-- UI/package.json UI/package-lock.json UI/static/releases.json BE/internal/apidocs/openapi.yaml
git -c user.name='forgejo-actions[bot]' -c user.email=noreply@git.terrascale.tech \
	tag --annotate "$tag" -m "$tag"
release_sha=$(git rev-parse HEAD)

# Atomic: either main and the tag both move, or neither does (main moved
# since the fetch; the newer run releases).
if ! git push --atomic "$remote" "HEAD:refs/heads/main" "refs/tags/$tag"; then
	echo "main moved while releasing; the newer commit's run releases." >&2
	git tag --delete "$tag" >/dev/null
	exit 0
fi

jq -n --arg tag "$tag" --arg body "$notes" \
	'{tag_name: $tag, name: $tag, body: $body, draft: false, prerelease: false}' >"${TMPDIR:-/tmp}/release-request.json"
code=$(curl --silent --output "${TMPDIR:-/tmp}/release-response.json" --write-out '%{http_code}' --max-time 30 \
	--header "Authorization: token $FORGEJO_TOKEN" --header 'Content-Type: application/json' \
	--data-binary "@${TMPDIR:-/tmp}/release-request.json" "$api/releases")
case "$code" in
201) echo "Created release $tag." ;;
409) echo "Release $tag already exists." ;;
*) echo "Creating release $tag failed with HTTP $code:" >&2; cat "${TMPDIR:-/tmp}/release-response.json" >&2; exit 1 ;;
esac

{
	echo "version=$tag"
	echo "sha=$release_sha"
} >>"$output"
