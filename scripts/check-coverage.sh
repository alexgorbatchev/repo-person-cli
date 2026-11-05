#!/usr/bin/env bash
set -euo pipefail

mkdir -p .tmp
run_dir=$(mktemp -d "$PWD/.tmp/coverage.XXXXXX")
test_pid=
cleanup() {
  if [[ -n "$test_pid" ]]; then
    kill -TERM -- "-$test_pid" 2>/dev/null || true
    # Bound graceful shutdown before removing files still used by test children.
    for ((attempt = 0; attempt < 50; attempt++)); do
      kill -0 -- "-$test_pid" 2>/dev/null || break
      sleep 0.1
    done
    kill -KILL -- "-$test_pid" 2>/dev/null || true
    wait "$test_pid" 2>/dev/null || true
  fi
  rm -rf "$run_dir"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# Job control gives the test command its own process group on Linux and macOS.
set -m
TMPDIR="$run_dir" GOTMPDIR="$run_dir" go test -race -coverprofile="$run_dir/coverage.out" ./... &
test_pid=$!
wait "$test_pid"
set +m

awk '
  NR > 1 { total += $2; if ($3 > 0) covered += $2 }
  END {
    if (total == 0) { print "No statement coverage data found"; exit 1 }
    printf "Statement coverage: %.2f%% (minimum: 90%%)\n", 100 * covered / total
    if (100 * covered < 90 * total) exit 1
  }
' "$run_dir/coverage.out"
