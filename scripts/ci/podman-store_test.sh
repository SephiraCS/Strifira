#!/bin/sh
# Run the committed cleanup logic against private directories; never /cache.
set -eu
repo=$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)
sandbox=$(mktemp -d)
trap 'rm -rf -- "$sandbox"' EXIT
root="$sandbox/stores"
mkdir -p "$root" "$sandbox/bin" "$sandbox/outside"
# Substitute only the fixed root in a private copy, without adding a runtime
# override to the production deletion script.
test "$(sed -n '/^root=/p' "$repo/scripts/ci/podman-store.sh")" = 'root=/cache/ci-tmp'
sed "s@^root=/cache/ci-tmp\$@root=$root@" "$repo/scripts/ci/podman-store.sh" > "$sandbox/store.sh"
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
PODMAN_STORE="$store" sh "$sandbox/store.sh" remove
test ! -e "$store"
grep -Fx "$store/storage.conf" "$PODMAN_TEST_CALLS"
# Every job must declare a timeout below the cancellation sweep grace.
awk '
    /^[[:space:]]*runs-on:/ { jobs[FILENAME]++ }
    /^[[:space:]]*timeout-minutes:/ {
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
' "$repo"/.forgejo/workflows/*.yml
echo 'podman store cleanup checks passed'
