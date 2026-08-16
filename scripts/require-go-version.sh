#!/usr/bin/env bash
set -euo pipefail

minimum=${1:?usage: require-go-version.sh MAJOR.MINOR}
go_command=${GO:-go}
current=$("$go_command" env GOVERSION)

if [[ $current == devel* ]]; then
  exit 0
fi
if [[ ! $current =~ ^go([0-9]+)\.([0-9]+)(\.[0-9]+)?([a-z].*)?$ ]]; then
  echo "Could not determine the active Go version from '$current'." >&2
  exit 1
fi
current_major=${BASH_REMATCH[1]}
current_minor=${BASH_REMATCH[2]}

if [[ ! $minimum =~ ^([0-9]+)\.([0-9]+)$ ]]; then
  echo "Invalid minimum Go version '$minimum'; expected MAJOR.MINOR." >&2
  exit 1
fi
minimum_major=${BASH_REMATCH[1]}
minimum_minor=${BASH_REMATCH[2]}

if (( current_major < minimum_major || current_major == minimum_major && current_minor < minimum_minor )); then
  echo "govulncheck requires Go $minimum or newer; active toolchain is $current." >&2
  echo "Install a newer Go toolchain and rerun 'make vuln'; automatic toolchain switching is disabled for this target." >&2
  exit 1
fi
