#!/usr/bin/env bash
set -euo pipefail

tag=${1:?usage: validate-release-tag.sh TAG}
semver_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+)(\.[0-9A-Za-z-]+)*)?$'
if [[ ! $tag =~ $semver_pattern ]]; then
  echo "Release tag is not a canonical semantic version: $tag" >&2
  exit 1
fi

major=${BASH_REMATCH[1]}
version=${tag#v}
if [[ $version == *-* ]]; then
  prerelease=${version#*-}
  IFS='.' read -r -a identifiers <<< "$prerelease"
  for identifier in "${identifiers[@]}"; do
    if [[ $identifier =~ ^[0-9]+$ && ${#identifier} -gt 1 && $identifier == 0* ]]; then
      echo "Numeric prerelease identifiers must not contain leading zeroes: $tag" >&2
      exit 1
    fi
  done
fi

module_path=$(awk '$1 == "module" { print $2; exit }' go.mod)
if [[ -z $module_path ]]; then
  echo "Could not read the module path from go.mod." >&2
  exit 1
fi
if (( major >= 2 )) && [[ $module_path != */v$major ]]; then
  echo "Release tag $tag requires module path suffix /v$major; found $module_path." >&2
  exit 1
fi
