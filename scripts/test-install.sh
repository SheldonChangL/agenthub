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

# fake_ldconfig builds a PATH shim whose `ldconfig -p` reports one WebKit2GTK
# ABI, both, or neither. That answer decides which of the two Linux desktop
# archives install.sh downloads, so every Linux case below pins it rather than
# inheriting whatever the machine running these tests happens to have — the
# suite runs on developer laptops with no ldconfig at all and on runners that
# have one ABI today and the other after an image bump.
fake_ldconfig() { # fake_ldconfig <4.0|4.1|both|none> -> prints a directory for PATH
	local abi=$1
	local shim="$work/ldconfig-$abi"
	local lines=""
	case $abi in
	4.1) lines="\tlibwebkit2gtk-4.1.so.0 (libc6,x86-64) => /lib/x86_64-linux-gnu/libwebkit2gtk-4.1.so.0" ;;
	4.0) lines="\tlibwebkit2gtk-4.0.so.37 (libc6,x86-64) => /lib/x86_64-linux-gnu/libwebkit2gtk-4.0.so.37" ;;
	both) lines="\tlibwebkit2gtk-4.1.so.0 (libc6,x86-64) => /lib/x86_64-linux-gnu/libwebkit2gtk-4.1.so.0\n\tlibwebkit2gtk-4.0.so.37 (libc6,x86-64) => /lib/x86_64-linux-gnu/libwebkit2gtk-4.0.so.37" ;;
	none) lines="\tlibc.so.6 (libc6,x86-64) => /lib/x86_64-linux-gnu/libc.so.6" ;;
	*) echo "fake_ldconfig: unknown abi $abi" >&2; exit 1 ;;
	esac
	mkdir -p "$shim"
	cat >"$shim/ldconfig" <<EOF
#!/bin/sh
# Only -p is ever asked for; anything else would be a change in install.sh
# that this shim should be updated for rather than silently absorbed.
if [ "\${1:-}" != "-p" ]; then
	echo "fake ldconfig got \$*" >&2
	exit 1
fi
printf '%b\\n' "$lines"
EOF
	chmod +x "$shim/ldconfig"
	echo "$shim"
}

# fake_os_release writes the one file install.sh reads to decide which package
# manager to name in the WebKit hint.
fake_os_release() { # fake_os_release <id> [id_like] -> prints a file path
	local id=$1 like=${2:-}
	local file="$work/os-release-$id${like:+-$like}"
	{
		echo "NAME=\"$id\""
		echo "ID=$id"
		[ -z "$like" ] || echo "ID_LIKE=$like"
	} >"$file"
	echo "$file"
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
	# LDCONFIG_SHIM goes first so it wins over a real ldconfig in /sbin.
	#
	# AGENTHUB_OS_RELEASE defaults to a path that does not exist, so a run that
	# does not care which distribution it is on gets the same generic advice
	# everywhere. Without it these tests would read the os-release of whatever
	# machine runs them and assert different text on a laptop than in CI.
	#
	# LDCONFIG_PATHS overrides the candidate list install.sh walks. A PATH shim
	# cannot express "no ldconfig anywhere", because the probe also tries
	# /sbin/ldconfig and /usr/sbin/ldconfig by absolute path and a Linux runner
	# has one there; the cases that pin those two branches set this instead.
	PATH="${LDCONFIG_SHIM:+$LDCONFIG_SHIM:}${AH_SHIM:-$no_service_ah}:$shim:$PATH" \
		AGENTHUB_OS_RELEASE="${OS_RELEASE_FILE:-$work/no-such-os-release}" \
		AGENTHUB_LDCONFIG_PATHS="${LDCONFIG_PATHS:-}" \
		sh "$installer" --dry-run "$@" >"$out" 2>&1
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

# exists_path is -e that also says yes to a dangling symlink.
exists_path() {
	[ -e "$1" ] || [ -L "$1" ]
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
LDCONFIG_SHIM=$(fake_ldconfig 4.1)
dry_run Linux x86_64 "$work/linux.txt" --version v0.1.0 --prefix "$work/pfx"
unset LDCONFIG_SHIM
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

# ---- the two Linux WebKit ABIs ---------------------------------------------
#
# WebKit2GTK 4.0 and 4.1 are different ABIs, a distribution ships one or the
# other, and a binary built for either exits at startup on the other with no
# window and nothing in a log. The release carries a build for each, and this is
# the code that decides which one a machine downloads: getting it wrong is the
# whole failure, and it is invisible until someone double-clicks the app.

echo "== a 4.0 machine downloads the 4.0 build =="
LDCONFIG_SHIM=$(fake_ldconfig 4.0)
dry_run Linux x86_64 "$work/wk40.txt" --version v0.1.0 --prefix "$work/pfx"
unset LDCONFIG_SHIM
contains webkit40 "$work/wk40.txt" "agenthub-desktop_v0.1.0_linux_amd64_webkit40.tar.gz"
contains webkit40 "$work/wk40.txt" "this machine has WebKit2GTK 4.0"
contains webkit40 "$work/wk40.txt" "compared with agenthub-desktop_v0.1.0_linux_amd64_webkit40.tar.gz"
# The digest has to be checked against the file that was actually fetched.
lacks webkit40 "$work/wk40.txt" "compared with agenthub-desktop_v0.1.0_linux_amd64.tar.gz"
# Nothing to install, so nothing to advise: the matching build was downloaded.
lacks webkit40 "$work/wk40.txt" "will not open"
lacks webkit40 "$work/wk40.txt" "apt install"

echo "== a 4.1 machine downloads the plain build =="
LDCONFIG_SHIM=$(fake_ldconfig 4.1)
dry_run Linux x86_64 "$work/wk41.txt" --version v0.1.0 --prefix "$work/pfx"
unset LDCONFIG_SHIM
contains webkit41 "$work/wk41.txt" "agenthub-desktop_v0.1.0_linux_amd64.tar.gz"
lacks webkit41 "$work/wk41.txt" "_webkit40.tar.gz"
lacks webkit41 "$work/wk41.txt" "this machine has WebKit2GTK 4.0"
contains webkit41 "$work/wk41.txt" "this machine has WebKit2GTK 4.1; taking the build linked against it"
lacks webkit41 "$work/wk41.txt" "will not open"

echo "== both installed takes 4.1 =="
LDCONFIG_SHIM=$(fake_ldconfig both)
dry_run Linux x86_64 "$work/wkboth.txt" --version v0.1.0 --prefix "$work/pfx"
unset LDCONFIG_SHIM
contains webkit-both "$work/wkboth.txt" "agenthub-desktop_v0.1.0_linux_amd64.tar.gz"
lacks webkit-both "$work/wkboth.txt" "_webkit40.tar.gz"
contains webkit-both "$work/wkboth.txt" "this machine has both WebKit2GTK ABIs; taking the 4.1 build"

echo "== neither installed takes 4.1 and says what to install =="
LDCONFIG_SHIM=$(fake_ldconfig none)
dry_run Linux x86_64 "$work/wknone.txt" --version v0.1.0 --prefix "$work/pfx"
unset LDCONFIG_SHIM
contains webkit-none "$work/wknone.txt" "agenthub-desktop_v0.1.0_linux_amd64.tar.gz"
lacks webkit-none "$work/wknone.txt" "_webkit40.tar.gz"
contains webkit-none "$work/wknone.txt" "this machine has no WebKit2GTK runtime installed; taking the 4.1 build"
contains webkit-none "$work/wknone.txt" "no WebKit2GTK runtime was found"
contains webkit-none "$work/wknone.txt" "it will pick the 4.0 build"
# The install still happens; the missing runtime is a hint, not a refusal.
contains webkit-none "$work/wknone.txt" "ah service install"
# With no os-release to read, the advice cannot name a package manager and must
# not pretend to.
contains webkit-none "$work/wknone.txt" "with your package manager"

echo "== the hint names the right package for the distribution =="
# These names do not follow from the version number — 4.1 is
# libwebkit2gtk-4.1-0 on Debian and webkit2gtk4.1 on Fedora — so each branch is
# asserted rather than assumed. This text is the only thing a person whose app
# will not start has to go on.
LDCONFIG_SHIM=$(fake_ldconfig none)
for distro_case in "ubuntu:debian:apt install libgtk-3-0 libwebkit2gtk-4.1-0" \
	"debian::apt install libgtk-3-0 libwebkit2gtk-4.1-0" \
	"fedora::dnf install gtk3 webkit2gtk4.1" \
	"arch::pacman -S gtk3 webkit2gtk-4.1"; do
	distro_id=${distro_case%%:*}
	distro_rest=${distro_case#*:}
	distro_like=${distro_rest%%:*}
	distro_want=${distro_rest#*:}
	OS_RELEASE_FILE=$(fake_os_release "$distro_id" "$distro_like")
	dry_run Linux x86_64 "$work/hint-$distro_id.txt" --version v0.1.0 --prefix "$work/pfx"
	unset OS_RELEASE_FILE
	contains "hint-$distro_id" "$work/hint-$distro_id.txt" "$distro_want"
done
unset LDCONFIG_SHIM

# The probe reads `ldconfig`, and on Debian and its derivatives /sbin is not on
# a non-root PATH — which is the machine this whole mechanism exists for. These
# two cases are the ones a developer laptop and a root CI runner both fail to
# be, so they pin the candidate list rather than the PATH.
echo "== ldconfig only at an absolute path is still read =="
sbin_only=$(fake_ldconfig 4.0)
LDCONFIG_PATHS="$work/absent/ldconfig $sbin_only/ldconfig"
dry_run Linux x86_64 "$work/wksbin.txt" --version v0.1.0 --prefix "$work/pfx"
unset LDCONFIG_PATHS
contains webkit-sbin "$work/wksbin.txt" "agenthub-desktop_v0.1.0_linux_amd64_webkit40.tar.gz"
contains webkit-sbin "$work/wksbin.txt" "this machine has WebKit2GTK 4.0; taking the build linked against it"
lacks webkit-sbin "$work/wksbin.txt" "could not read this machine's WebKit2GTK ABI"
lacks webkit-sbin "$work/wksbin.txt" "will not open"

echo "== no ldconfig anywhere says so instead of going quiet =="
# The silent version of this is the original bug in another costume: the 4.1
# archive taken on a 4.0 machine with nothing printed at all.
LDCONFIG_PATHS="$work/absent/ldconfig"
OS_RELEASE_FILE=$(fake_os_release ubuntu debian)
dry_run Linux x86_64 "$work/wknold.txt" --version v0.1.0 --prefix "$work/pfx"
unset LDCONFIG_PATHS OS_RELEASE_FILE
contains webkit-noldconfig "$work/wknold.txt" "agenthub-desktop_v0.1.0_linux_amd64.tar.gz"
lacks webkit-noldconfig "$work/wknold.txt" "_webkit40.tar.gz"
contains webkit-noldconfig "$work/wknold.txt" "could not read this machine's WebKit2GTK ABI (no readable ldconfig); taking the 4.1 build"
# Not silent afterwards either: the package hint is what the reader needs, and
# it must not claim the runtime is missing, only that it could not be read.
contains webkit-noldconfig "$work/wknold.txt" "could not be read (no readable ldconfig)"
contains webkit-noldconfig "$work/wknold.txt" "apt install libgtk-3-0 libwebkit2gtk-4.1-0"

# Every case above either shims ldconfig onto PATH or pins the candidate list,
# so none of them would notice the list shrinking back to PATH alone — which is
# the whole defect: a Debian 12 shell has no /sbin on PATH. Assert the literal.
# shellcheck disable=SC2016 # the literal default, not an expansion
contains webkit-paths "$installer" '${AGENTHUB_LDCONFIG_PATHS:-ldconfig /sbin/ldconfig /usr/sbin/ldconfig}'
lacks webkit-noldconfig "$work/wknold.txt" "no WebKit2GTK runtime was found"

echo "== --cli-only never probes for a window it is not installing =="
LDCONFIG_SHIM=$(fake_ldconfig 4.0)
dry_run Linux x86_64 "$work/wkcli.txt" --version v0.1.0 --prefix "$work/pfx" --cli-only
unset LDCONFIG_SHIM
contains webkit-cli "$work/wkcli.txt" "agenthub_v0.1.0_linux_amd64.tar.gz"
lacks webkit-cli "$work/wkcli.txt" "_webkit40.tar.gz"
lacks webkit-cli "$work/wkcli.txt" "this machine has WebKit2GTK 4.0"

echo "== --from does not claim to have chosen the archive =="
# The 4.0 probe still runs (the hint depends on it), but an archive named on the
# command line was not picked by it.
LDCONFIG_SHIM=$(fake_ldconfig 4.0)
dry_run_fails "from-nonexistent" Linux x86_64 "$work/from.txt" --from "$work/no-such.tar.gz"
unset LDCONFIG_SHIM
lacks from-claim "$work/from.txt" "taking the build linked against it"

echo "== the applications menu gets an entry =="
LDCONFIG_SHIM=$(fake_ldconfig 4.1)
dry_run Linux x86_64 "$work/entry.txt" --version v0.1.0 --prefix "$work/pfx"
unset LDCONFIG_SHIM
commands_only "$work/entry.txt" "$work/entry.cmds"
contains menu "$work/entry.cmds" "applications"
contains menu "$work/entry.txt" "agenthub.desktop"
# A command line install has no window to launch, so it leaves no menu entry
# behind for someone to click and watch do nothing.
LDCONFIG_SHIM=$(fake_ldconfig 4.1)
dry_run Linux x86_64 "$work/entry-cli.txt" --version v0.1.0 --prefix "$work/pfx" --cli-only
unset LDCONFIG_SHIM
lacks menu-cli "$work/entry-cli.txt" "agenthub.desktop"
# Neither does macOS: it has a .app, and a .desktop file there is litter.
dry_run Darwin arm64 "$work/entry-mac.txt" --version v0.1.0 --prefix "$work/pfx"
lacks menu-darwin "$work/entry-mac.txt" "agenthub.desktop"

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
# XDG_DATA_HOME is redirected into the work directory for every real install
# below. Without it these runs — faked into being Linux — write a real
# ~/.local/share/applications/agenthub.desktop on the machine running the
# tests, pointing Exec= into a temp directory that is deleted on the next line
# of this script: a dead launcher entry left behind by a test suite. It also
# makes the entry readable here, which is the only way its body gets asserted
# at all.
xdg="$work/xdg"
PATH="$shim:$PATH" XDG_DATA_HOME="$xdg" sh "$installer" \
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

# ---- the menu entry that was actually written ------------------------------

# The dry run proves the entry is written; only a real install can say what is
# in it. A launcher reads exactly these keys, and every one of them is silently
# wrong in its own way: a broken Exec= gives a menu item that does nothing, a
# missing StartupWMClass= gives a window that docks under a second icon.
entry="$xdg/applications/agenthub.desktop"
contains menu-body "$work/real.txt" "wrote $entry"
checks=$((checks + 1))
[ -f "$entry" ] || fail "real: no menu entry at $entry"
# It goes where XDG_DATA_HOME says and nowhere else. This assertion is the one
# that keeps the suite from littering the machine that runs it.
home_entry="${HOME:-/nonexistent}/.local/share/applications/agenthub.desktop"
checks=$((checks + 1))
if [ -f "$home_entry" ] && grep -qF -- "$work" "$home_entry"; then
	fail "real: the install wrote $home_entry pointing into this suite's work directory"
fi
contains menu-body "$entry" "[Desktop Entry]"
contains menu-body "$entry" "Type=Application"
contains menu-body "$entry" "Name=AgentHub"
contains menu-body "$entry" "Exec=\"$target/share/agenthub/agenthub-desktop\""
contains menu-body "$entry" "Terminal=false"
contains menu-body "$entry" "Categories=Development;"
contains menu-body "$entry" "StartupWMClass=agenthub-desktop"
# This archive ships no appicon.png, so Icon= must be absent rather than
# present and dangling: a launcher shows a broken-image placeholder for the
# second and its own generic icon for the first.
lacks menu-body "$entry" "Icon="

# An archive that does carry the icon names it, by absolute path inside the
# install tree. And the prefix here has a space in it, which is what makes the
# quoting of Exec= load-bearing: unquoted, a launcher reads the path as a
# command plus an argument and starts nothing.
spaced_stage="$work/stage-icon/agenthub-desktop_v0.1.0_linux_amd64"
mkdir -p "$spaced_stage"
for binary in ah agenthub-node agenthub-mcp agenthub-desktop; do
	printf '#!/bin/sh\necho "%s v0.1.0 (fake)"\n' "$binary" >"$spaced_stage/$binary"
	chmod +x "$spaced_stage/$binary"
done
printf 'not really a png\n' >"$spaced_stage/appicon.png"
iconed="$work/iconed"
mkdir -p "$iconed"
tar -C "$work/stage-icon" -czf "$iconed/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" \
	agenthub-desktop_v0.1.0_linux_amd64
spaced_target="$work/a prefix with spaces"
spaced_xdg="$work/xdg spaced"
PATH="$shim:$PATH" XDG_DATA_HOME="$spaced_xdg" sh "$installer" \
	--from "$iconed/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" \
	--prefix "$spaced_target" --no-service --no-open >"$work/real-icon.txt" 2>&1
spaced_entry="$spaced_xdg/applications/agenthub.desktop"
checks=$((checks + 1))
[ -f "$spaced_entry" ] || fail "icon: no menu entry at $spaced_entry"
contains menu-icon "$spaced_entry" "Exec=\"$spaced_target/share/agenthub/agenthub-desktop\""
contains menu-icon "$spaced_entry" "Icon=$spaced_target/share/agenthub/appicon.png"
checks=$((checks + 1))
[ -x "$spaced_target/share/agenthub/agenthub-desktop" ] ||
	fail "icon: the install under a path with spaces left no desktop binary"

# Re-running upgrades in place rather than failing on what is already there.
PATH="$shim:$PATH" XDG_DATA_HOME="$xdg" sh "$installer" \
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
if PATH="$shim:$PATH" XDG_DATA_HOME="$xdg" sh "$installer" \
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
if PATH="$shim:$PATH" XDG_DATA_HOME="$xdg" sh "$installer" \
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

# ---- ~/.local/bin ends up on PATH ------------------------------------------

# macOS puts no ~/.local/bin on PATH, so a default install that only printed a
# hint ended with `ah: command not found`. These runs use a HOME of their own —
# without --prefix the installer writes under HOME, and the startup file it
# appends to is the owner's real one otherwise — and a PATH that cannot already
# hold that HOME's bin directory.
echo "== a default install puts ~/.local/bin on PATH =="
bare_path="/usr/bin:/bin:/usr/sbin:/sbin"
path_home="$work/path-home"
mkdir -p "$path_home"
# shellcheck disable=SC2016 # the literal line the installer writes
export_line='export PATH="$HOME/.local/bin:$PATH"'

path_dry() { # path_dry <output file> <shell> [args...]
	local out=$1 shell=$2
	shift 2
	PATH="$no_service_ah:$(fake_uname Darwin arm64):$bare_path" HOME="$path_home" SHELL="$shell" \
		sh "$installer" --dry-run --version v0.1.0 "$@" >"$out" 2>&1
}
path_dry "$work/path-zsh.txt" /bin/zsh --no-service --no-open
contains path-zsh "$work/path-zsh.txt" "+ append to $path_home/.zshrc: $export_line"
contains path-zsh "$work/path-zsh.txt" "PATH: on PATH in new terminals"
lacks path-zsh "$work/path-zsh.txt" "is not on your PATH"
checks=$((checks + 1))
[ ! -e "$path_home/.zshrc" ] || fail "path-zsh: a dry run wrote $path_home/.zshrc"
path_dry "$work/path-bash-mac.txt" /bin/bash --no-service --no-open
contains path-bash-mac "$work/path-bash-mac.txt" "+ append to $path_home/.bash_profile: "
path_dry "$work/path-noshell.txt" "" --no-service --no-open
contains path-noshell "$work/path-noshell.txt" "+ append to $path_home/.zshrc: "
path_dry "$work/path-optout.txt" /bin/zsh --no-service --no-open --no-modify-path
contains path-optout "$work/path-optout.txt" "note: $path_home/.local/bin is not on your PATH"
lacks path-optout "$work/path-optout.txt" "append to"
path_dry "$work/path-tcsh.txt" /bin/tcsh --no-service --no-open
contains path-tcsh "$work/path-tcsh.txt" "is not on your PATH. Add it"
contains path-tcsh "$work/path-tcsh.txt" "PATH: not on PATH"
lacks path-tcsh "$work/path-tcsh.txt" "append to"
# A bash login shell reads only the first of .bash_profile, .bash_login and
# .profile that exists; a new .bash_profile would switch an existing .profile off.
profile_home="$work/profile-home"
mkdir -p "$profile_home"
echo 'export FROM_PROFILE=1' >"$profile_home/.profile"
PATH="$no_service_ah:$(fake_uname Darwin arm64):$bare_path" HOME="$profile_home" SHELL=/bin/bash \
	sh "$installer" --dry-run --version v0.1.0 --no-service --no-open >"$work/path-profile.txt" 2>&1
contains path-profile "$work/path-profile.txt" "+ append to $profile_home/.profile: "
lacks path-profile "$work/path-profile.txt" ".bash_profile"
PATH="$no_service_ah:$(fake_uname Darwin arm64):$path_home/.local/bin:$bare_path" HOME="$path_home" SHELL=/bin/zsh \
	sh "$installer" --dry-run --version v0.1.0 --no-service --no-open >"$work/path-already.txt" 2>&1
lacks path-already "$work/path-already.txt" "append to"
lacks path-already "$work/path-already.txt" "is not on your PATH"
contains path-already "$work/path-already.txt" "PATH: on PATH"

# A real install without --prefix, into that HOME, run twice. What is asserted
# is the composition: a new shell that reads the startup file finds the `ah`
# this install linked — not merely that some line was written.
echo "== the startup file a real install writes finds ah =="
linux_shim=$(fake_uname Linux x86_64)
path_real() { # path_real <output file> <shell> [args...]
	local out=$1 shell=$2
	shift 2
	PATH="$linux_shim:$bare_path" HOME="$path_home" SHELL="$shell" XDG_DATA_HOME="$work/path-xdg" \
		sh "$installer" --from "$good/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" \
		--no-service "$@" >"$out" 2>&1
}
checks=$((checks + 1))
path_real "$work/path-real.txt" /bin/bash || fail "path-real: the install failed: $(cat "$work/path-real.txt")"
contains path-real "$work/path-real.txt" "added $path_home/.local/bin to PATH in $path_home/.bashrc"
checks=$((checks + 1))
path_real "$work/path-real2.txt" /bin/bash || fail "path-real2: the second install failed"
contains path-real2 "$work/path-real2.txt" "$path_home/.bashrc already adds"
checks=$((checks + 1))
[ "$(grep -cF "$export_line" "$path_home/.bashrc")" -eq 1 ] ||
	fail "path-real: a second install added the line again: $(cat "$path_home/.bashrc")"
checks=$((checks + 1))
# shellcheck disable=SC2016 # expanded by the child shell, with its own HOME
found=$(env -i HOME="$path_home" PATH="$bare_path" bash -c '. "$HOME/.bashrc"; command -v ah' || true)
[ "$found" = "$path_home/.local/bin/ah" ] || fail "path-real: a shell reading .bashrc finds ah at \"$found\""
if command -v zsh >/dev/null 2>&1; then
	zdot="$work/path-zdot"
	mkdir -p "$zdot"
	checks=$((checks + 1))
	ZDOTDIR="$zdot" path_real "$work/path-real-zsh.txt" /bin/zsh || fail "path-real-zsh: the install failed"
	contains path-real-zsh "$work/path-real-zsh.txt" "to PATH in $zdot/.zshrc"
	checks=$((checks + 1))
	found=$(env -i HOME="$path_home" ZDOTDIR="$zdot" PATH="$bare_path" zsh -i -c 'command -v ah' 2>/dev/null || true)
	[ "$found" = "$path_home/.local/bin/ah" ] || fail "path-real-zsh: a new zsh finds ah at \"$found\""
fi
# A startup file that cannot be written is a warning, not the end of the
# install: the node still gets registered and the closing lines still print.
# Root writes through a read-only mode, so the case means nothing there.
if [ "$(id -u)" -ne 0 ]; then
	ro_home="$work/ro-home"
	mkdir -p "$ro_home"
	: >"$work/ro-bashrc"
	chmod 444 "$work/ro-bashrc"
	ln -s "$work/ro-bashrc" "$ro_home/.bashrc"
	checks=$((checks + 1))
	PATH="$linux_shim:$bare_path" HOME="$ro_home" SHELL=/bin/bash XDG_DATA_HOME="$work/ro-xdg" \
		sh "$installer" --from "$good/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" \
		--no-service >"$work/path-ro.txt" 2>&1 || fail "path-ro: a read-only .bashrc stopped the install: $(cat "$work/path-ro.txt")"
	contains path-ro "$work/path-ro.txt" "warning: could not write $ro_home/.bashrc"
	contains path-ro "$work/path-ro.txt" "PATH: not on PATH"
	contains path-ro "$work/path-ro.txt" "skipped the background service"
	contains path-ro "$work/path-ro.txt" "done. AgentHub"
fi
rm -f "$path_home/.profile"
checks=$((checks + 1))
path_real "$work/path-real-optout.txt" /bin/sh --no-modify-path || fail "path-real-optout: the install failed"
checks=$((checks + 1))
[ ! -e "$path_home/.profile" ] || fail "path-real-optout: --no-modify-path wrote $path_home/.profile"

# ---- --uninstall -------------------------------------------------------------

# Every uninstall below runs with a HOME of its own: it deletes things under
# HOME, and the one running these tests is the developer's. HOME is not the
# only way out, either: XDG_CONFIG_HOME, XDG_STATE_HOME, XDG_DATA_HOME and
# ZDOTDIR name directories on their own, so each run clears them, and a
# faked uname does not fake the disk — a Linux-shaped run on a mac still sees
# the real /Applications. That is not hypothetical: an earlier draft of these
# tests deleted the developer's /Applications/agenthub-desktop.app.
isolated() { # isolated <home> <command...>
	local home=$1
	shift
	env -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u XDG_DATA_HOME -u ZDOTDIR HOME="$home" \
		AGENTHUB_APPLICATIONS="$fake_applications" "$@"
}
# An /Applications of the tests' own, holding an app a Linux uninstall must not
# touch: a .app is a macOS install, whatever directory it is in.
fake_applications="$work/Applications"
mkdir -p "$fake_applications/agenthub-desktop.app/Contents/MacOS"
printf '#!/bin/sh\necho "$*" >>"%s"\n' "$work/fake-app-ah.log" >"$fake_applications/agenthub-desktop.app/Contents/MacOS/ah"
chmod +x "$fake_applications/agenthub-desktop.app/Contents/MacOS/ah"

# uninstall_ah_script writes an ah that records what it was asked and answers
# `--json service status` with a registered unit using <db>. <status> is the
# exit code for `service uninstall`.
uninstall_ah_script() { # uninstall_ah_script <file> <log> <db> <status>
	cat >"$1" <<EOF
#!/bin/sh
echo "\$*" >>"$2"
case "\$*" in
*"service status"*)
	printf '%s\n' '{"service":{"Installed":true,"Supported":true,"Arguments":["--db","$3"]}}'
	;;
*"service uninstall"*) exit $4 ;;
esac
EOF
	chmod +x "$1"
}

echo "== --uninstall takes back what a real install put in place =="
un_home="$work/un-home"
mkdir -p "$un_home/.local/bin"
printf 'alias keep=1\n\n# the owner'"'"'s own\n' >"$un_home/.bashrc"
cp "$un_home/.bashrc" "$work/un-bashrc.before"
ln -s /usr/bin/true "$un_home/.local/bin/agenthub-mcp"
un_run() { # un_run <output file> [args...]
	local out=$1
	shift
	PATH="$linux_shim:$bare_path" SHELL=/bin/bash \
		isolated "$un_home" sh "$installer" "$@" >"$out" 2>&1
}
checks=$((checks + 1))
un_run "$work/un-install.txt" --from "$good/agenthub-desktop_v0.1.0_linux_amd64.tar.gz" --no-service ||
	fail "un: the install failed: $(cat "$work/un-install.txt")"
uninstall_ah_script "$un_home/.local/share/agenthub/ah" "$work/un-ah.log" "$un_home/.config/agenthub/agenthub.db" 0
mkdir -p "$un_home/.config/agenthub" "$un_home/.local/state/agenthub"
echo key >"$un_home/.config/agenthub/node.key"
echo db >"$un_home/.config/agenthub/agenthub.db"
echo log >"$un_home/.local/state/agenthub/node.log"
checks=$((checks + 1))
[ -f "$un_home/.local/share/applications/agenthub.desktop" ] || fail "un: the install wrote no menu entry to take back"
checks=$((checks + 1))
un_run "$work/un.txt" --uninstall || fail "un: --uninstall failed: $(cat "$work/un.txt")"
contains un "$work/un-ah.log" "service uninstall"
lacks un "$work/un.txt" "/Applications"
for gone in .local/share/agenthub .local/bin/ah .local/bin/agenthub-desktop .local/share/applications/agenthub.desktop; do
	checks=$((checks + 1))
	! exists_path "$un_home/$gone" || fail "un: $gone is still there"
done
checks=$((checks + 1))
[ -L "$un_home/.local/bin/agenthub-mcp" ] || fail "un: a link that points elsewhere was removed"
contains un "$work/un.txt" "left $un_home/.local/bin/agenthub-mcp alone"
checks=$((checks + 1))
cmp -s "$un_home/.bashrc" "$work/un-bashrc.before" ||
	fail "un: .bashrc is not what it was before the install: $(od -c "$un_home/.bashrc" | head -5)"
checks=$((checks + 1))
[ -f "$un_home/.config/agenthub/node.key" ] || fail "un: --uninstall without --purge deleted node.key"
contains un "$work/un.txt" "--uninstall --purge"
contains un "$work/un.txt" "ah revoke"
checks=$((checks + 1))
un_run "$work/un-purge.txt" --uninstall --purge || fail "un-purge: failed: $(cat "$work/un-purge.txt")"
for gone in .config/agenthub .local/state/agenthub; do
	checks=$((checks + 1))
	! exists_path "$un_home/$gone" || fail "un-purge: $gone is still there"
done
contains un-purge "$work/un-purge.txt" "installing again makes a new node"
lacks un-purge "$work/un-purge.txt" "/Applications"

checks=$((checks + 1))
[ -x "$fake_applications/agenthub-desktop.app/Contents/MacOS/ah" ] || fail "un: a Linux uninstall removed a macOS app bundle"
checks=$((checks + 1))
[ ! -e "$work/fake-app-ah.log" ] || fail "un: a Linux uninstall ran the ah inside a macOS app bundle: $(cat "$work/fake-app-ah.log")"

echo "== --purge of a database outside the default takes only its files =="
checkout="$work/checkout/agenthub"
mkdir -p "$checkout" "$work/purge-home" "$work/purge-shim"
echo key >"$checkout/node.key"
echo db >"$checkout/agenthub.db"
echo db >"$checkout/agenthub.db-wal"
echo mine >"$checkout/README.md"
uninstall_ah_script "$work/purge-shim/ah" "$work/purge-ah.log" "$checkout/agenthub.db" 0
checks=$((checks + 1))
PATH="$work/purge-shim:$linux_shim:$bare_path" SHELL=/bin/bash \
	isolated "$work/purge-home" sh "$installer" --uninstall --purge >"$work/purge.txt" 2>&1 || fail "purge: failed: $(cat "$work/purge.txt")"
for gone in node.key agenthub.db agenthub.db-wal; do
	checks=$((checks + 1))
	[ ! -e "$checkout/$gone" ] || fail "purge: $gone is still there"
done
checks=$((checks + 1))
[ -f "$checkout/README.md" ] || fail "purge: a file of the owner's beside the database was deleted"

echo "== a service that will not come down stops the uninstall =="
refuse_home="$work/refuse-home"
mkdir -p "$refuse_home/.local/share/agenthub" "$work/refuse-shim"
uninstall_ah_script "$refuse_home/.local/share/agenthub/ah" "$work/refuse-ah.log" "" 1
checks=$((checks + 1))
if PATH="$linux_shim:$bare_path" SHELL=/bin/bash \
	isolated "$refuse_home" sh "$installer" --uninstall >"$work/refuse.txt" 2>&1; then
	fail "refuse: a failed service uninstall was carried past"
fi
contains refuse "$work/refuse.txt" "nothing else was removed"
checks=$((checks + 1))
[ -x "$refuse_home/.local/share/agenthub/ah" ] || fail "refuse: the install was deleted under a registered service"

echo "== --uninstall on macOS: the app, its links and its caches =="
mac_home="$work/mac-home"
mac_pfx="$work/mac-pfx"
mkdir -p "$mac_pfx/agenthub-desktop.app/Contents/MacOS" "$mac_pfx/bin" \
	"$mac_home/Library/Caches/com.wails.agenthub-desktop" "$mac_home/Library/Preferences"
touch "$mac_pfx/.agenthub-install" "$mac_home/Library/Preferences/com.wails.agenthub-desktop.plist"
uninstall_ah_script "$mac_pfx/agenthub-desktop.app/Contents/MacOS/ah" "$work/mac-ah.log" "" 0
ln -s "$mac_pfx/agenthub-desktop.app/Contents/MacOS/ah" "$mac_pfx/bin/ah"
mac_un() { # mac_un <output file> [args...]
	local out=$1
	shift
	PATH="$(fake_uname Darwin arm64):$bare_path" SHELL=/bin/zsh \
		isolated "$mac_home" sh "$installer" --uninstall --prefix "$mac_pfx" "$@" >"$out" 2>&1
}
checks=$((checks + 1))
mac_un "$work/mac-dry.txt" --dry-run || fail "mac-dry: failed: $(cat "$work/mac-dry.txt")"
commands_only "$work/mac-dry.txt" "$work/mac-dry.cmds"
contains mac-dry "$work/mac-dry.cmds" "service uninstall"
contains mac-dry "$work/mac-dry.cmds" "rm -rf $mac_pfx/agenthub-desktop.app"
contains mac-dry "$work/mac-dry.cmds" "rm -f $mac_pfx/bin/ah"
contains mac-dry "$work/mac-dry.cmds" "rm -rf $mac_home/Library/Caches/com.wails.agenthub-desktop"
lacks mac-dry "$work/mac-dry.cmds" "sudo"
checks=$((checks + 1))
[ -d "$mac_pfx/agenthub-desktop.app" ] || fail "mac-dry: a dry run removed the app"
[ -L "$mac_pfx/bin/ah" ] || fail "mac-dry: a dry run removed $mac_pfx/bin/ah"
# Reading the registration is what a dry run is for; taking it down is not.
: >>"$work/mac-ah.log"
lacks mac-dry "$work/mac-ah.log" "service uninstall"
checks=$((checks + 1))
mac_un "$work/mac.txt" || fail "mac: failed: $(cat "$work/mac.txt")"
for gone in "$mac_pfx/agenthub-desktop.app" "$mac_pfx/bin/ah" "$mac_pfx/.agenthub-install" \
	"$mac_home/Library/Caches/com.wails.agenthub-desktop" "$mac_home/Library/Preferences/com.wails.agenthub-desktop.plist"; do
	checks=$((checks + 1))
	! exists_path "$gone" || fail "mac: $gone is still there"
done

echo "== --uninstall refuses the flags that do not go with it =="
checks=$((checks + 1))
if sh "$installer" --purge >"$work/purge-alone.txt" 2>&1; then fail "--purge alone was accepted"; fi
contains purge-alone "$work/purge-alone.txt" "--purge only goes with --uninstall"
checks=$((checks + 1))
if sh "$installer" --uninstall --version v0.1.0 >"$work/un-version.txt" 2>&1; then fail "--uninstall --version was accepted"; fi
contains un-version "$work/un-version.txt" "do not go with it"

# ---- what the last lines tell a stranger -----------------------------------

echo "== the closing lines say what to do next =="
contains closing "$work/svc-none.txt" "Open $work/pfx/agenthub-desktop.app; it starts on a setup checklist."
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
