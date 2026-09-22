#!/usr/bin/env bash
# Prints the package directories affected by the given Go files, restricted to
# packages that contain at least one non-test source file.
#
# The restriction exists because this repository commits a failing test before the
# implementation it describes. A package that holds only _test.go files cannot be
# vetted or linted in that state: the tooling reports "no non-test Go files" or an
# undefined symbol, which would make the red step of the loop unreachable without
# bypassing the hook. Compilable code is still covered, because the package is
# analyzed again on the commit that adds the implementation, and `go build ./...`
# in the pre-push hook covers the whole tree.
set -euo pipefail

declare -A seen=()
for file in "$@"; do
  case "${file}" in
    *.go) ;;
    *) continue ;;
  esac
  dir="$(dirname "${file}")"
  [ -d "${dir}" ] || continue
  [ -n "${seen[${dir}]:-}" ] && continue
  seen["${dir}"]=1
  if compgen -G "${dir}/*.go" > /dev/null; then
    for candidate in "${dir}"/*.go; do
      case "${candidate}" in
        *_test.go) continue ;;
      esac
      printf './%s\n' "${dir#./}"
      break
    done
  fi
done
