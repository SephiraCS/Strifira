#!/bin/sh
# Files a failed non-blocking run (slow suite, nightly scans) as a Forgejo
# issue, so a red run that gates nothing is still seen and owned.
#
#   scripts/ci/report-failure.sh
#
# Run it from a final job with `needs: [...]` and `if: failure()`. One open
# issue per title (by default per workflow) collects the failures: the first
# failure opens it, later ones comment on it, and closing the issue starts a
# fresh one on the next failure. Each report carries the commit, the run URL,
# the failed jobs, failing test names found in their logs, a reproduction
# command, and a redacted tail of every failed job's log.
#
# Environment:
#   FORGEJO_TOKEN   job token (forgejo.token); needs issue write and run read
#   REPORT_TITLE    issue title (default "CI failure: <workflow>")
#   REPRO           reproduction command(s), shown verbatim
#   FAILED_JOBS     fallback job list when the run's jobs cannot be listed
#   REDACT          extra literal values to mask, one per line
#   LOG_LINES       log tail length per failed job (default 60)
#   REPORT_DRY_RUN  1 prints the report instead of filing it
# plus the GITHUB_* variables every Actions job gets.
set -eu
export LC_ALL=C

: "${FORGEJO_TOKEN:?FORGEJO_TOKEN is required}"
: "${GITHUB_SERVER_URL:?}" "${GITHUB_REPOSITORY:?}" "${GITHUB_RUN_ID:?}" "${GITHUB_SHA:?}"
workflow=${GITHUB_WORKFLOW:-CI}
title=${REPORT_TITLE:-"CI failure: $workflow"}
log_lines=${LOG_LINES:-60}
api="$GITHUB_SERVER_URL/api/v1/repos/$GITHUB_REPOSITORY"
run_url="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/${GITHUB_RUN_NUMBER:-$GITHUB_RUN_ID}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

call() {
	# call <method> <path> [json body file]; prints the body, fails on non-2xx.
	if [ -n "${3:-}" ]; then
		set -- "$1" "$2" --header 'Content-Type: application/json' --data-binary "@$3"
	fi
	method=$1 path=$2
	shift 2
	curl --silent --show-error --fail --max-time 30 --request "$method" \
		--header "Authorization: token $FORGEJO_TOKEN" --header 'Accept: application/json' \
		"$@" "$api/$path"
}

# Forgejo already masks registered secrets as ***; this also catches values
# that never were secrets (tokens echoed by tools, URLs with credentials,
# KEY=value assignments, bearer headers, JWTs, private keys) and the literal
# values in REDACT and FORGEJO_TOKEN.
# shellcheck disable=SC2016 # the sed scripts hold literal $ and backticks
redact() {
	{
		printf '%s\n' "$FORGEJO_TOKEN"
		printf '%s\n' "${REDACT:-}"
	} | awk 'length($0) >= 6' >"$work/literals"
	# Terminal formatting goes first, so a secret split by a color escape or
	# a carriage return is whole again before the literals are matched: CSI
	# and OSC sequences, charset and other short escapes, then stray control
	# bytes (tab and newline stay).
	sed -E \
		-e 's/\x1b\][^\x07\x1b]*(\x07|\x1b\\)//g' \
		-e 's#\x1b\[[0-9;:?<=>]*[ -/]*[@-~]##g' \
		-e 's#\x1b[ -/]+[0-~]##g' \
		-e 's/\x1b[@-_]//g' \
		-e 's/[\x01-\x08\x0b-\x1f\x7f]//g' |
	# One pass per line with an advancing cursor: the earliest (then longest)
	# literal is masked and scanning continues after it, so inserted markers
	# are never rescanned (REDACT=REDACTED terminates).
	awk -v lits="$work/literals" '
		BEGIN { while ((getline l < lits) > 0) secrets[++n] = l }
		{
			rest = $0
			out = ""
			for (;;) {
				best = 0
				len = 0
				for (i = 1; i <= n; i++) {
					p = index(rest, secrets[i])
					if (p > 0 && (best == 0 || p < best || (p == best && length(secrets[i]) > len))) {
						best = p
						len = length(secrets[i])
					}
				}
				if (best == 0) break
				out = out substr(rest, 1, best - 1) "[REDACTED]"
				rest = substr(rest, best + len)
			}
			print out rest
		}' |
		sed -E \
			-e 's/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z //' \
			-e 's#([A-Za-z][A-Za-z0-9+.-]*://)[^/@ :]+:[^/@ ]+@#\1[REDACTED]@#g' \
			-e 's/((authorization|proxy-authorization)["'\'']?[[:space:]]*[:=]?[[:space:]]*["'\'']?((token|bearer|basic|digest|negotiate)[[:space:]]+)?)[^[:space:]"'\'',}]+/\1[REDACTED]/Ig' \
			-e 's/(bearer[[:space:]]+)[A-Za-z0-9._~+\/=-]{8,}/\1[REDACTED]/Ig' \
			-e 's/([A-Za-z0-9_]*(SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|APIKEY|PRIVATE_KEY|ACCESS_KEY|CREDENTIAL)[A-Za-z0-9_]*["'\'']?[[:space:]]*[:=][[:space:]]*)"[^"]*"/\1"[REDACTED]"/Ig' \
			-e 's/([A-Za-z0-9_]*(SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|APIKEY|PRIVATE_KEY|ACCESS_KEY|CREDENTIAL)[A-Za-z0-9_]*["'\'']?[[:space:]]*[:=][[:space:]]*)'\''[^'\'']*'\''/\1[REDACTED]/Ig' \
			-e 's/([A-Za-z0-9_]*(SECRET|TOKEN|PASSWORD|PASSWD|API_KEY|APIKEY|PRIVATE_KEY|ACCESS_KEY|CREDENTIAL)[A-Za-z0-9_]*["]?[[:space:]]*[:=][[:space:]]*["]?)[^[:space:]"&,]+/\1[REDACTED]/Ig' \
			-e 's/eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+/[REDACTED-JWT]/g' \
			-e 's/\b(gh[pousr]|glpat|xox[abpr]|sk|kub)[_-][A-Za-z0-9_-]{16,}/[REDACTED]/g' \
			-e 's/AKIA[0-9A-Z]{16}/[REDACTED]/g' \
			-e '/-----BEGIN [A-Z ]*PRIVATE KEY-----/,/-----END [A-Z ]*PRIVATE KEY-----/c\
[REDACTED PRIVATE KEY]' |
		sed 's/`\{3,\}/` ` `/g'
}

# Failed jobs of this run, as "<id>\t<name>" lines.
: >"$work/failed"
if call GET "actions/runs/$GITHUB_RUN_ID/jobs" >"$work/jobs.json" 2>"$work/jobs.err"; then
	jq -r '(if type == "array" then . else (.jobs // []) end)[]
		| select((.conclusion // .status) as $s | $s == "failure" or $s == "timedout" or $s == "cancelled")
		| "\(.id)\t\(.name)"' "$work/jobs.json" >"$work/failed" || :
else
	echo "warning: could not list the run's jobs: $(cat "$work/jobs.err")" >&2
fi
if [ ! -s "$work/failed" ] && [ -n "${FAILED_JOBS:-}" ]; then
	printf '%s\n' "$FAILED_JOBS" | sed '/^$/d; s/^/-\t/' >"$work/failed"
fi

{
	echo "**Workflow:** $workflow (${GITHUB_EVENT_NAME:-unknown} on \`${GITHUB_REF_NAME:-unknown}\`)"
	echo "**Commit:** [\`$GITHUB_SHA\`]($GITHUB_SERVER_URL/$GITHUB_REPOSITORY/commit/$GITHUB_SHA)"
	echo "**Run:** $run_url"
	echo
	echo "### Failed jobs"
	if [ -s "$work/failed" ]; then
		cut -f2 "$work/failed" | sed 's/^/- /'
	else
		echo "- (the job list was unavailable; see the run)"
	fi
	echo
	echo "### Reproduce"
	echo '```sh'
	echo "git checkout $GITHUB_SHA"
	printf '%s\n' "${REPRO:-# see the failed jobs in the run}"
	echo '```'
} >"$work/body.md"

: >"$work/tests"
: >"$work/logs.md"
while IFS="$(printf '\t')" read -r id name; do
	[ "$id" != - ] || continue
	if ! call GET "actions/jobs/$id/logs" >"$work/log" 2>/dev/null; then
		printf '\n<details><summary>%s</summary>\n\nThe log could not be fetched; see the run.\n</details>\n' "$name" >>"$work/logs.md"
		continue
	fi
	# Go, Playwright, Trivy, govulncheck and npm audit failure markers.
	redact <"$work/log" |
		grep -E -e '--- FAIL: ' -e '^FAIL[[:space:]]' -e 'panic: test timed out' \
			-e '^[[:space:]]+[0-9]+\) \[' -e '✘' -e '(CRITICAL|HIGH): [0-9]+' \
			-e '^Vulnerability #' -e 'severity vulnerabilit' |
		sed "s|^|[$name] |" | head -n 40 >>"$work/tests" || :
	{
		printf '\n<details><summary>%s: last %s log lines (redacted)</summary>\n\n```text\n' "$name" "$log_lines"
		# The tail before the post-job cleanup steps holds the failure.
		# Redact the whole log before cutting it, so a multi-line private
		# key that starts above the tail is still recognized and masked.
		awk '/⭐ Run Post |🏁  Job /{ exit } { print }' "$work/log" | redact | tail -n "$log_lines"
		printf '```\n</details>\n'
	} >>"$work/logs.md"
done <"$work/failed"

{
	echo
	echo "### Failing tests and findings"
	if [ -s "$work/tests" ]; then
		echo '```text'
		sort -u "$work/tests" | head -n 60
		echo '```'
	else
		echo "None recognized in the logs; see the log tails below."
	fi
	cat "$work/logs.md"
	echo
	echo "_Filed by scripts/ci/report-failure.sh. This workflow does not block deploys; close this issue once it is fixed._"
} >>"$work/body.md"

if [ "${REPORT_DRY_RUN:-}" = 1 ]; then
	printf '# %s\n\n' "$title"
	cat "$work/body.md"
	exit 0
fi

# open_issues: the numbers of the open issues titled exactly $title, oldest
# first. A failed lookup fails the reporter rather than filing a duplicate.
query=$(jq -rn --arg t "$title" '$t | @uri')
# The search also matches other titles, so every page is read: until an
# empty page, which also copes with a server that caps limit below 50.
open_issues() {
	: >"$work/matches"
	page=1
	while :; do
		call GET "issues?state=open&type=issues&limit=50&page=$page&q=$query" >"$work/issues.json"
		count=$(jq -er 'if type == "array" then length else error("not a list") end' "$work/issues.json")
		[ "$count" -gt 0 ] || break
		jq -r --arg t "$title" '.[] | select(.title == $t and (.pull_request == null)) | .number' \
			"$work/issues.json" >>"$work/matches"
		page=$((page + 1))
		if [ "$page" -gt 200 ]; then
			echo "more than 199 pages of open issues match \"$title\"; giving up" >&2
			exit 1
		fi
	done
	sort -n -u "$work/matches"
}

comment() {
	jq -n --rawfile body "$work/body.md" '{body: ("Failed again.\n\n" + $body)}' >"$work/request.json"
	call POST "issues/$1/comments" "$work/request.json" >/dev/null
	echo "Commented on issue #$1: $GITHUB_SERVER_URL/$GITHUB_REPOSITORY/issues/$1"
}

# Reuse the open issue with exactly this title, if there is one.
open_issues >"$work/open"
number=$(head -n 1 "$work/open")
if [ -n "$number" ]; then
	comment "$number"
	exit 0
fi

jq -n --arg title "$title" --rawfile body "$work/body.md" '{title: $title, body: $body}' >"$work/request.json"
call POST issues "$work/request.json" >"$work/created.json"
number=$(jq -r '.number // empty' "$work/created.json")
[ -n "$number" ] || { echo "the created issue has no number: $(cat "$work/created.json")" >&2; exit 1; }
echo "Opened issue #$number: $GITHUB_SERVER_URL/$GITHUB_REPOSITORY/issues/$number"

# Lookup and creation are not atomic: two failing runs with the same title
# (say a tag scan and the nightly one) can both open an issue. Every racer
# then sees the same oldest open issue, so the newer ones fold their report
# into it and close themselves, leaving exactly one open issue.
open_issues >"$work/open"
oldest=$(head -n 1 "$work/open")
if [ -n "$oldest" ] && [ "$oldest" != "$number" ]; then
	comment "$oldest"
	jq -n --arg body "Duplicate of #$oldest, which another run opened at the same time; the report was added there." \
		'{body: $body}' >"$work/request.json"
	call POST "issues/$number/comments" "$work/request.json" >/dev/null
	echo '{"state":"closed"}' >"$work/request.json"
	call PATCH "issues/$number" "$work/request.json" >/dev/null
	echo "Closed #$number as a duplicate of #$oldest."
fi
