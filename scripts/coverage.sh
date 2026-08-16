#!/usr/bin/env bash
set -euo pipefail

minimum=${1:-80.0}
profile=${2:-coverage.out}
packages=$(mktemp)
trap 'rm -f "$packages"' EXIT

# The example package is a runnable demonstration, not library production code.
go list ./... | awk '$0 !~ /\/example(\/|$)/' >"$packages"
xargs go test -count=1 -covermode=atomic -coverprofile="$profile" <"$packages"
./scripts/enforce-coverage.sh "$profile" "$minimum"
