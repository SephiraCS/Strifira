#!/usr/bin/env bash

cd "$(dirname "$0")/.."

PIDFILE=".dev.pids"

# Ensure Docker CLI is in PATH (macOS Docker Desktop)
export PATH="/Applications/Docker.app/Contents/Resources/bin:$PATH"

echo "=== Strifira - Stopping ==="

# Kill dev processes
if [ -f "$PIDFILE" ]; then
    while read -r pid; do
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null && echo "Stopped process $pid"
        fi
    done < "$PIDFILE"
    rm -f "$PIDFILE"
fi

# Kill any leftover air/vite processes
pkill -f "air" 2>/dev/null && echo "Stopped air" || true
pkill -f "vite.*strifira" 2>/dev/null && echo "Stopped vite" || true

# Stop Docker services
docker compose stop 2>/dev/null && echo "Stopped Postgres" || true

echo "Done."
