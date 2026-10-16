#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

echo "=== Strifira - Reset Dev Environment ==="

export PATH="/Applications/Docker.app/Contents/Resources/bin:$PATH"

ENVIRONMENT_FROM_CALLER="${ENVIRONMENT-}"
DATABASE_URL_WAS_SET=0
if [ "${DATABASE_URL+x}" = "x" ]; then
    DATABASE_URL_WAS_SET=1
    DATABASE_URL_OVERRIDE="$DATABASE_URL"
fi

if [ -f .env ]; then
    set -a && source .env && set +a
fi

if [ "$DATABASE_URL_WAS_SET" = "1" ]; then
    export DATABASE_URL="$DATABASE_URL_OVERRIDE"
fi

refuse_production() {
    case "${1:-}" in
    [Pp][Rr][Oo][Dd] | [Pp][Rr][Oo][Dd][Uu][Cc][Tt][Ii][Oo][Nn])
        echo "FATAL: Refusing to reset the production database!" >&2
        exit 1
        ;;
    esac
}

refuse_production "$ENVIRONMENT_FROM_CALLER"
refuse_production "${ENVIRONMENT:-development}"

POSTGRES_USER="${POSTGRES_USER:-strifira}"
POSTGRES_DB="${POSTGRES_DB:-strifira}"
PSQL=(docker compose exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -q -v ON_ERROR_STOP=1)

echo "Starting postgres for safety checks..."
docker compose up -d --wait postgres

echo "Checking for non-destroyed dev machines..."
if [ "$("${PSQL[@]}" -tAc "SELECT to_regclass('public.dev_machines') IS NOT NULL")" = "t" ]; then
    MACHINE_COUNT=$("${PSQL[@]}" -tAc "SELECT COUNT(*) FROM dev_machines WHERE status <> 'destroyed'")
    if [ "$MACHINE_COUNT" != "0" ]; then
        echo "FATAL: $MACHINE_COUNT non-destroyed dev_machine(s) exist. Tear them down before reset to avoid Docker orphans." >&2
        exit 1
    fi
else
    echo "Dev Machine tables are not migrated yet; safety check skipped."
fi

echo "Proceeding with destructive reset..."
docker compose down -v --remove-orphans

echo "Starting postgres..."
docker compose up -d --wait postgres

echo "Running migrations..."
(cd BE && go run ./cmd/server migrate up)

echo "=== Reset complete. Run make seed next. ==="
