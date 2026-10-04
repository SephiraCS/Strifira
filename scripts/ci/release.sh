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
#   5. writes version=vX.Y.Z, sha=<release commit> and deployed=<its parent>
#      to $GITHUB_OUTPUT; the Release job then publishes the images and rolls
#      alpha forward to the release commit (scripts/ci/deploy-release.sh).
#
# No loop: the push uses the job token, and Forgejo never starts workflows
# for changes authored with it; "[skip ci]" also keeps CI off that commit if
# a personal token is ever used instead. The release commit only moves
# version fields and the release manifest, so it skips the test suites, but
# the Release job still rolls alpha to it so alpha shows the new version and
# manifest (rolling alpha starts no workflow either).
#
# When main has moved past the deployed commit (another merge is queued
# behind this run), the release is left to the newer commit's run so the
# release commit never lands on top of undeployed code. A re-run of a run
# whose commit was already released resumes it instead: when a tagged
# release commit of this commit is on main (whether or not main has moved on
# since), a release-API or image failure after the push is repaired by
# re-running the job, which (re)creates the release idempotently and writes
# the outputs again without touching main. The same happens when the
# deployed commit is itself a tagged release commit (CI dispatched on main
# right after a release). publish-images.sh then publishes that version but
# only moves latest when it is still the newest release.
#
# A push that fails because main moved on is skipped like above; any other
# push failure (authentication, outage, protected branch, conflicting tag)
# fails the job.
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

# notes <previous version or empty> [deployed commit, default $sha]: the
# release notes for the deployed commit.
notes() {
	deployed=${2:-$sha}
	if [ -n "$1" ]; then
		range="v$1..$deployed"
	else
		range=$deployed
	fi
	log=$(git log --first-parent --format='- %s' --max-count=100 "$range" | grep -v '^- chore(release): ' || :)
	printf 'Deployed to alpha as %s.\n\n%s\n' "$deployed" "${log:-- No changes recorded.}"
}

# create_release <tag> <notes>: creates the Forgejo release; an existing one
# counts as created, so re-runs are idempotent.
create_release() {
	jq -n --arg tag "$1" --arg body "$2" \
		'{tag_name: $tag, name: $tag, body: $body, draft: false, prerelease: false}' >"${TMPDIR:-/tmp}/release-request.json"
	code=$(curl --silent --output "${TMPDIR:-/tmp}/release-response.json" --write-out '%{http_code}' --max-time 30 \
		--header "Authorization: token $FORGEJO_TOKEN" --header 'Content-Type: application/json' \
		--data-binary "@${TMPDIR:-/tmp}/release-request.json" "$api/releases" || :)
	case "$code" in
	201) echo "Created release $1." ;;
	409) echo "Release $1 already exists." ;;
	*) echo "Creating release $1 failed with HTTP ${code:-000}:" >&2; cat "${TMPDIR:-/tmp}/release-response.json" >&2 2>/dev/null || :; exit 1 ;;
	esac
}

# outputs <tag> <release commit>: also names the commit the release commit
# bumps (deployed=), which alpha must still run for the Release job to roll
# it forward to the release commit.
outputs() {
	{
		echo "version=$1"
		echo "sha=$2"
		echo "deployed=$(git rev-parse "$2^")"
	} >>"$output"
}

# released_tag <commit> [deployed commit, default $sha]: prints vX.Y.Z when
# <commit> is the release commit of the deployed commit: its only parent is
# the deployed commit, its subject is "chore(release): vX.Y.Z [skip ci]", and
# tag vX.Y.Z points at it.
released_tag() {
	[ "$(git rev-parse "$1^@" 2>/dev/null)" = "${2:-$sha}" ] || return 1
	t=$(git log -1 --format=%s "$1" | sed -nE 's/^chore\(release\): (v[0-9]+\.[0-9]+\.[0-9]+) \[skip ci\]$/\1/p')
	[ -n "$t" ] || return 1
	[ "$(git rev-parse --verify --quiet "refs/tags/$t^{commit}")" = "$1" ] || return 1
	echo "$t"
}

# release_commit: prints the release commit of $sha on main, if any.
release_commit() {
	git tag --list 'v[0-9]*' --contains "$sha" | while read -r t; do
		c=$(git rev-parse --verify --quiet "refs/tags/$t^{commit}") || continue
		[ "$(released_tag "$c" || :)" = "$t" ] || continue
		git merge-base --is-ancestor "$c" release/main || continue
		echo "$c"
		break
	done
}

# resume <release commit> <tag>: finishes a release whose commit and tag are
# already on main: (re)creates the Forgejo release and writes the outputs so
# the images are published, without touching main.
resume() {
	deployed=$(git rev-parse "$1^")
	git checkout --quiet --detach "$1"
	previous=$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$deployed" 2>/dev/null | sed 's/^v//' || :)
	create_release "$2" "$(notes "$previous" "$deployed")"
	outputs "$2" "$1"
}

main=$(git rev-parse release/main)

# The deployed commit is itself a release commit (CI dispatched on main right
# after a release, or a re-run of such a run): make sure that release is
# complete instead of cutting another one on top of a version-only commit,
# whether or not main has moved on since, as long as it is still on main.
parent=$(git rev-parse "$sha^@" 2>/dev/null || :)
case "$parent" in
*[!0-9a-f]* | '') ;;
*)
	if tag=$(released_tag "$sha" "$parent") && git merge-base --is-ancestor "$sha" "$main"; then
		echo "$sha is the release commit of $tag; resuming the release (main is at $main)."
		resume "$sha" "$tag"
		exit 0
	fi
	;;
esac

if [ "$main" != "$sha" ]; then
	if release=$(release_commit) && [ -n "$release" ]; then
		# A previous attempt pushed the release commit; finish what it left.
		tag=$(released_tag "$release")
		echo "$sha was released as $tag ($release); resuming the release (main is at $main)."
		resume "$release" "$tag"
		exit 0
	fi
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
notes=$(notes "$latest")

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

# Atomic: either main and the tag both move, or neither does. A rejected
# push is only a race when main has since moved past the deployed commit
# (the newer run releases); a push whose response was lost but landed
# continues; anything else fails the job.
if ! git push --atomic "$remote" "HEAD:refs/heads/main" "refs/tags/$tag"; then
	git tag --delete "$tag" >/dev/null
	git fetch --quiet --tags --force "$remote" "+refs/heads/main:refs/remotes/release/main"
	main=$(git rev-parse release/main)
	if [ "$main" = "$release_sha" ] && [ "$(released_tag "$main" || :)" = "$tag" ]; then
		echo "The push of $tag landed despite the error; continuing."
	elif [ "$main" != "$sha" ] && git merge-base --is-ancestor "$sha" "$main"; then
		echo "main moved to $main while releasing; the newer commit's run releases." >&2
		exit 0
	else
		echo "Pushing $tag to main failed, and main ($main) has not moved past $sha; not a race." >&2
		exit 1
	fi
fi

create_release "$tag" "$notes"
outputs "$tag" "$release_sha"
