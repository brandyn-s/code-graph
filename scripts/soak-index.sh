#!/usr/bin/env bash
# soak-index.sh: index one repository N times in a row with a single
# code-graph binary (normally an AddressSanitizer build) and fail on any
# crash, sanitizer report, or unbounded database growth. This is the nightly
# .github/workflows/soak.yml lane; run it locally with
#
#   make soak                       # builds bin/code-graph under ASan first
#   scripts/soak-index.sh 50 bench/accuracy/synthetic/post-battery
#
# Environment:
#   CODE_GRAPH_BIN       binary to run (default bin/code-graph)
#   SOAK_LOG_DIR         where per-iteration stdout/stderr are kept (default:
#                        a temp dir removed on success)
#   SOAK_MAX_DB_GROWTH   fail when the final database is more than this many
#                        times the size after the first iteration (default 3)
#   ASAN_OPTIONS         default detect_leaks=0:halt_on_error=1 (Go's runtime
#                        confuses LeakSanitizer; ASan errors still abort)
set -euo pipefail

ITERATIONS="${1:-50}"
REPO="${2:-bench/accuracy/synthetic/post-battery}"
BINARY="${CODE_GRAPH_BIN:-bin/code-graph}"
MAX_GROWTH="${SOAK_MAX_DB_GROWTH:-3}"

[ -x "$BINARY" ] || { echo "soak: binary not found: $BINARY (run make build-asan)" >&2; exit 2; }
[ -d "$REPO" ] || { echo "soak: repository not found: $REPO" >&2; exit 2; }
case "$ITERATIONS" in ''|*[!0-9]*) echo "soak: iterations must be a number: $ITERATIONS" >&2; exit 2;; esac

WORK="$(mktemp -d)"
LOG_DIR="${SOAK_LOG_DIR:-$WORK/logs}"
mkdir -p "$LOG_DIR"
trap 'rm -rf "$WORK"' EXIT

export CODE_GRAPH_CACHE_DIR="$WORK/cache"
export ASAN_OPTIONS="${ASAN_OPTIONS:-detect_leaks=0:halt_on_error=1}"
mkdir -p "$CODE_GRAPH_CACHE_DIR"

REPO_ABS="$(cd "$REPO" && pwd)"
ARGS="$(printf '{"repo_path":"%s","mode":"full","force":true}' "$REPO_ABS")"

db_size() {
  local total=0 f
  for f in "$CODE_GRAPH_CACHE_DIR"/*.db; do
    [ -f "$f" ] || continue
    total=$((total + $(wc -c < "$f")))
  done
  echo "$total"
}

first_size=0
echo "soak: $ITERATIONS x index_repository($REPO_ABS) with $BINARY"
for i in $(seq 1 "$ITERATIONS"); do
  start="$(date +%s)"
  rc=0
  "$BINARY" cli index_repository "$ARGS" > "$LOG_DIR/iter-$i.out" 2> "$LOG_DIR/iter-$i.err" || rc=$?
  elapsed=$(( $(date +%s) - start ))
  if [ "$rc" -ne 0 ]; then
    echo "soak: iteration $i FAILED rc=$rc after ${elapsed}s; stderr:" >&2
    head -60 "$LOG_DIR/iter-$i.err" >&2
    echo "soak: logs in $LOG_DIR" >&2
    trap - EXIT
    exit 1
  fi
  if grep -Eq 'ERROR: (Address|Thread|Leak|UndefinedBehavior)Sanitizer|runtime error:|fatal error:|panic:' "$LOG_DIR/iter-$i.err" "$LOG_DIR/iter-$i.out"; then
    echo "soak: iteration $i produced a sanitizer/runtime report; stderr:" >&2
    grep -En 'Sanitizer|runtime error:|fatal error:|panic:' "$LOG_DIR/iter-$i.err" "$LOG_DIR/iter-$i.out" | head -20 >&2
    trap - EXIT
    exit 1
  fi
  size="$(db_size)"
  [ "$i" -eq 1 ] && first_size="$size"
  echo "soak: iteration $i ok in ${elapsed}s, db=${size} bytes"
done

final_size="$(db_size)"
if [ "$first_size" -gt 0 ] && [ "$final_size" -gt $((first_size * MAX_GROWTH)) ]; then
  echo "soak: database grew from $first_size to $final_size bytes over $ITERATIONS forced re-indexes (limit ${MAX_GROWTH}x)" >&2
  trap - EXIT
  exit 1
fi
echo "soak: PASS ($ITERATIONS iterations, db $first_size -> $final_size bytes)"
