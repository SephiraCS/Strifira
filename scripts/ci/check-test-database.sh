#!/bin/sh
# Refuses a TEST_DATABASE_URL that names the application database.
#
#   scripts/ci/check-test-database.sh
#
# Run by `make test-migrate` before it migrates TEST_DATABASE_URL and the
# backend suites write to it. The application database is DATABASE_URL in
# ./.env (CI has no .env and points both variables at one throwaway
# database, which is fine). Two URLs name the same database when host, port
# and database name match; credentials and query parameters are ignored,
# the port defaults to 5432, and localhost, 127.0.0.1 and [::1] are one host.
# Keyword DSNs (host=... dbname=...) are compared verbatim.
set -eu

: "${TEST_DATABASE_URL:?TEST_DATABASE_URL is not set: point it at a test database (never the app database).}"
[ -f .env ] || exit 0
# shellcheck disable=SC2016 # expanded by the inner shell, after .env
app=$(env -u DATABASE_URL sh -c 'set -a && . ./.env && printf "%s" "${DATABASE_URL:-}"')
[ -n "$app" ] || exit 0

# identity <url>: host:port/database, lowercase host.
identity() {
	case "$1" in
	*://*) ;;
	*) printf '%s\n' "$1"; return ;;
	esac
	printf '%s\n' "$1" | sed -E \
		-e 's#^[A-Za-z][A-Za-z0-9+.-]*://##' \
		-e 's#[?\#].*$##' \
		-e 's#^[^/]*@##' \
		-e 's#^([^/]*)/*$#\1/#' |
		awk -F/ '{
			host = tolower($1); db = substr($0, length($1) + 2)
			port = 5432
			if (host ~ /^\[.*\](:[0-9]+)?$/) {
				if (match(host, /\]:[0-9]+$/)) { port = substr(host, RSTART + 2); host = substr(host, 1, RSTART) }
			} else if (match(host, /:[0-9]+$/)) {
				port = substr(host, RSTART + 1); host = substr(host, 1, RSTART - 1)
			}
			if (host == "" || host == "localhost" || host == "127.0.0.1" || host == "[::1]") host = "loopback"
			print host ":" port "/" db
		}'
}

if [ "$(identity "$TEST_DATABASE_URL")" = "$(identity "$app")" ]; then
	echo "TEST_DATABASE_URL names the application database from .env ($(identity "$app")); the backend suites write to it. Point TEST_DATABASE_URL at a separate test database." >&2
	exit 1
fi
