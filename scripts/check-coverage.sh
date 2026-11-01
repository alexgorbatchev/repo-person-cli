#!/usr/bin/env bash
set -euo pipefail
awk '
  NR > 1 { total += $2; if ($3 > 0) covered += $2 }
  END {
    if (total == 0) { print "No statement coverage data found"; exit 1 }
    printf "Statement coverage: %.2f%% (minimum: 90%%)\n", 100 * covered / total
    if (100 * covered < 90 * total) exit 1
  }
' coverage.out
