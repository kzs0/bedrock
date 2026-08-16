#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
bench_dir="$repo_root/bench"
snapshot=$(mktemp -d)
go_command=${GO:-go}

cp "$bench_dir/go.mod" "$snapshot/go.mod"
cp "$bench_dir/go.sum" "$snapshot/go.sum"
restore() {
  cp "$snapshot/go.mod" "$bench_dir/go.mod"
  cp "$snapshot/go.sum" "$bench_dir/go.sum"
  rm -rf "$snapshot"
}
trap restore EXIT

(cd "$bench_dir" && "$go_command" mod tidy)

status=0
if ! diff -u "$snapshot/go.mod" "$bench_dir/go.mod"; then
  status=1
fi
if ! diff -u "$snapshot/go.sum" "$bench_dir/go.sum"; then
  status=1
fi
if (( status != 0 )); then
  echo "bench/go.mod and bench/go.sum are not tidy; run 'cd bench && go mod tidy'." >&2
  exit "$status"
fi
