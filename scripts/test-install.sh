#!/usr/bin/env bash
# Exercise install.sh without a network and without touching this machine.
#
# Two kinds of check:
#
#   1. --dry-run under a faked `uname`, so the darwin path can be read on Linux
#      and the linux path on a mac. What is asserted is the transcript: the
#      asset name for that platform, that the digest is checked before anything
#      is unpacked, that --prefix keeps every path inside the prefix, and that
#      no command it would run is a sudo.
#   2. A real install from a locally built archive, with a SHA256SUMS that is
#      right and then one that is wrong. The wrong one is the important half:
#      an installer whose verification can be skipped is an installer with no
#      verification, and that is not something a dry run can show.
#
# bash, not sh: this is a test harness and never ships. install.sh itself is
# POSIX sh, which `shellcheck -s sh` and `sh -n` in CI keep true.
set -euo pipefail

repository=$(cd -- "$(dirname -- "$0")/.." && pwd)
installer="$repository/install.sh"
[ -f "$installer" ] || {
	echo "no install.sh at $installer" >&2
	exit 2
}

work=$(mktemp -d "${TMPDIR:-/tmp}/agenthub-install-test.XXXXXX")
trap 'rm -rf "$work"' EXIT

failures=0
checks=0

fail() {
	failures=$((failures + 1))
	echo "  FAIL: $*" >&2
}

contains() { # contains <label> <haystack file> <needle>
	checks=$((checks + 1))
	if ! grep -qF -- "$3" "$2"; then
		fail "$1: expected to find \"$3\""
	fi
}

lacks() { # lacks <label> <haystack file> <needle>
	checks=$((checks + 1))
	if grep -qF -- "$3" "$2"; then
		fail "$1: did not expect \"$3\""
	fi
}

# fake_uname builds a PATH shim so install.sh can be asked what it would do on a
# machine this is not. Only `uname` is faked; everything else resolves normally.
fake_uname() { # fake_uname <system> <machine> -> prints a directory for PATH
	local system=$1 machine=$2
	local shim="$work/shim-$system-$machine"
	mkdir -p "$shim"
	cat >"$shim/uname" <<EOF
#!/bin/sh
case "\${1:-}" in
-s) echo "$system" ;;
-m) echo "$machine" ;;
*) echo "$system" ;;
esac
EOF
	chmod +x "$shim/uname"
	echo "$shim"
}

# fake_ah builds a PATH shim whose `ah` answers `--json service status` the way
# a real one would on a machine in the named state. install.sh asks that
# question before it re-registers the service, and a dry run has no installed
# ah of its own to ask, so this is the machine's existing one.
fake_ah() { # fake_ah <none|db|fail> [database path] -> prints a directory for PATH
	local mode=$1 db=${2:-}
	local shim="$work/ah-$mode"
	mkdir -p "$shim"
	case $mode in
	none)
		cat >"$shim/ah" <<'EOF'
#!/bin/sh
case "$*" in
*"service status"*) printf '{\n  "service": {\n    "Installed": false,\n    "Supported": true\n  }\n}\n' ;;
*) echo "ah (fake)" ;;
esac
EOF
		;;
	db)
		cat >"$shim/ah" <<EOF
#!/bin/sh
case "\$*" in
*"service status"*)
	printf '%s\n' '{' \\
		'  "service": {' \\
		'    "Arguments": [' \\
		'      "--db",' \\
		'      "$db"' \\
		'    ],' \\
		'    "Installed": true,' \\
		'    "Supported": true' \\
		'  }' \\
		'}'
	;;
*) echo "ah (fake)" ;;
esac
EOF
		;;
	fail)
		cat >"$shim/ah" <<'EOF'
#!/bin/sh
echo "ah: unknown flag --json" >&2
exit 2
EOF
		;;
	esac
	chmod +x "$shim/ah"
	echo "$shim"
}

# Every dry run gets an `ah` that reports no registered service, so the
# transcript is the same on a machine that has AgentHub installed and on one
# that does not. The cases that care put a different one in front of it.
no_service_ah=$(fake_ah none)

dry_run() { # dry_run <system> <machine> <output file> [args...]
	local system=$1 machine=$2 out=$3
	shift 3
	local shim
	shim=$(fake_uname "$system" "$machine")
	PATH="${AH_SHIM:-$no_service_ah}:$shim:$PATH" sh "$installer" --dry-run "$@" >"$out" 2>&1
}

# dry_run_fails is dry_run for the cases whose point is the refusal.
dry_run_fails() { # dry_run_fails <label> <system> <machine> <output file> [args...]
	local label=$1
	shift
	checks=$((checks + 1))
	if dry_run "$@"; then
		fail "$label: the run was expected to fail and did not"
	fi
}

# The commands a dry run would actually execute are the lines it prefixes with
# "+". Advice it prints (the WebKit hint names an apt command) is not one of
# them, and asserting over the whole transcript would confuse the two.
commands_only() { # commands_only <transcript> <destination>
	grep '^+ ' "$1" >"$2" || true
}

echo "== darwin/arm64, desktop =="
dry_run Darwin arm64 "$work/darwin.txt" --version v0.1.0 --prefix "$work/pfx"
contains "prefix install names its own app path" "$work/darwin.txt" "Open $work/pfx/agenthub-desktop.app; it starts on a setup checklist."
lacks "prefix install does not say Applications" "$work/darwin.txt" "from Applications"
commands_only "$work/darwin.txt" "$work/darwin.cmds"
contains darwin "$work/darwin.txt" "agenthub-desktop_v0.1.0_darwin_universal.dmg"
contains darwin "$work/darwin.txt" "releases/download/v0.1.0/SHA256SUMS"
contains darwin "$work/darwin.txt" "compared with agenthub-desktop_v0.1.0_darwin_universal.dmg"
contains darwin "$work/darwin.cmds" "hdiutil attach -nobrowse -readonly -quiet"
contains darwin "$work/darwin.cmds" "ditto"
contains darwin "$work/darwin.cmds" "xattr -dr com.apple.quarantine"
contains darwin "$work/darwin.cmds" "$work/pfx/agenthub-desktop.app"
contains darwin "$work/darwin.cmds" "ah service install"
contains darwin "$work/darwin.cmds" "$work/pfx/bin/ah"
lacks darwin "$work/darwin.cmds" "sudo"
lacks darwin "$work/darwin.txt" "/Applications/agenthub-desktop.app"
lacks darwin "$work/darwin.txt" "$HOME/.local/bin"
# The digest is checked before the disk image is attached, not after: the point
# of verifying is to not touch an unverified file.
checks=$((checks + 1))
if [ "$(grep -n 'compared with' "$work/darwin.txt" | cut -d: -f1 | head -n1)" -gt \
	"$(grep -n 'hdiutil attach' "$work/darwin.txt" | cut -d: -f1 | head -n1)" ]; then
	fail "darwin: the checksum is compared after the image is attached"
fi

echo "== darwin/x86_64, --cli-only --no-service --no-open =="
dry_run Darwin x86_64 "$work/darwin-cli.txt" --version v0.1.0 --cli-only --no-service --no-open --prefix "$work/pfx"
commands_only "$work/darwin-cli.txt" "$work/darwin-cli.cmds"
contains darwin-cli "$work/darwin-cli.txt" "agenthub_v0.1.0_darwin_amd64.tar.gz"
contains darwin-cli "$work/darwin-cli.cmds" "$work/pfx/bin/agenthub-mcp"
lacks darwin-cli "$work/darwin-cli.cmds" "service install"
lacks darwin-cli "$work/darwin-cli.cmds" "hdiutil"
lacks darwin-cli "$work/darwin-cli.cmds" "open "
lacks darwin-cli "$work/darwin-cli.cmds" "sudo"

echo "== linux/x86_64, desktop =="
dry_run Linux x86_64 "$work/linux.txt" --version v0.1.0 --prefix "$work/pfx"
commands_only "$work/linux.txt" "$work/linux.cmds"
contains linux "$work/linux.txt" "agenthub-desktop_v0.1.0_linux_amd64.tar.gz"
contains linux "$work/linux.txt" "compared with agenthub-desktop_v0.1.0_linux_amd64.tar.gz"
contains linux "$work/linux.cmds" "tar -xzf"
contains linux "$work/linux.cmds" "$work/pfx/share/agenthub"
contains linux "$work/linux.cmds" "$work/pfx/bin/agenthub-desktop"
contains linux "$work/linux.cmds" "ah service install"
lacks linux "$work/linux.cmds" "sudo"
lacks linux "$work/linux.cmds" "hdiutil"

echo "== linux/aarch64 falls back to the command line archive =="
dry_run Linux aarch64 "$work/linux-arm.txt" --version v0.1.0 --prefix "$work/pfx"
commands_only "$work/linux-arm.txt" "$work/linux-arm.cmds"
contains linux-arm "$work/linux-arm.txt" "no desktop build exists for linux arm64"
contains linux-arm "$work/linux-arm.txt" "agenthub_v0.1.0_linux_arm64.tar.gz"
lacks linux-arm "$work/linux-arm.txt" "agenthub-desktop_v0.1.0"
lacks linux-arm "$work/linux-arm.cmds" "sudo"

echo "== the latest release is looked up when no version is given =="
dry_run Linux x86_64 "$work/latest.txt" --prefix "$work/pfx"
contains latest "$work/latest.txt" "api.github.com/repos/SheldonChangL/agenthub/releases/latest"

echo "== --help and a bad flag =="
checks=$((checks + 1))
sh "$installer" --help >"$work/help.txt" 2>&1 || fail "--help exited non-zero"
contains help "$work/help.txt" "--cli-only"
contains help "$work/help.txt" "It never uses sudo."
checks=$((checks + 1))
if sh "$installer" --nonsense >"$work/bad.txt" 2>&1; then
	fail "an unknown flag was accepted"
fi
contains bad-flag "$work/bad.txt" "unknown option --nonsense"

# ---- real installs from a local archive, no network ------------------------

echo "== a real --from install, and a real refusal =="
staging="$work/stage/agenthub-desktop_v0.1.0_linux_amd64"
mkdir -p "$staging"
for binary in ah agenthub-node agenthub-mcp agenthub-desktop; do
	printf '#!/bin/sh\necho "%s v0.1.0 (fake)"\n' "$binary" >"$staging/$binary"
	chmod +x "$staging/$binary"
done
echo "fake" >"$staging/README.txt"
good="$work/good"
mkdir -p "$good"
tar -C "$work/stage" -czf "$good/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" \
	agenthub-desktop_v0.1.0_linux_amd64
(
	cd "$good"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum agenthub-desktop_v0.1.0_linux_amd64.tar.gz >SHA256SUMS
	else
		shasum -a 256 agenthub-desktop_v0.1.0_linux_amd64.tar.gz >SHA256SUMS
	fi
)

shim=$(fake_uname Linux x86_64)
target="$work/real"
PATH="$shim:$PATH" sh "$installer" \
	--from "$good/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" \
	--prefix "$target" --no-service --no-open >"$work/real.txt" 2>&1
contains real "$work/real.txt" "verified agenthub-desktop_v0.1.0_linux_amd64.tar.gz against SHA256SUMS"
for binary in ah agenthub-node agenthub-mcp agenthub-desktop; do
	checks=$((checks + 1))
	[ -x "$target/share/agenthub/$binary" ] || fail "real: $binary is missing from the install"
done
checks=$((checks + 1))
[ -L "$target/bin/ah" ] || fail "real: $target/bin/ah is not a symlink"
checks=$((checks + 1))
[ "$("$target/bin/ah")" = "ah v0.1.0 (fake)" ] || fail "real: the linked ah is not the installed one"

# Re-running upgrades in place rather than failing on what is already there.
PATH="$shim:$PATH" sh "$installer" \
	--from "$good/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" \
	--prefix "$target" --no-service --no-open >"$work/real2.txt" 2>&1
contains rerun "$work/real2.txt" "verified agenthub-desktop_v0.1.0_linux_amd64.tar.gz against SHA256SUMS"
checks=$((checks + 1))
[ -x "$target/share/agenthub/ah" ] || fail "rerun: the second install left no ah"

# A SHA256SUMS that does not match must stop the install, and must stop it
# before anything is unpacked.
bad="$work/bad"
mkdir -p "$bad"
cp "$good/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" "$bad/"
printf '%s  %s\n' \
	"0000000000000000000000000000000000000000000000000000000000000000" \
	"agenthub-desktop_v0.1.0_linux_amd64.tar.gz" >"$bad/SHA256SUMS"
refused="$work/refused"
checks=$((checks + 1))
if PATH="$shim:$PATH" sh "$installer" \
	--from "$bad/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" \
	--prefix "$refused" --no-service --no-open >"$work/bad-sums.txt" 2>&1; then
	fail "a corrupted SHA256SUMS was accepted"
fi
contains bad-sums "$work/bad-sums.txt" "checksum mismatch"
contains bad-sums "$work/bad-sums.txt" "Nothing was installed."
checks=$((checks + 1))
[ ! -e "$refused" ] || fail "the refused install still created $refused"

# A SHA256SUMS that simply has no line for this file is the same refusal: an
# unverifiable download is not a verified one.
missing="$work/missing"
mkdir -p "$missing"
cp "$good/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" "$missing/"
printf '%s  %s\n' \
	"0000000000000000000000000000000000000000000000000000000000000000" \
	"something-else.tar.gz" >"$missing/SHA256SUMS"
checks=$((checks + 1))
if PATH="$shim:$PATH" sh "$installer" \
	--from "$missing/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" \
	--prefix "$work/nope" --no-service --no-open >"$work/missing-sums.txt" 2>&1; then
	fail "a SHA256SUMS with no line for the file was accepted"
fi
contains missing-sums "$work/missing-sums.txt" "has no line for"

# ---- the existing service is carried, not overwritten ----------------------

# `ah service install` replaces the registration outright, and the database
# path lives nowhere else. A reinstall that does not carry --db forward gives
# the machine a new identity and drops every pairing, silently. These three
# are the whole contract: carry it, or do not touch the service at all.

echo "== an existing service keeps its database =="
AH_SHIM=$(fake_ah db /some/where.db)
dry_run Darwin arm64 "$work/svc-db.txt" --version v0.1.0 --prefix "$work/pfx"
unset AH_SHIM
commands_only "$work/svc-db.txt" "$work/svc-db.cmds"
contains svc-db "$work/svc-db.cmds" "service install --db /some/where.db"
contains svc-db "$work/svc-db.txt" "keeping the node's database at /some/where.db"
contains svc-db "$work/svc-db.txt" "the unit's node binary is replaced on purpose"

echo "== an unreadable service status installs no service =="
AH_SHIM=$(fake_ah fail)
dry_run Darwin arm64 "$work/svc-fail.txt" --version v0.1.0 --prefix "$work/pfx"
unset AH_SHIM
commands_only "$work/svc-fail.txt" "$work/svc-fail.cmds"
lacks svc-fail "$work/svc-fail.cmds" "service install"
contains svc-fail "$work/svc-fail.txt" "could not read the current background service"
contains svc-fail "$work/svc-fail.txt" "service install --db <path to the node's database>"
# The app is still installed: an unreadable service is a reason not to touch
# the service, not a reason to leave half an app behind.
contains svc-fail "$work/svc-fail.cmds" "ditto"

echo "== no registered service installs a plain one =="
dry_run Darwin arm64 "$work/svc-none.txt" --version v0.1.0 --prefix "$work/pfx"
commands_only "$work/svc-none.txt" "$work/svc-none.cmds"
contains svc-none "$work/svc-none.cmds" "service install"
lacks svc-none "$work/svc-none.cmds" "service install --db"

# ---- a truncated stream must run nothing -----------------------------------

# This is what `curl | sh` fails as: the connection drops and the shell is
# handed half a file. Everything with an effect is inside main(), and main is
# called on the last line, so half a file defines functions and calls none.
echo "== a truncated script runs nothing =="
installer_bytes=$(wc -c <"$installer")
installer_lines=$(wc -l <"$installer")
truncate_at() { # truncate_at <label> <byte count>
	local label=$1 bytes=$2
	head -c "$bytes" "$installer" >"$work/trunc-$label.sh"
}
truncate_at 60pct $((installer_bytes * 60 / 100))
truncate_at 90pct $((installer_bytes * 90 / 100))
sed -n "1,$((installer_lines - 5))p" "$installer" >"$work/trunc-tail.sh"

uname_shim=$(fake_uname Darwin arm64)
for cut in 60pct 90pct tail; do
	trunc_prefix="$work/trunc-prefix-$cut"
	PATH="$no_service_ah:$uname_shim:$PATH" sh "$work/trunc-$cut.sh" \
		--dry-run --version v0.1.0 --prefix "$trunc_prefix" \
		>"$work/trunc-$cut.txt" 2>&1 || true
	commands_only "$work/trunc-$cut.txt" "$work/trunc-$cut.cmds"
	checks=$((checks + 1))
	if [ -s "$work/trunc-$cut.cmds" ]; then
		fail "truncated at $cut: it still ran commands"
	fi
	checks=$((checks + 1))
	[ ! -e "$trunc_prefix" ] || fail "truncated at $cut: it created $trunc_prefix"
done
# shellcheck disable=SC2016 # the literal last line of install.sh
contains truncation "$installer" 'main "$@"'

# ---- a directory this script did not create is not deleted -----------------

# AGENTHUB_HOME and --prefix are documented knobs, and `rm -rf` is downstream
# of both. Each of these printed a `rm -rf` of the named directory before.
echo "== a prefix that is not ours is refused =="
guard() { # guard <label> <message> [installer args...]
	local label=$1 message=$2
	shift 2
	dry_run_fails "$label" Linux x86_64 "$work/guard-$label.txt" "$@"
	commands_only "$work/guard-$label.txt" "$work/guard-$label.cmds"
	contains "$label" "$work/guard-$label.txt" "$message"
	lacks "$label" "$work/guard-$label.cmds" "rm"
}
guard root "this script installs into a directory of its own" --prefix /
guard home "is your home directory" --prefix "$HOME"
guard above-home "contains your home directory" --prefix "$(dirname "$HOME")"

notours="$work/notours"
mkdir -p "$notours"
echo "someone else's file" >"$notours/thesis.txt"
guard foreign "holds no AgentHub install" --prefix "$notours"
checks=$((checks + 1))
[ -f "$notours/thesis.txt" ] || fail "the refused prefix lost a file"

echo "== AGENTHUB_HOME pointing somewhere else is refused =="
checks=$((checks + 1))
export AGENTHUB_HOME="$notours"
if dry_run Linux x86_64 "$work/guard-agenthub-home.txt" \
	--version v0.1.0 --prefix "$work/pfx-ah"; then
	fail "AGENTHUB_HOME pointing at a foreign directory was accepted"
fi
unset AGENTHUB_HOME
commands_only "$work/guard-agenthub-home.txt" "$work/guard-agenthub-home.cmds"
contains agenthub-home "$work/guard-agenthub-home.txt" "holds no AgentHub install"
lacks agenthub-home "$work/guard-agenthub-home.cmds" "rm"

echo "== HOME unset is a message, not an unbound variable =="
checks=$((checks + 1))
if env -u HOME PATH="$no_service_ah:$uname_shim:$PATH" sh "$installer" \
	--dry-run --version v0.1.0 >"$work/no-home.txt" 2>&1; then
	fail "an unset HOME was accepted"
fi
contains no-home "$work/no-home.txt" "HOME is not set"
lacks no-home "$work/no-home.txt" "unbound variable"

# ---- the cleanup trap never rm -rf's an empty path -------------------------

# mktemp is shimmed to succeed while printing nothing, which is the shape that
# puts an empty string into TEMP_DIR after the trap is armed. rm is shimmed to
# record rather than delete, so a missing guard is a line in a log rather than
# a deleted directory.
echo "== the cleanup trap guards its variables =="
# shellcheck disable=SC2016 # the literal text of the guard, not an expansion
contains cleanup-guard "$installer" '[ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]'
trap_shim="$work/shim-trap"
mkdir -p "$trap_shim"
printf '#!/bin/sh\nexit 0\n' >"$trap_shim/mktemp"
cat >"$trap_shim/rm" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>"$work/rm.log"
EOF
chmod +x "$trap_shim/mktemp" "$trap_shim/rm"
: >"$work/rm.log"
checks=$((checks + 1))
if PATH="$trap_shim:$no_service_ah:$uname_shim:$PATH" sh "$installer" \
	--from "$work/no-such-archive.tar.gz" --prefix "$work/pfx-trap" --no-service \
	>"$work/trap.txt" 2>&1; then
	fail "a --from that is not a file was accepted"
fi
contains trap "$work/trap.txt" "is not a file"
checks=$((checks + 1))
[ ! -s "$work/rm.log" ] || fail "the cleanup trap ran rm with an empty TEMP_DIR: $(cat "$work/rm.log")"

# ---- what the last lines tell a stranger -----------------------------------

echo "== the closing lines say what to do next =="
contains closing "$work/svc-none.txt" "Open agenthub-desktop from Applications; it starts on a setup checklist."
contains closing "$work/svc-none.txt" "| sh -s -- --no-service"
contains closing "$work/svc-none.txt" "$work/pfx/bin/ah"
contains closing "$work/svc-none.txt" "node: running as a background service"
contains closing "$work/darwin-cli.txt" "node: not registered (--no-service)"
contains closing "$work/linux.txt" "it starts on a setup checklist."

echo
if [ "$failures" -ne 0 ]; then
	echo "$failures of $checks checks failed" >&2
	exit 1
fi
echo "all $checks checks passed"
