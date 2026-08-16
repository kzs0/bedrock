#!/usr/bin/env bash
set -euo pipefail

profile=${1:-coverage.out}
minimum=${2:-80.0}

if [[ ! -s "$profile" ]]; then
  echo "Coverage profile $profile is missing or empty." >&2
  exit 1
fi

total=$(go tool cover -func="$profile" | awk '/^total:/ { gsub(/%/, "", $3); print $3 }')
if [[ -z "$total" ]]; then
  echo "Could not read aggregate coverage from $profile." >&2
  exit 1
fi

awk -v total="$total" -v minimum="$minimum" 'BEGIN {
  if (total + 0 < minimum + 0) {
    printf "Aggregate coverage %.1f%% is below the required %.1f%%.\n", total, minimum > "/dev/stderr"
    exit 1
  }
  printf "Aggregate coverage %.1f%% meets the required %.1f%%.\n", total, minimum
}'
