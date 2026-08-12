#!/bin/sh
# scripts/bench.sh — end-to-end latency harness for jog's hot path.
#
# Measures what a user actually feels at the prompt: the full
# `jog shell-hook` round trip — binary startup plus git spawns — on a
# generated many-file repo, with `git status` as the floor no snapshot
# design can beat. Complements the in-process numbers from
# `go test ./internal/snap -run TestNoOpPerf -v`, which can't see
# process startup, and the spawn counts gated by TestSpawnBudget.
#
# Usage: scripts/bench.sh [file-count]     (default 5000)
#
# Uses hyperfine when installed; otherwise a plain timed loop (GNU date).
set -eu

FILES=${1:-5000}
ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT INT TERM

if ! command -v hyperfine >/dev/null; then
    case $(date +%N) in *N*)
        echo "bench.sh: need hyperfine or GNU date for timing" >&2; exit 1 ;;
    esac
fi

# Pin the environment the way the tests do — the numbers must reflect
# jog, not whatever fsmonitor/untrackedCache the host's config enables.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=bench GIT_AUTHOR_EMAIL=bench@example.com
export GIT_COMMITTER_NAME=bench GIT_COMMITTER_EMAIL=bench@example.com

echo "building jog..."
(cd "$ROOT" && go build -o "$WORK/jog" ./cmd/jog)
JOG=$WORK/jog

echo "generating a $FILES-file repo..."
REPO=$WORK/repo
mkdir "$REPO" && cd "$REPO"
git init -q -b main
# The maintenance spawns that ride shell-hook (auto-trim, update check)
# are cadence-gated background forks — off, so runs are identical.
git config jog.updateCheck false
git config jog.autoTrim false
i=0
while [ "$i" -lt "$FILES" ]; do
    d=src/$((i % 26))
    [ -d "$d" ] || mkdir -p "$d"
    printf 'content %s\n' "$i" >"$d/f$i.txt"
    i=$((i + 1))
done
# Backdate everything: files whose mtime shares the index write's second
# are "racily clean" and git re-reads their contents on every status
# (~4x slower). Real repos self-heal over time; a mass-created one must
# model the healthy steady state explicitly.
find src -type f -exec touch -t 202601010000 '{}' +
git add -A
git commit -q -m 'many files'
git --no-optional-locks status --porcelain >/dev/null # warm the caches

run() { # run <label> <cmd> [args...]
    label=$1
    shift
    if command -v hyperfine >/dev/null; then
        hyperfine -n "$label" --warmup 3 "$*"
    else
        n=0
        start=$(date +%s%N)
        while [ "$n" -lt 20 ]; do
            "$@" >/dev/null
            n=$((n + 1))
        done
        end=$(date +%s%N)
        echo "$label: $(((end - start) / 20000000)) ms/run (mean of 20; install hyperfine for statistics)"
    fi
}

echo
echo "--- git status baseline"
run "git status" git --no-optional-locks status --porcelain

echo
echo "--- clean no-op (status fast path: 3 spawns)"
"$JOG" shell-hook -- "bench warmup"
run "shell-hook clean" "$JOG" shell-hook -- "bench"

echo
echo "--- dirty-but-unchanged no-op (the hook hot path: 5 spawns)"
printf 'uncommitted\n' >wip.txt
"$JOG" shell-hook -- "bench warmup" # mints the snapshot; the rest no-op
run "shell-hook dirty" "$JOG" shell-hook -- "bench"
