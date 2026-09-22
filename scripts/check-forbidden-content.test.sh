#!/usr/bin/env bash
# Self-test for the forbidden content scanner. Every fixture builds its offending
# bytes from escapes, including the attribution footers, so that this file stays
# clean under the scanner it is testing.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
scanner="${here}/check-forbidden-content.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

failures=0

expect() {
  local want="$1" name="$2" file="$3"
  local got=0
  "${scanner}" --files "${file}" >/dev/null 2>&1 || got=$?
  if [ "${got}" -ne "${want}" ]; then
    echo "FAIL ${name}: expected exit ${want}, got ${got}"
    failures=$((failures + 1))
  else
    echo "ok   ${name}"
  fi
}

printf 'package main\n\n// A plain comment with no forbidden content.\n' > "${tmp}/clean.go"
expect 0 "clean file passes" "${tmp}/clean.go"

printf 'title: build passing \xf0\x9f\x9a\x80\n' > "${tmp}/emoji.md"
expect 1 "emoji is rejected" "${tmp}/emoji.md"

printf 'A sentence \xe2\x80\x94 interrupted by an em dash.\n' > "${tmp}/emdash.md"
expect 1 "em dash is rejected" "${tmp}/emdash.md"

printf 'feat: add thing\n\nCo-\x61uthored-by: Someone <someone@example.com>\n' > "${tmp}/coauthor.txt"
expect 1 "author attribution footer is rejected" "${tmp}/coauthor.txt"

printf 'feat: add thing\n\nGener\x61ted with some tool\n' > "${tmp}/generated.txt"
expect 1 "tool attribution footer is rejected" "${tmp}/generated.txt"

printf 'A check mark \xe2\x9c\x85 in documentation.\n' > "${tmp}/checkmark.md"
expect 1 "dingbat emoji is rejected" "${tmp}/checkmark.md"

printf 'An en dash \xe2\x80\x93 is allowed, and a hyphen - is allowed.\n' > "${tmp}/allowed.md"
expect 0 "en dash and hyphen are allowed" "${tmp}/allowed.md"

# A captured profile and a compiled asset are tracked files. Decoding them as
# text produced thousands of warnings on the same stream the violations are
# reported on, which is where a real one would be missed.
printf 'binary\x00\xe2\x80\x94payload\n' > "${tmp}/profile.pprof"
expect 0 "a binary file is skipped" "${tmp}/profile.pprof"

if [ "${failures}" -ne 0 ]; then
  echo "${failures} scanner self-test(s) failed"
  exit 1
fi
echo "all scanner self-tests passed"
