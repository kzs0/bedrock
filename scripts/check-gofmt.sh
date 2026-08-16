#!/usr/bin/env bash
set -euo pipefail

go_files=$(mktemp)
trap 'rm -f "$go_files"' EXIT

find . -type f -name '*.go' \
  -not -path './.git/*' \
  -not -path './.codex/*' \
  -print0 >"$go_files"
unformatted=$(xargs -0 gofmt -l <"$go_files")
if [[ -n "$unformatted" ]]; then
  echo "The following Go files are not gofmt-formatted:" >&2
  echo "$unformatted" >&2
  exit 1
fi
