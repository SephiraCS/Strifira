#!/bin/sh
# Refuses a TEST_DATABASE_URL that names the application database.
#
#   scripts/ci/check-test-database.sh
#
# Run from the repository root by `make test-migrate` before it migrates
# TEST_DATABASE_URL and the backend suites write to it. The application
# database is DATABASE_URL in ./.env; without one (CI has no .env and
# points both variables at one throwaway database) only TEST_DATABASE_URL
# is required. The comparison is BE/cmd/checktestdb, which resolves both
# strings the way pgx does and prints only host:port/database.
set -eu

: "${TEST_DATABASE_URL:?TEST_DATABASE_URL is not set: point it at a test database (never the app database).}"
[ -f .env ] || exit 0
# shellcheck disable=SC2016 # expanded by the inner shell, after .env
app=$(env -u DATABASE_URL sh -c 'set -a && . ./.env && printf "%s" "${DATABASE_URL:-}"')
[ -n "$app" ] || exit 0
cd BE
APP_DATABASE_URL=$app exec go run ./cmd/checktestdb
