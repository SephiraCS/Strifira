#!/bin/sh
# Tells whether a change touches an image's build inputs.
#
#   scripts/ci/image-inputs-changed.sh <base-ref> <path>...
#
# Exits 0 when <base-ref>...HEAD changes any of the paths and 1 when it does
# not. The release job (ci.yml) bumps the "version" of UI/package.json and
# UI/package-lock.json on every deploy; a change to those files that only
# moves that version does not change what the images install, so it does not
# count. Any error counts as a change, so the image is still built and
# scanned.
set -eu
base=${1:?usage: $0 <base-ref> <path>...}
shift

# Compare against the merge base, like the three-dot diff does.
merge_base=$(git merge-base "$base" HEAD) || exit 0
changed=$(git diff --name-only "$merge_base" HEAD -- "$@") || exit 0
[ -n "$changed" ] || exit 1

# Drops the package's own version: the top-level field in both files and the
# root package entry of the lockfile.
strip_version() {
	jq -S 'del(.version) | if .packages[""]? then del(.packages[""].version) else . end'
}

# The loop runs in a subshell: 10 means a real change, 11 none.
status=0
printf '%s\n' "$changed" | {
	while IFS= read -r path; do
		case "$path" in
		UI/package.json | UI/package-lock.json)
			old=$(git show "$merge_base:$path" | strip_version) || exit 10
			new=$(git show "HEAD:$path" | strip_version) || exit 10
			[ "$old" = "$new" ] || exit 10
			echo "$path: only the version changed" >&2
			;;
		*)
			exit 10
			;;
		esac
	done
	exit 11
} || status=$?
[ "$status" != 11 ] || exit 1
exit 0
