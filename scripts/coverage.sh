#!/usr/bin/env bash
set -euo pipefail

minimum=${1:-80.0}
profile=${2:-coverage.out}
GO=${GO:-go}
packages=$(mktemp)
trap 'rm -f "$packages"' EXIT

# Exclude runnable examples from library coverage.
"$GO" list ./... | awk '$0 !~ /\/example(\/|$)/' >"$packages"
coverpkg=$(paste -sd, "$packages")
xargs "$GO" test -count=1 -coverpkg="$coverpkg" -covermode=atomic -coverprofile="$profile" <"$packages"
python3 ./scripts/coverage-inventory.py "$profile" > "${profile%.out}-inventory.md"
./scripts/enforce-coverage.sh "$profile" "$minimum"
