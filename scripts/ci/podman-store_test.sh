#!/bin/sh
# Test cleanup against private directories, and enforce job timeout policy.
# The committed deletion logic never touches /cache during these checks.
set -eu
repo=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
sandbox=$(mktemp -d)
trap 'rm -rf -- "$sandbox"' EXIT
root="$sandbox/stores"
mkdir -p "$root" "$sandbox/bin" "$sandbox/outside"
# Substitute only the fixed root in a private copy, without adding a runtime
# override to the production deletion script.
test "$(sed -n '/^root=\/cache\/ci-tmp$/p' "$repo/scripts/ci/podman-store.sh")" = 'root=/cache/ci-tmp'
ln -s "$root" "$sandbox/store-alias"
sed "s@^root=/cache/ci-tmp\$@root=$sandbox/store-alias@" "$repo/scripts/ci/podman-store.sh" > "$sandbox/store.sh"
cat > "$sandbox/bin/podman" <<'STUB'
#!/bin/sh
printf '%s\n' "$CONTAINERS_STORAGE_CONF" >> "$PODMAN_TEST_CALLS"
STUB
chmod +x "$sandbox/bin/podman"
export PATH="$sandbox/bin:$PATH" PODMAN_TEST_CALLS="$sandbox/calls"
export GITHUB_RUN_ID=123 GITHUB_ENV="$sandbox/env"
sh "$sandbox/store.sh" create test
store="$root/ku-123-test"
test "$(wc -l < "$GITHUB_ENV")" -eq 3
grep -Fx "CONTAINERS_STORAGE_CONF=$store/storage.conf" "$GITHUB_ENV"
grep -Fx "CONTAINERS_CONF_OVERRIDE=$store/containers.conf" "$GITHUB_ENV"
grep -Fx "PODMAN_STORE=$store" "$GITHUB_ENV"
touch "$store/cancelled-attempt"
sh "$sandbox/store.sh" create test
test ! -e "$store/cancelled-attempt"
# An interrupted create can leave a directory before either config is written.
rm "$store/storage.conf" "$store/containers.conf"
touch "$store/torn-attempt"
sh "$sandbox/store.sh" create test
test ! -e "$store/torn-attempt"
for name in old young unmarked; do mkdir -p "$root/ku-$name"; done
touch "$root/ku-old/storage.conf" "$root/ku-young/storage.conf"
touch -d '7 hours ago' "$root/ku-old" "$root/ku-unmarked"
sh "$sandbox/store.sh" create sweep
test ! -e "$root/ku-old"
test -d "$root/ku-young"
test ! -e "$root/ku-unmarked"
mkdir -p "$root/ku-unmarked"
touch "$sandbox/outside/keep"
rm -rf -- "$store"
ln -s "$sandbox/outside" "$store"
sh "$sandbox/store.sh" create test
test ! -L "$store"
test -f "$sandbox/outside/keep"
ln -s "$sandbox/outside" "$root/ku-stale-link"
touch -h -d '7 hours ago' "$root/ku-stale-link"
sh "$sandbox/store.sh" create sweep
test ! -L "$root/ku-stale-link"
test -f "$sandbox/outside/keep"
ln -s "$sandbox/outside" "$root/ku-link"
mkdir -p "$store/nested"
for unsafe in "$sandbox/outside" "$store/../../outside" "$root/ku-link" "$store/nested" "$root/ku-unmarked"; do
    if PODMAN_STORE="$unsafe" sh "$sandbox/store.sh" remove; then
        echo "accepted unsafe store: $unsafe" >&2; exit 1
    fi
done
test -f "$sandbox/outside/keep"
test ! -e "$PODMAN_TEST_CALLS"
for name in '../escape' 'name/subdir' '.' '..'; do
    if sh "$sandbox/store.sh" create "$name"; then echo 'accepted unsafe name' >&2; exit 1; fi
done
if GITHUB_RUN_ID='../escape' sh "$sandbox/store.sh" create test; then exit 1; fi
# Remove is warn-only on actual deletion failures; create must refuse reset.
PODMAN_TEST_REAL_RM=$(command -v rm)
export PODMAN_TEST_REAL_RM
cat > "$sandbox/bin/rm" <<'STUB'
#!/bin/sh
for argument do
    [ "$argument" != "${PODMAN_TEST_FAIL_DELETE:-}" ] || exit 9
done
exec "$PODMAN_TEST_REAL_RM" "$@"
STUB
chmod +x "$sandbox/bin/rm"
PODMAN_TEST_FAIL_DELETE="$store" PODMAN_STORE="$store" sh "$sandbox/store.sh" remove
test -d "$store"
if PODMAN_TEST_FAIL_DELETE="$store" sh "$sandbox/store.sh" create test; then
    echo 'create accepted a failed store reset' >&2; exit 1
fi
PODMAN_STORE="$store" sh "$sandbox/store.sh" remove
test ! -e "$store"
grep -Fx "$store/storage.conf" "$PODMAN_TEST_CALLS"
# Every job must declare a timeout below the cancellation sweep grace.
set --
for workflow in "$repo"/.forgejo/workflows/*.yml "$repo"/.forgejo/workflows/*.yaml; do
    [ ! -f "$workflow" ] || set -- "$@" "$workflow"
done
[ "$#" -gt 0 ] || { echo 'no workflows found' >&2; exit 1; }
awk '
    /^    runs-on:/ { jobs[FILENAME]++ }
    /^    timeout-minutes:/ {
        limits[FILENAME]++
        if ($2 !~ /^[0-9]+$/ || $2 >= 360) {
            printf "invalid timeout-minutes %s in %s: expected a number below 360\n", $2, FILENAME > "/dev/stderr"
            failed = 1
        }
    }
    END {
        for (file in jobs) if (jobs[file] != limits[file]) {
            printf "every job in %s must declare timeout-minutes\n", file > "/dev/stderr"
            failed = 1
        }
        exit failed
    }
' "$@"
echo 'podman store cleanup checks passed'
