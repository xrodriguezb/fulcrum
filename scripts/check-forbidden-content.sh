#!/usr/bin/env bash
# Rejects content that must never enter this repository: emoji, the em dash
# (U+2014), and machine attribution footers. Enforcement lives here rather than in
# a review checklist because a rule nobody can forget is worth more than a rule
# everybody agrees with.
#
# Usage:
#   check-forbidden-content.sh                  scan staged changes (pre-commit)
#   check-forbidden-content.sh --all            scan every tracked file
#   check-forbidden-content.sh --files a b c    scan the named files
#   check-forbidden-content.sh --message FILE   scan a commit message file
set -euo pipefail

mode="staged"
declare -a targets=()

while [ $# -gt 0 ]; do
  case "$1" in
    --staged) mode="staged"; shift ;;
    --all) mode="all"; shift ;;
    --message) mode="files"; shift; targets+=("$1"); shift ;;
    --files) mode="files"; shift; while [ $# -gt 0 ]; do targets+=("$1"); shift; done ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) targets+=("$1"); mode="files"; shift ;;
  esac
done

# Generated and vendored files are excluded: their content is not authored here,
# and regenerating them is the only way to change them.
is_excluded() {
  case "$1" in
    web/src/api/schema.d.ts|*/node_modules/*|node_modules/*|vendor/*|*.png|*.jpg|*.jpeg|*.gif|*.ico|*.woff|*.woff2|*.pdf) return 0 ;;
    *) return 1 ;;
  esac
}

case "${mode}" in
  staged)
    while IFS= read -r line; do
      [ -n "${line}" ] && targets+=("${line}")
    done < <(git diff --cached --name-only --diff-filter=ACMR)
    ;;
  all)
    while IFS= read -r line; do
      [ -n "${line}" ] && targets+=("${line}")
    done < <(git ls-files)
    ;;
esac

declare -a scan=()
for f in ${targets+"${targets[@]}"}; do
  [ -f "${f}" ] || continue
  is_excluded "${f}" && continue
  scan+=("${f}")
done

if [ ${#scan[@]} -eq 0 ]; then
  exit 0
fi

# Perl does the matching because BSD grep has no -P and no codepoint classes, and
# this hook has to behave identically on a developer laptop and on a CI runner.
PERL_FILES_JOINED=$(printf '%s\n' "${scan[@]}")
export PERL_FILES_JOINED
perl -e '
use strict;
use warnings;
use open qw(:std :encoding(UTF-8));

my @files = grep { length } split /\n/, ($ENV{PERL_FILES_JOINED} // q{});
my $violations = 0;

sub is_binary {
  my ($path) = @_;
  open my $probe, "<:raw", $path or return 0;
  read $probe, my $head, 8192;
  close $probe;
  return defined($head) && $head =~ /\0/;
}

my @rules = (
  { name => "emoji",         re => qr/[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{FE0F}\x{200D}\x{24C2}\x{3030}\x{303D}]/ },
  { name => "em dash U+2014", re => qr/\x{2014}/ },
  # The literals are spelled with escapes so this scanner does not flag its own
  # source when it walks the whole tree.
  { name => "attribution footer", re => qr/(?i:co-auth\x6fred-by|gener\x61ted with)/ },
);

for my $file (@files) {
  # A file with a NUL byte is not text, and decoding it as UTF-8 produces
  # hundreds of warnings per file on stderr. Violations are reported on stderr
  # too, so that noise is where a real one would go unnoticed.
  if (is_binary($file)) {
    next;
  }

  open my $fh, "<", $file or next;
  my $lineno = 0;
  while (my $line = <$fh>) {
    $lineno++;
    for my $rule (@rules) {
      next unless $line =~ $rule->{re};
      my $shown = $line;
      chomp $shown;
      $shown = substr($shown, 0, 120);
      printf STDERR "%s:%d: forbidden content (%s): %s\n", $file, $lineno, $rule->{name}, $shown;
      $violations++;
    }
  }
  close $fh;
}

if ($violations) {
  printf STDERR "\n%d violation(s). See section 0.7 of the build specification.\n", $violations;
  printf STDERR "Emoji, the em dash U+2014 and attribution footers are not permitted in this repository.\n";
  exit 1;
}
exit 0;
'
