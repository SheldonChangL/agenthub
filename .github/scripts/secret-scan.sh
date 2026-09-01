#!/usr/bin/env bash
#
# Fails when a credential pattern appears in tracked files.
#
# The patterns are deliberately high-confidence: a scan that cries wolf gets
# muted, and a muted scan protects nothing. Anything matched here is a shape
# that has essentially one meaning — a PEM private key block, a provider key
# with a fixed prefix — rather than "a long random-looking string", which every
# fingerprint, hash and base64 test vector in this repository would trip.
#
# Test fixtures are allowlisted by exact path, never by pattern. A wildcard that
# exempts, say, every *_test.go would exempt the next real key committed into
# one. Adding a path here is a reviewed decision recorded in the diff.

set -euo pipefail

# Exact paths exempt from the scan, with the reason each one is safe.
#
#  - this script: it contains the patterns themselves.
#
# The keys used by tests are generated at run time (ed25519.GenerateKey) or are
# obvious constants such as bytes.Repeat({0x5A}), so no test fixture needs an
# entry today. The mechanism exists so that adding one is explicit.
ALLOWLIST=(
  ".github/scripts/secret-scan.sh"
)

# name<TAB>regex. Extended regular expressions, matched case-sensitively.
PATTERNS=(
  $'PEM private key\t-----BEGIN( [A-Z]+)? PRIVATE KEY-----'
  $'PEM OpenSSH private key\t-----BEGIN OPENSSH PRIVATE KEY-----'
  $'AWS access key id\tAKIA[0-9A-Z]{16}'
  $'GitHub personal access token\tgh[pousr]_[A-Za-z0-9]{36,}'
  $'GitHub fine-grained token\tgithub_pat_[A-Za-z0-9_]{22,}'
  $'Slack token\txox[abprs]-[A-Za-z0-9-]{10,}'
  $'Google API key\tAIza[0-9A-Za-z_-]{35}'
  $'private key assignment\t(private|secret)_?key[[:space:]]*[:=][[:space:]]*"[A-Za-z0-9+/]{32,}={0,2}"'
)

# --self-test proves the patterns still match what they name. A scan whose
# regexes have rotted reports a clean tree for the same reason an empty tree
# does, and the two are indistinguishable from the outside. Every pattern above
# must fire on a synthetic sample, so "no findings" means the scan looked.
if [ "${1:-}" = "--self-test" ]; then
  probe=$(mktemp)
  trap 'rm -f "$probe"' EXIT
  failures=0
  for entry in "${PATTERNS[@]}"; do
    name="${entry%%$'\t'*}"
    regex="${entry#*$'\t'}"
    case "$name" in
      "PEM private key")                 sample='-----BEGIN RSA PRIVATE KEY-----' ;;
      "PEM OpenSSH private key")         sample='-----BEGIN OPENSSH PRIVATE KEY-----' ;;
      "AWS access key id")               sample='AKIAABCDEFGHIJKLMNOP' ;;
      "GitHub personal access token")    sample="ghp_$(printf 'a%.0s' $(seq 36))" ;;
      "GitHub fine-grained token")       sample="github_pat_$(printf 'b%.0s' $(seq 22))" ;;
      "Slack token")                     sample='xoxb-1234567890abcdef' ;;
      "Google API key")                  sample="AIza$(printf 'c%.0s' $(seq 35))" ;;
      "private key assignment")          sample="private_key = \"$(printf 'd%.0s' $(seq 40))\"" ;;
      *)
        echo "self-test has no sample for pattern: ${name}"
        failures=1
        continue
        ;;
    esac
    printf '%s\n' "$sample" > "$probe"
    if ! grep -qE -e "$regex" "$probe"; then
      echo "pattern no longer matches its own sample: ${name}"
      failures=1
    fi
  done
  if [ "$failures" -ne 0 ]; then
    echo "secret scan self-test FAILED"
    exit 1
  fi
  echo "secret scan self-test: all ${#PATTERNS[@]} patterns matched their samples"
  exit 0
fi

is_allowlisted() {
  local candidate="$1" entry
  for entry in "${ALLOWLIST[@]}"; do
    [ "$candidate" = "$entry" ] && return 0
  done
  return 1
}

status=0
while IFS= read -r file; do
  is_allowlisted "$file" && continue
  # Skip anything git considers binary: a match inside one is noise, and the
  # scan reports line numbers that would not mean anything.
  if ! grep -Iq . "$file" 2>/dev/null; then
    continue
  fi
  for entry in "${PATTERNS[@]}"; do
    name="${entry%%$'\t'*}"
    regex="${entry#*$'\t'}"
    if matches=$(grep -nE -e "$regex" "$file" 2>/dev/null); then
      echo "possible ${name} in ${file}:"
      echo "$matches" | sed 's/^/    /'
      status=1
    fi
  done
done < <(git ls-files)

if [ "$status" -eq 0 ]; then
  echo "secret scan: no credential patterns found in $(git ls-files | wc -l | tr -d ' ') tracked files"
fi
exit $status
