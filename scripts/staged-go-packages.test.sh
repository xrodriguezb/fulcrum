#!/usr/bin/env bash
# Self-test for the staged package selector.
#
# The selector feeds go vet and golangci-lint in the pre-commit hook, and those
# callers read its standard output. A selector that fails therefore looks exactly
# like a commit with nothing to analyse, so the cases below assert the exit status
# as well as the output, and they run under whichever bash is on the PATH, which
# on a macOS machine is 3.2.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
selector="${here}/staged-go-packages.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

failures=0

expect() {
  local name="$1" want="$2"
  shift 2
  local got status=0
  got="$("${selector}" "$@" 2>/dev/null)" || status=$?
  if [ "${status}" -ne 0 ]; then
    echo "FAIL ${name}: the selector exited ${status}"
    failures=$((failures + 1))
  elif [ "${got}" != "${want}" ]; then
    echo "FAIL ${name}: expected [${want}], got [${got}]"
    failures=$((failures + 1))
  else
    echo "ok   ${name}"
  fi
}

# The hook passes the paths git reports, which are relative to the repository
# root, so the cases use relative paths too.
mkdir -p "${tmp}/service" "${tmp}/worker" "${tmp}/redstep" "${tmp}/docs"
printf 'package service\n' > "${tmp}/service/service.go"
printf 'package service\n' > "${tmp}/service/service_test.go"
printf 'package worker\n' > "${tmp}/worker/worker.go"
printf 'package redstep\n' > "${tmp}/redstep/redstep_test.go"
printf 'notes\n' > "${tmp}/docs/notes.md"
cd "${tmp}"

expect "a package with source is printed" "./service" "service/service.go"

expect "a package is printed once for several files" "./service" \
  "service/service.go" "service/service_test.go"

expect "a package holding only tests is skipped" "" "redstep/redstep_test.go"

expect "files that are not go are ignored" "" "docs/notes.md"

expect "a path that no longer exists is ignored" "" "gone/gone.go"

expect "no arguments produces no output" ""

expect "every affected package is printed" "./service
./worker" "service/service.go" "worker/worker.go"

if [ "${failures}" -ne 0 ]; then
  echo "${failures} selector test(s) failed"
  exit 1
fi
echo "staged package selector: all cases passed"
