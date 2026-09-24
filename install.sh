#!/bin/sh
# AgentHub one-line installer for macOS and Linux.
#
#     curl -fsSL https://raw.githubusercontent.com/SheldonChangL/agenthub/main/install.sh | sh
#
# It picks the one release asset for this machine, verifies it against the
# release's own SHA256SUMS before anything is unpacked, puts it somewhere the
# owner can already write, and registers the background node with launchd or
# `systemd --user`. It never asks for sudo, and the complete list of paths it
# writes is in docs/install-script.md.
#
# Every statement with an effect lives inside main(), and the last line of the
# file calls it. That is not style: this script is piped into `sh` from the
# network, and a stream cut in half is a file that ends mid-function. With the
# body at the top level, half a file is half an install that exits 0. With the
# call last, half a file defines some functions and runs none of them.
#
# POSIX sh only: this runs under macOS /bin/sh (a bash in POSIX mode) and under
# dash on Debian and Ubuntu, so no arrays, no [[, no local, no +=.
set -eu

REPO="SheldonChangL/agenthub"
API_LATEST="https://api.github.com/repos/${REPO}/releases/latest"
DOWNLOAD_BASE="https://github.com/${REPO}/releases/download"
RAW_SELF="https://raw.githubusercontent.com/${REPO}/main/install.sh"

VERSION="${AGENTHUB_VERSION:-}"
CLI_ONLY=0
NO_SERVICE=0
NO_OPEN=0
NO_MODIFY_PATH=0
NO_SKILL=0
UNINSTALL=0
PURGE=0
DRY_RUN=0
PREFIX=""
FROM=""

TEMP_DIR=""
MOUNT_POINT=""

# A directory this script put an install in carries this file, so a later run
# can tell "the prefix I installed into" from "a directory of the owner's that
# happens to be named on the command line". Without it, --prefix ~/Documents
# would be a delete.
MARKER=".agenthub-install"

# The Claude Code skill that teaches an agent to use `ah` (`ah inbox`,
# `ah send`, ...). It travels inside the release asset, so the SHA256SUMS check
# above covers it like every binary; it is never fetched on its own. The copy
# this script puts in place carries SKILL_MARKER, which is how an upgrade or an
# uninstall tells it from a link or a copy the owner made themselves.
SKILL_NAME="agenthub-watch"
SKILL_MARKER="$MARKER"
# Where the .dmg keeps it: beside the app, hidden, so the drag-to-install
# window still shows only the app and Applications, and outside the bundle, so
# the bundle's signature is not touched.
DMG_SKILL_DIR=".skills"
SKILL_SOURCE=""

AH=""
SERVICE_STATE=""
PATH_STATE=""
SKILL_STATE=""

usage() {
	cat <<'EOF'
install.sh — install AgentHub (desktop app and command line) on macOS or Linux.

  curl -fsSL https://raw.githubusercontent.com/SheldonChangL/agenthub/main/install.sh | sh

Options:
  --version vX.Y.Z   install this release instead of the latest one
                     (AGENTHUB_VERSION does the same)
  --cli-only         install ah, agenthub-node and agenthub-mcp without the app
  --no-service       do not register the background node
  --no-open          do not open the app when the install finishes (macOS)
  --no-modify-path   do not add ~/.local/bin to PATH in the shell's startup
                     file; only say what to add
  --no-skill         do not install the agenthub-watch skill for Claude Code
                     (with --uninstall: leave it in place)
  --prefix DIR       install under DIR instead of /Applications or ~/.local;
                     symlinks then go to DIR/bin
  --from FILE        install from a local .dmg or .tar.gz instead of
                     downloading; a SHA256SUMS beside it is still checked
  --uninstall        remove what this script installed: the background
                     service, the app, the links, the menu entry, the PATH
                     line and the app's caches; the node's identity and
                     database stay, so a reinstall is the same node
  --purge            with --uninstall: also delete the node's identity,
                     database and logs (a reinstall is then a new node)
  --dry-run          print every command instead of running it
  -h, --help         this text

Through a pipe, flags go after `sh -s --`:

  curl -fsSL https://raw.githubusercontent.com/SheldonChangL/agenthub/main/install.sh | sh -s -- --no-service

It never uses sudo. Read it before you run it: curl -fsSL <url> | less
EOF
}

say() {
	echo "$*"
}

# warn is for something the reader should notice but that is not a refusal. It
# goes to stderr so that it survives a transcript someone is skimming.
warn() {
	echo "warning: $*" >&2
}

die() {
	echo "install.sh: $*" >&2
	exit 1
}

# quote renders one argument the way a shell would have to be given it, so the
# --dry-run transcript is something a reader can paste back. Ordinary words are
# left bare: a transcript in which every word wears quotes is one nobody reads.
quote() {
	case "$1" in
	"" | *[!A-Za-z0-9._/:=@+-]*)
		printf '%s' "$1" | sed "s/'/'\\\\''/g; s/^/'/; s/\$/'/"
		;;
	*)
		printf '%s' "$1"
		;;
	esac
}

# run is the only place a command with an effect is started. Under --dry-run it
# prints the command and returns 0, which is what makes the dry run safe to
# point at a machine that already has AgentHub on it.
run() {
	if [ "$DRY_RUN" -eq 1 ]; then
		printf '+'
		for run_argument in "$@"; do
			printf ' %s' "$(quote "$run_argument")"
		done
		printf '\n'
		return 0
	fi
	"$@"
}

cleanup() {
	cleanup_status=$?
	if [ -n "$MOUNT_POINT" ] && [ -d "$MOUNT_POINT" ]; then
		hdiutil detach "$MOUNT_POINT" -quiet >/dev/null 2>&1 || true
	fi
	# Both tests matter. An empty TEMP_DIR reaching `rm -rf` is `rm -rf ""`,
	# which some shells expand to the current directory.
	if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
		rm -rf "$TEMP_DIR"
	fi
	exit "$cleanup_status"
}

parse_arguments() {
	while [ "$#" -gt 0 ]; do
		case "$1" in
		--version)
			[ "$#" -ge 2 ] || die "--version needs a tag, for example --version v0.1.0"
			VERSION="$2"
			shift 2
			;;
		--version=*)
			VERSION="${1#--version=}"
			shift
			;;
		--prefix)
			[ "$#" -ge 2 ] || die "--prefix needs a directory"
			PREFIX="$2"
			shift 2
			;;
		--prefix=*)
			PREFIX="${1#--prefix=}"
			shift
			;;
		--from)
			[ "$#" -ge 2 ] || die "--from needs a path to a .dmg or .tar.gz"
			FROM="$2"
			shift 2
			;;
		--from=*)
			FROM="${1#--from=}"
			shift
			;;
		--cli-only)
			CLI_ONLY=1
			shift
			;;
		--no-service)
			NO_SERVICE=1
			shift
			;;
		--no-open)
			NO_OPEN=1
			shift
			;;
		--no-modify-path)
			NO_MODIFY_PATH=1
			shift
			;;
		--no-skill)
			NO_SKILL=1
			shift
			;;
		--uninstall)
			UNINSTALL=1
			shift
			;;
		--purge)
			PURGE=1
			shift
			;;
		--dry-run)
			DRY_RUN=1
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			die "unknown option $1; --help lists them"
			;;
		esac
	done
}

# --- the directories this script is allowed to write ---------------------------

# absolute_path makes a path absolute and strips the trailing slashes, so that
# "/", "//" and "/." are one string and can be compared with one test.
absolute_path() {
	absolute_value="$1"
	case "$absolute_value" in
	/*) ;;
	*) absolute_value="$PWD/$absolute_value" ;;
	esac
	while :; do
		case "$absolute_value" in
		/) break ;;
		*/) absolute_value="${absolute_value%/}" ;;
		*) break ;;
		esac
	done
	[ -n "$absolute_value" ] || absolute_value="/"
	printf '%s' "$absolute_value"
}

# exists is -e that also says yes to a symlink whose target is gone; both are
# things that are in the way of an install.
exists() {
	[ -e "$1" ] || [ -L "$1" ]
}

# holds_agenthub asks whether a directory is one an AgentHub install already
# owns. The marker file answers for a prefix this script wrote; the binaries
# answer for a tree installed by hand or by an older version of this script.
holds_agenthub() {
	exists "$1/$MARKER" || exists "$1/ah" || exists "$1/bin/ah" ||
		exists "$1/agenthub-desktop" || exists "$1/agenthub-desktop.app" ||
		exists "$1/share/agenthub"
}

# check_install_directory refuses a directory this script would delete or
# replace and that is not its own. AGENTHUB_HOME and --prefix are knobs a
# reader is invited to turn, and `rm -rf` is downstream of both.
check_install_directory() { # check_install_directory <what it is> <directory>
	check_dir="$(absolute_path "$2")"
	check_home="$(absolute_path "$HOME")"
	if [ "$check_dir" = "/" ]; then
		die "$1 is /; this script installs into a directory of its own, not into the root of the disk"
	fi
	if [ "$check_dir" = "$check_home" ]; then
		die "$1 is your home directory ($check_dir); installing there would put an upgrade's \`rm -rf\` on it"
	fi
	case "$check_home" in
	"$check_dir"/*)
		die "$1 is $check_dir, which contains your home directory; pick a directory of its own, for example \$HOME/.local/agenthub"
		;;
	esac
	if [ -d "$check_dir" ] && ! holds_agenthub "$check_dir"; then
		die "$1 is $check_dir, which already exists and holds no AgentHub install.
An upgrade replaces that directory, so this script will not take one it did not
create. Pick a new directory, or delete that one yourself first."
	fi
}

# --- verification -------------------------------------------------------------

# digest_of prints the lowercase sha256 of a file.
digest_of() {
	# shellcheck disable=SC2086 # SHA_ARGS is a deliberate word list, and is empty for sha256sum
	"$SHA_CMD" $SHA_ARGS "$1" | cut -d' ' -f1 | tr 'A-F' 'a-f'
}

# verify refuses to go on unless the file's digest is the one the release
# published. This is the only thing standing between a downloader and whatever
# a hostile network handed them, so a missing line in SHA256SUMS is a failure,
# not a shrug.
verify() { # verify <file> <SHA256SUMS path> <name to look up>
	if [ "$DRY_RUN" -eq 1 ]; then
		say "+ $SHA_CMD $SHA_ARGS $1   # compared with $3 in $2, and stop if it differs"
		return 0
	fi
	# Matched field by field rather than with a regular expression built out of
	# a file name: a name is not a pattern, and one with a dot in it matched
	# more lines than the one it names.
	verify_expected="$(awk -v name="$3" '
		length($1) == 64 && $1 ~ /^[0-9a-fA-F]+$/ {
			candidate = $2
			sub(/^\*/, "", candidate)
			if (candidate == name) { print tolower($1); exit }
		}' "$2")"
	[ -n "$verify_expected" ] || die "SHA256SUMS has no line for $3; the download cannot be verified, so nothing was installed"
	verify_actual="$(digest_of "$1")"
	if [ "$verify_actual" != "$verify_expected" ]; then
		die "checksum mismatch for $3
  expected $verify_expected
  got      $verify_actual
Nothing was installed. Delete the file and try again; if it happens twice, do not run it — compare the hash with the one in the release notes at https://github.com/${REPO}/releases"
	fi
	say "verified $3 against SHA256SUMS"
}

fetch() { # fetch <url> <destination>
	if [ -n "$CURL" ]; then
		run "$CURL" -fsSL -o "$2" "$1"
	elif [ -n "$WGET" ]; then
		run "$WGET" -q -O "$2" "$1"
	else
		die "neither curl nor wget is on PATH; install one, or download the release by hand"
	fi
}

resolve_version() {
	if [ -n "$VERSION" ]; then
		return 0
	fi
	if [ "$DRY_RUN" -eq 1 ]; then
		say "+ curl -fsSL $API_LATEST   # to read tag_name"
		VERSION="vX.Y.Z"
		return 0
	fi
	if [ -n "$CURL" ]; then
		resolve_body="$("$CURL" -fsSL "$API_LATEST" 2>/dev/null || true)"
	elif [ -n "$WGET" ]; then
		resolve_body="$("$WGET" -qO- "$API_LATEST" 2>/dev/null || true)"
	else
		die "neither curl nor wget is on PATH; install one, or pass --version vX.Y.Z"
	fi
	# sed rather than jq: a one-line installer that needs a JSON parser
	# installed first is not a one-line installer.
	VERSION="$(printf '%s\n' "$resolve_body" |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
	[ -n "$VERSION" ] || die "could not read the latest version from $API_LATEST; pass --version vX.Y.Z, or check https://github.com/${REPO}/releases"
	say "latest release is $VERSION"
}

# --- install ------------------------------------------------------------------

# link_into puts one symlink in BIN_DIR, replacing whatever was there, so a
# second run of this script is an upgrade rather than an error.
link_into() { # link_into <target> <name>
	run ln -sfn "$1" "$BIN_DIR/$2"
	say "linked $BIN_DIR/$2 -> $1"
}

# PATH_MARK is the comment above the line this script adds to a shell startup
# file. A second run looks for it rather than for "a line that mentions
# .local/bin", which an owner's own file can have for reasons of its own.
PATH_MARK="# added by the AgentHub installer"

# startup_file names the file a new terminal of this owner's login shell reads,
# which is where a PATH line has to go to be seen, or prints nothing for a
# shell whose files this does not know how to write (csh, nu, ...). zsh has
# been the macOS default since 10.15. Terminal opens a bash as a login shell,
# which reads only the first of .bash_profile, .bash_login and .profile that
# exists — so the line goes into that one, because creating a .bash_profile
# beside an existing .profile would silently switch the .profile off. On Linux
# a terminal's bash reads .bashrc.
startup_file() {
	case "$(basename "${SHELL:-}")" in
	zsh) echo "${ZDOTDIR:-$HOME}/.zshrc" ;;
	bash)
		if [ "$OS_SLUG" = "darwin" ]; then
			for bash_login_file in .bash_profile .bash_login .profile; do
				if [ -e "$HOME/$bash_login_file" ]; then
					echo "$HOME/$bash_login_file"
					return 0
				fi
			done
			echo "$HOME/.bash_profile"
		else
			echo "$HOME/.bashrc"
		fi
		;;
	fish) echo "${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/agenthub.fish" ;;
	"")
		if [ "$OS_SLUG" = "darwin" ]; then
			echo "${ZDOTDIR:-$HOME}/.zshrc"
		else
			echo "$HOME/.profile"
		fi
		;;
	sh | dash | ash | ksh | mksh | yash) echo "$HOME/.profile" ;;
	esac
}

# ensure_path makes `ah` something a new terminal finds. macOS puts no
# ~/.local/bin on PATH at all, so without this the one-line install ends with
# a command that is not found. It appends one marked line to the shell's
# startup file, once; a --prefix install and --no-modify-path only say what to
# add, because an owner who chose either has chosen to manage PATH themselves.
ensure_path() {
	PATH_STATE="on PATH"
	case ":${PATH}:" in
	*":$BIN_DIR:"*) return 0 ;;
	esac
	PATH_HINT="note: $BIN_DIR is not on your PATH. Add it: export PATH=\"$BIN_DIR:\$PATH\""
	if [ -n "$PREFIX" ] || [ "$NO_MODIFY_PATH" -eq 1 ]; then
		say "$PATH_HINT"
		PATH_STATE="not on PATH; add $BIN_DIR to it"
		return 0
	fi
	PATH_FILE="$(startup_file)"
	if [ -z "$PATH_FILE" ]; then
		say "$PATH_HINT (this script does not write $(basename "$SHELL")'s startup files)"
		PATH_STATE="not on PATH; add $BIN_DIR to it"
		return 0
	fi
	case "$PATH_FILE" in
	*.fish)
		# shellcheck disable=SC2016 # written for fish to expand, not this shell
		path_line='contains -- $HOME/.local/bin $PATH; or set -gx PATH $HOME/.local/bin $PATH'
		;;
	*)
		# shellcheck disable=SC2016 # written for the startup file to expand
		path_line='export PATH="$HOME/.local/bin:$PATH"'
		;;
	esac
	if [ -f "$PATH_FILE" ] && grep -qF "$PATH_MARK" "$PATH_FILE"; then
		say "$PATH_FILE already adds $BIN_DIR to PATH"
	elif [ "$DRY_RUN" -eq 1 ]; then
		printf '+ append to %s: %s\n' "$(quote "$PATH_FILE")" "$path_line"
	# The app is already in place when this runs, and the service is not yet
	# registered, so a startup file that cannot be written (a read-only
	# symlink, as home-manager makes them) is a warning and not the end of the
	# install: under set -e a failed append would exit here, leaving the node
	# unregistered and — on Linux — stopped.
	elif (mkdir -p "$(dirname "$PATH_FILE")" && printf '\n%s\n%s\n' "$PATH_MARK" "$path_line" >>"$PATH_FILE") 2>/dev/null; then
		say "added $BIN_DIR to PATH in $PATH_FILE"
	else
		warn "could not write $PATH_FILE, so $BIN_DIR is not on your PATH. Add it: export PATH=\"$BIN_DIR:\$PATH\""
		PATH_STATE="not on PATH; add $BIN_DIR to it"
		return 0
	fi
	PATH_STATE="on PATH in new terminals (this one: export PATH=\"$BIN_DIR:\$PATH\")"
}

# unpack_tarball extracts into the temp directory and prints nothing; the
# unpacked directory lands in UNPACKED.
unpack_tarball() {
	run mkdir -p "$TEMP_DIR/unpacked"
	run tar -xzf "$ARCHIVE" -C "$TEMP_DIR/unpacked"
	UNPACKED=""
	for unpack_candidate in "$TEMP_DIR/unpacked"/*; do
		if [ -d "$unpack_candidate" ]; then
			UNPACKED="$unpack_candidate"
			break
		fi
	done
	if [ -z "$UNPACKED" ]; then
		if [ "$DRY_RUN" -eq 1 ]; then
			# Nothing was really extracted, so name the directory the release
			# puts inside the archive and carry on printing.
			UNPACKED="$TEMP_DIR/unpacked/$(basename "$ARCHIVE_NAME" .tar.gz)"
		else
			die "$ARCHIVE_NAME did not contain a directory; it is not an AgentHub archive"
		fi
	fi
}

# replace_directory swaps a directory for a freshly unpacked one. The old one is
# unlinked rather than written into: on Linux, writing over the binary of a
# running node fails with "text file busy", and the running node keeps its own
# already-open copy until the service is restarted below.
replace_directory() { # replace_directory <staging> <destination>
	run mkdir -p "$(dirname "$2")"
	run rm -rf "$2"
	run mv "$1" "$2"
}

# quit_running_app asks a running desktop app to quit before its bundle is
# deleted from under it. Replacing the bundle of a running process leaves the
# process alive with no files: menus stop working, and the next launch is from
# a bundle the old process still half-owns.
quit_running_app() {
	if ! command -v pgrep >/dev/null 2>&1; then
		return 0
	fi
	if ! pgrep -f "$APP_PATH/Contents/MacOS/desktop" >/dev/null 2>&1; then
		return 0
	fi
	say "agenthub-desktop is running; asking it to quit before its bundle is removed"
	run osascript -e 'quit app "agenthub-desktop"'
	if [ "$DRY_RUN" -eq 1 ]; then
		return 0
	fi
	quit_waited=0
	while [ "$quit_waited" -lt 10 ]; do
		if ! pgrep -f "$APP_PATH/Contents/MacOS/desktop" >/dev/null 2>&1; then
			say "agenthub-desktop quit"
			return 0
		fi
		sleep 1
		quit_waited=$((quit_waited + 1))
	done
	die "agenthub-desktop is still running after 10 seconds, and removing a running app's bundle breaks it.
Quit it yourself (right-click its Dock icon, Quit) and run this again, or re-run with --no-open after quitting."
}

install_macos_app() {
	MOUNT_POINT="$TEMP_DIR/mnt"
	run mkdir -p "$MOUNT_POINT"
	run hdiutil attach -nobrowse -readonly -quiet -mountpoint "$MOUNT_POINT" "$ARCHIVE"
	run mkdir -p "$APP_PARENT"
	quit_running_app
	run rm -rf "$APP_PATH"
	run ditto "$MOUNT_POINT/agenthub-desktop.app" "$APP_PATH"
	# The skill is copied out before the image is detached; installing it waits
	# for install_claude_skill, like the tarball's. Under --dry-run nothing is
	# mounted, so the copy is printed as if the image carried one.
	if [ "$NO_SKILL" -eq 0 ] && { [ "$DRY_RUN" -eq 1 ] || [ -f "$MOUNT_POINT/$DMG_SKILL_DIR/$SKILL_NAME/SKILL.md" ]; }; then
		run mkdir -p "$TEMP_DIR/dmg-skill"
		run cp -R "$MOUNT_POINT/$DMG_SKILL_DIR/$SKILL_NAME" "$TEMP_DIR/dmg-skill/$SKILL_NAME"
		SKILL_SOURCE="$TEMP_DIR/dmg-skill/$SKILL_NAME"
	fi
	run hdiutil detach "$MOUNT_POINT" -quiet
	MOUNT_POINT=""
	say "installed $APP_PATH"
	# Legitimate here, and only here: Gatekeeper's quarantine prompt exists to
	# ask "is this file what its publisher built?", and this script has just
	# answered that question with the release's own SHA256SUMS — a stronger
	# check than the one being skipped, done before a single byte was unpacked.
	# The app is not notarized (issue #66), so without this the first launch is
	# a dialog and a trip through System Settings for a file already proven.
	if [ "$DRY_RUN" -eq 1 ]; then
		run xattr -dr com.apple.quarantine "$APP_PATH"
	else
		xattr -dr com.apple.quarantine "$APP_PATH" 2>/dev/null || true
		say "cleared the quarantine flag (the digest above is what Gatekeeper would have stood in for)"
	fi
	AH="$APP_PATH/Contents/MacOS/ah"
	link_into "$AH" "ah"
}

install_tarball_tree() {
	check_install_directory "the AgentHub directory" "$AGENTHUB_DIR"
	unpack_tarball
	replace_directory "$UNPACKED" "$AGENTHUB_DIR"
	say "installed $AGENTHUB_DIR"
	AH="$AGENTHUB_DIR/ah"
	link_into "$AH" "ah"
	SKILL_SOURCE="$AGENTHUB_DIR/skills/$SKILL_NAME"
	if [ "$CLI_ONLY" -eq 1 ]; then
		link_into "$AGENTHUB_DIR/agenthub-node" "agenthub-node"
		link_into "$AGENTHUB_DIR/agenthub-mcp" "agenthub-mcp"
	else
		link_into "$AGENTHUB_DIR/agenthub-desktop" "agenthub-desktop"
	fi
}

# detect_webkit_abi reports which WebKit2GTK this machine can actually load.
#
# The Linux app binds WebKit through cgo, and WebKit2GTK ships as two ABIs that
# are not interchangeable: 4.1 is the libsoup3 build, 4.0 the libsoup2 one. A
# binary linked against either exits immediately on a machine that has only the
# other — "libwebkit2gtk-4.1.so.0: cannot open shared object file", no window,
# nothing in any log. Ubuntu 22.04 and its derivatives normally have 4.0
# installed; Ubuntu 24.04, Debian 13 and Fedora 39+ have 4.1.
#
# Some distributions can install the other ABI as well (22.04 has
# libwebkit2gtk-4.1-0 in jammy-updates/universe), so this is not about which
# runtime is obtainable — it is about which one is already there. Installing a
# package needs root, and this script never asks for it, so the only build that
# can start without a password is the one matching what is installed now.
#
# So the release builds Linux twice and this chooses the download. Reading
# ldconfig rather than /etc/os-release on purpose: what matters is the library
# that is installed, not the distribution that usually installs it, and a
# machine that has been upgraded, has a backport, or is a derivative nobody
# listed answers correctly here and would not from a name.
#
# ldconfig itself is the trap. It lives in /sbin, and /sbin is not on a
# non-root PATH on Debian and its derivatives, which is exactly the population
# running `curl ... | sh` as themselves on a 4.0 machine. `command -v ldconfig`
# misses there, so the absolute paths are tried too; a probe that gives up
# because of where a binary lives would silently take the 4.1 archive on the
# distribution this whole mechanism exists for.
#
# Sets WEBKIT_ABI to 4.1, 4.0, or empty when neither was found, and
# WEBKIT_PROBED to 1 when some ldconfig answered at all and 0 when none could
# be run (musl, NixOS, a stripped PATH). Empty with WEBKIT_PROBED=0 means "not
# known", which is a different thing from "not installed" and is reported as
# such rather than passed off as a diagnosis.
detect_webkit_abi() {
	WEBKIT_ABI=""
	WEBKIT_PROBED=0
	webkit_libs=""
	# AGENTHUB_LDCONFIG_PATHS exists so the "no ldconfig reachable" and "only at
	# an absolute path" branches can be tested on a machine that has a real
	# ldconfig in /sbin, which no PATH the tests control can hide. Same reason as
	# AGENTHUB_OS_RELEASE above it: the branch that matters is the one the test
	# machine is least likely to be in.
	for webkit_ldconfig in ${AGENTHUB_LDCONFIG_PATHS:-ldconfig /sbin/ldconfig /usr/sbin/ldconfig}; do
		case "$webkit_ldconfig" in
		/*) [ -x "$webkit_ldconfig" ] || continue ;;
		*) command -v "$webkit_ldconfig" >/dev/null 2>&1 || continue ;;
		esac
		webkit_libs="$("$webkit_ldconfig" -p 2>/dev/null || true)"
		[ -n "$webkit_libs" ] || continue
		WEBKIT_PROBED=1
		break
	done
	[ "$WEBKIT_PROBED" -eq 1 ] || return 0
	# 4.1 first. On the rare machine carrying both, it is the one still getting
	# security updates everywhere it ships.
	case "$webkit_libs" in
	*libwebkit2gtk-4.1.so.0*)
		case "$webkit_libs" in
		*libwebkit2gtk-4.0.so.37*) WEBKIT_ABI="both" ;;
		*) WEBKIT_ABI="4.1" ;;
		esac
		return 0
		;;
	esac
	case "$webkit_libs" in
	*libwebkit2gtk-4.0.so.37*)
		WEBKIT_ABI="4.0"
		return 0
		;;
	esac
	return 0
}

# webkit_hint says what to install when neither ABI is present. With one of them
# installed there is nothing to say: the matching build was downloaded, and the
# app starts.
webkit_hint() {
	[ -z "$WEBKIT_ABI" ] || return 0
	# AGENTHUB_OS_RELEASE exists so the branches below can be tested. Which
	# package name this prints is the whole value of the hint — it is read by
	# someone whose app will not start — and the names do not follow a pattern
	# (4.1 is libwebkit2gtk-4.1-0, 4.0 is libwebkit2gtk-4.0-37), so a wrong one
	# is both easy to write and impossible to notice from the machine that
	# happens to be running the tests.
	webkit_os_release="${AGENTHUB_OS_RELEASE:-/etc/os-release}"
	webkit_id=""
	if [ -r "$webkit_os_release" ]; then
		webkit_id="$(sed -n 's/^ID_LIKE=//p;' "$webkit_os_release" | tr -d '"' | head -n 1)"
		if [ -z "$webkit_id" ]; then
			webkit_id="$(sed -n 's/^ID=//p' "$webkit_os_release" | tr -d '"' | head -n 1)"
		fi
	fi
	if [ "${WEBKIT_PROBED:-0}" -eq 1 ]; then
		say "warning: no WebKit2GTK runtime was found, so the window will not open yet."
	else
		# Nothing was probed, so nothing is known — but staying quiet is worse
		# than saying so. The runtime may well be there; what is certain is
		# which archive was taken, and that is what gets said.
		say "warning: this machine's WebKit2GTK could not be read (no readable ldconfig),"
		say "         so the window may not open."
	fi
	say "         The 4.1 build was installed; install its runtime if it is missing:"
	case "$webkit_id" in
	*debian* | *ubuntu*) say "  sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0" ;;
	*fedora* | *rhel*) say "  sudo dnf install gtk3 webkit2gtk4.1" ;;
	*arch*) say "  sudo pacman -S gtk3 webkit2gtk-4.1" ;;
	*) say "  install GTK 3 and WebKit2GTK 4.1 with your package manager (the README.txt beside the app names the package)" ;;
	esac
	say "         If your distribution has only the older 4.0 runtime, install"
	say "         that instead and re-run this script: it will pick the 4.0 build."
}

# install_desktop_entry puts AgentHub in the applications menu.
#
# Without it the app is installed and runnable and invisible: someone who just
# installed a desktop application looks for it in their launcher, not in a
# terminal. Written per-user under XDG_DATA_HOME — the rest of this script
# installs per-user and never sudo, and a menu entry is not the place to start.
#
# Icon= is the absolute path of the icon inside the install tree rather than a
# themed name, so there is no hicolor directory to populate and no icon cache to
# refresh; both are extra steps that fail quietly on a minimal desktop.
install_desktop_entry() {
	[ "$OS_SLUG" = "linux" ] || return 0
	[ "$CLI_ONLY" -eq 0 ] || return 0

	desktop_dir="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
	desktop_file="$desktop_dir/agenthub.desktop"
	desktop_exec="$AGENTHUB_DIR/agenthub-desktop"
	desktop_icon="$AGENTHUB_DIR/appicon.png"

	run mkdir -p "$desktop_dir"
	if [ "$DRY_RUN" -eq 1 ]; then
		say "+ write $desktop_file"
	else
		# The icon is only named when it is really there: a .desktop entry
		# pointing Icon= at a missing file shows a broken-image placeholder in
		# some launchers, which looks worse than the generic icon they use when
		# the key is absent. Archives from before the icon shipped land here.
		# Exec= is quoted. The desktop-entry spec parses the value as a command
		# line, so an unquoted path under a $HOME with a space in it ("/home/Ann
		# Lee/.local/...") is read as a program plus an argument and the entry
		# launches nothing. Quoting is spec-conformant for any path, so it is
		# unconditional rather than a check nobody's machine would exercise.
		cat >"$desktop_file" <<DESKTOP_ENTRY
[Desktop Entry]
Type=Application
Name=AgentHub
Comment=Local control plane for coding-agent sessions
Exec="$desktop_exec"
Terminal=false
Categories=Development;
StartupWMClass=agenthub-desktop
DESKTOP_ENTRY
		if [ -f "$desktop_icon" ]; then
			echo "Icon=$desktop_icon" >>"$desktop_file"
		fi
		say "wrote $desktop_file"
	fi

	# Best effort: the entry is valid the moment it is written, and every
	# desktop environment picks it up on the next login regardless. This only
	# saves the owner that login, so its absence is not worth a warning.
	if command -v update-desktop-database >/dev/null 2>&1; then
		update-desktop-database "$desktop_dir" >/dev/null 2>&1 || true
	fi
}

# stop_linux_node stops the running node before its directory is replaced, so an
# upgrade does not leave systemd supervising a binary that no longer exists at
# that path. It is best effort: nothing installed means nothing to stop.
stop_linux_node() {
	if ! command -v systemctl >/dev/null 2>&1; then
		return 0
	fi
	if ! systemctl --user is-active --quiet agenthub-node.service 2>/dev/null; then
		return 0
	fi
	say "stopping the running node before replacing its binaries"
	run systemctl --user stop agenthub-node.service
}

# --- the Claude Code skill ------------------------------------------------------

# skill_directory is where Claude Code looks for a user-level skill.
skill_directory() {
	printf '%s' "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills/$SKILL_NAME"
}

# skill_is_ours says yes only to a real directory holding the marker this
# script writes. A symlink is never ours, whatever it points at: it is how an
# owner who keeps skills elsewhere wires them in.
skill_is_ours() { # skill_is_ours <directory>
	[ -d "$1" ] && [ ! -L "$1" ] && [ -f "$1/$SKILL_MARKER" ]
}

# install_claude_skill puts the agenthub-watch skill where Claude Code finds it,
# so an agent on this machine knows `ah` is there to be used. It copies from the
# asset that was just verified and unpacked — never from the network — and
# replaces only a copy this script put there before. Nothing about it is worth
# failing an install over: the app is in place, and a skill that could not be
# written is a warning with the path to fix.
install_claude_skill() {
	if [ "$NO_SKILL" -eq 1 ]; then
		say "skipped the Claude Code skill (--no-skill)"
		SKILL_STATE="not installed (--no-skill)"
		return 0
	fi
	skill_dest="$(skill_directory)"
	# Under --dry-run nothing was unpacked, so the source cannot be looked at;
	# print what a release that carries the skill would get.
	if [ -z "$SKILL_SOURCE" ] || { [ "$DRY_RUN" -eq 0 ] && [ ! -f "$SKILL_SOURCE/SKILL.md" ]; }; then
		warn "this release does not carry the $SKILL_NAME skill for Claude Code (releases before it do not), so none was installed; everything else was"
		SKILL_STATE="not installed (this release does not carry it)"
		return 0
	fi
	# A --prefix install writes nothing outside the prefix, for the same reason
	# it leaves the shell's startup files alone: an owner who chose where
	# everything goes has chosen to wire it up themselves.
	if [ -n "$PREFIX" ]; then
		say "skipped the Claude Code skill: a --prefix install writes nothing outside $PREFIX"
		SKILL_STATE="not installed (--prefix writes nothing outside $PREFIX)"
		return 0
	fi
	if exists "$skill_dest" && ! skill_is_ours "$skill_dest"; then
		say "left $skill_dest alone: it is not a copy this script installed (a link or a copy of your own)"
		SKILL_STATE="$skill_dest is your own; left as it is"
		return 0
	fi
	skill_stage="$TEMP_DIR/skill-stage/$SKILL_NAME"
	if run mkdir -p "$TEMP_DIR/skill-stage" &&
		run cp -R "$SKILL_SOURCE" "$skill_stage" &&
		run touch "$skill_stage/$SKILL_MARKER" &&
		run mkdir -p "$(dirname "$skill_dest")" &&
		run rm -rf "$skill_dest" &&
		run mv "$skill_stage" "$skill_dest"; then
		say "installed the Claude Code skill $skill_dest"
		SKILL_STATE="$skill_dest (Claude Code sessions started from now on load it; running ones do not)"
	else
		warn "could not install the Claude Code skill into $skill_dest; everything else was installed"
		SKILL_STATE="not installed (could not write $skill_dest)"
	fi
}

# --- the background service ---------------------------------------------------

# status_ah names the ah to ask about the current registration: the one just
# installed, or — under --dry-run, where nothing was installed — whatever ah is
# already on PATH, which is the build that wrote the unit being asked about.
status_ah() {
	if [ -n "$AH" ] && [ -x "$AH" ]; then
		printf '%s' "$AH"
		return 0
	fi
	command -v ah 2>/dev/null || true
}

# read_service_registration reads the unit that is registered now.
#
# `ah service install` replaces an existing registration outright, and the
# database path is not remembered anywhere else: a reinstall that does not
# carry --db forward writes a unit with no --db, the node then opens its
# default database, and the machine comes back with a new identity, no
# pairings and no history. Nothing on screen would say so. So this is read
# first, and when it cannot be read the service is not touched at all.
#
# Sets SERVICE_READ (1 when the answer is trustworthy), SERVICE_INSTALLED,
# SERVICE_DB and SERVICE_EXTRA (other node flags the unit carries).
read_service_registration() {
	SERVICE_READ=0
	SERVICE_INSTALLED=0
	SERVICE_DB=""
	SERVICE_EXTRA=""
	service_ah="$(status_ah)"
	if [ -z "$service_ah" ]; then
		# No ah anywhere to ask, which is also no unit anywhere to preserve.
		SERVICE_READ=1
		return 0
	fi
	service_json="$("$service_ah" --json service status 2>/dev/null || true)"
	service_flat="$(printf '%s' "$service_json" | tr '\n\t' '  ')"
	service_installed="$(printf '%s' "$service_flat" |
		sed -n 's/.*"Installed"[[:space:]]*:[[:space:]]*\([a-z]*\).*/\1/p')"
	case "$service_installed" in
	true) SERVICE_INSTALLED=1 ;;
	false) SERVICE_INSTALLED=0 ;;
	# Anything else is a command that failed, an older ah with no --json, or
	# output this does not understand. Not "no service": unknown.
	*) return 0 ;;
	esac
	SERVICE_READ=1
	service_args="$(printf '%s' "$service_flat" |
		sed -n 's/.*"Arguments"[[:space:]]*:[[:space:]]*\[\([^]]*\)\].*/\1/p')"
	SERVICE_DB="$(printf '%s' "$service_args" |
		sed -n 's/.*"-*db"[[:space:]]*,[[:space:]]*"\([^"]*\)".*/\1/p')"
	if [ -z "$SERVICE_DB" ]; then
		SERVICE_DB="$(printf '%s' "$service_args" |
			sed -n 's/.*"-*db=\([^"]*\)".*/\1/p')"
	fi
	SERVICE_EXTRA="$(printf '%s' "$service_args" | tr ',' '\n' |
		sed -n 's/.*"--\([a-z][a-z-]*\)".*/--\1/p' |
		grep -v '^--db$' | grep -v '^--node-binary$' | tr '\n' ' ')"
}

register_service() {
	read_service_registration
	if [ "$SERVICE_READ" -eq 0 ]; then
		warn "could not read the current background service from \"$(status_ah)\"."
		say "The app is installed, but the background node was left alone: registering it"
		say "blind would re-point an existing node at a different database, and that node"
		say "would come back with a new identity and no pairings. Register it yourself:"
		say ""
		say "  \"$AH\" service install --db <path to the node's database>"
		say ""
		say "The unit file names the database it uses now: \"$AH\" service status"
		SERVICE_STATE="not registered; run \"$AH\" service install --db <path to the node's database>"
		return 0
	fi
	say "registering the background node"
	if [ "$SERVICE_INSTALLED" -eq 1 ] && [ -n "$SERVICE_DB" ]; then
		say "keeping the node's database at $SERVICE_DB"
		say "note: the unit's node binary is replaced on purpose — it now points at the one just installed"
		if [ -n "$SERVICE_EXTRA" ]; then
			warn "the old unit also carried ${SERVICE_EXTRA}; those are not carried over. The node remembers its network settings in its database (\`ah settings set ...\`), or pass them to \`ah service install\` yourself."
		fi
		run "$AH" service install --db "$SERVICE_DB"
	else
		run "$AH" service install
	fi
	run "$AH" service status
	SERVICE_STATE="running as a background service (\"$AH\" service status shows it)"
}

# --- what the reader is left with ----------------------------------------------

closing_lines() {
	say ""
	if [ -n "$VERSION" ]; then
		say "done. AgentHub $VERSION is installed:"
	else
		say "done. AgentHub is installed:"
	fi
	if [ "$INSTALLED_APP" -eq 1 ]; then
		say "  app:  $APP_PATH"
	fi
	if [ "$INSTALLED_TREE" -eq 1 ]; then
		say "  files: $AGENTHUB_DIR"
	fi
	say "  ah:   $BIN_DIR/ah   (\"$AH --version\" says which build)"
	say "  PATH: $PATH_STATE"
	say "  node: $SERVICE_STATE"
	say "  skill: $SKILL_STATE"
	say ""
	if [ "$CLI_ONLY" -eq 0 ]; then
		if [ "$OS_SLUG" = "darwin" ] && [ "$INSTALLED_APP" -eq 1 ] && [ -z "$PREFIX" ]; then
			say "Open agenthub-desktop from Applications; it starts on a setup checklist."
		elif [ "$OS_SLUG" = "darwin" ] && [ "$INSTALLED_APP" -eq 1 ]; then
			say "Open $APP_PATH; it starts on a setup checklist."
		else
			say "Open agenthub-desktop ($BIN_DIR/agenthub-desktop); it starts on a setup checklist."
		fi
	fi
	say "To pass flags through the pipe, put them after \`sh -s --\`:"
	say "  curl -fsSL $RAW_SELF | sh -s -- --no-service"
}

# --- uninstall -------------------------------------------------------------------

# Everything below removes only what this script (or `ah service install`, or
# the app) put in place, and each removal checks that the thing is ours first:
# a symlink is removed only when it points into the install, a directory only
# when it holds one. The owner's own files are never the collateral of a
# pattern that happened to match.

# BUNDLE_ID is the app's CFBundleIdentifier, which names its caches and
# preferences under ~/Library. Read from the installed bundle when there is one.
BUNDLE_ID="com.wails.agenthub-desktop"

# remove_path deletes one file or directory when it exists, and says so.
remove_path() { # remove_path <path>
	if exists "$1"; then
		run rm -rf "$1"
		say "removed $1"
	fi
}

# remove_link deletes BIN_DIR/<name> when it is a symlink into this install. A
# real file, or a link to some other ah (a source build, say), is left alone.
remove_link() { # remove_link <name>
	link_path="$BIN_DIR/$1"
	[ -L "$link_path" ] || return 0
	link_target="$(readlink "$link_path" 2>/dev/null || true)"
	case "$link_target" in
	"") ;;
	"${APP_PATH:-/nonexistent-app-path}"/* | "$AGENTHUB_DIR"/*)
		run rm -f "$link_path"
		say "removed $link_path"
		;;
	*) say "left $link_path alone: it points at $link_target, not at this install" ;;
	esac
}

# uninstall_ah names an ah that can take the service down: the installed one,
# then whatever is on PATH.
uninstall_ah() {
	for uninstall_candidate in "${APP_PATH:+$APP_PATH/Contents/MacOS/ah}" "$AGENTHUB_DIR/ah"; do
		[ -n "$uninstall_candidate" ] || continue
		if [ -x "$uninstall_candidate" ]; then
			printf '%s' "$uninstall_candidate"
			return 0
		fi
	done
	command -v ah 2>/dev/null || true
}

# remove_service stops the node and removes its registration, before any file
# it runs from is deleted: launchd and systemd both restart a node that exits,
# and one pointed at a binary that is gone fails in a loop at every login.
remove_service() {
	if [ -n "$AH" ]; then
		if ! run "$AH" service uninstall; then
			die "\"$AH\" service uninstall failed, so nothing else was removed: deleting the app under a registered service leaves it failing at every login. Fix that first, then run this again."
		fi
		return 0
	fi
	# No ah anywhere, which is an install half-removed by hand. The unit is
	# still where `ah service install` wrote it; take it down the same way.
	case "$OS_SLUG" in
	darwin)
		uninstall_unit="$HOME/Library/LaunchAgents/local.agenthub.node.plist"
		if exists "$uninstall_unit"; then
			run launchctl bootout "gui/$(id -u)/local.agenthub.node" || true
			remove_path "$uninstall_unit"
		fi
		;;
	linux)
		uninstall_unit="$HOME/.config/systemd/user/agenthub-node.service"
		if exists "$uninstall_unit"; then
			run systemctl --user disable --now agenthub-node.service || true
			remove_path "$uninstall_unit"
			run systemctl --user daemon-reload || true
		fi
		;;
	esac
}

# stop_window_node stops a node the desktop window started on its own (it does
# that when no service is running) — a detached process the service manager
# knows nothing about, still holding the database open. Matched by the full
# path of this install's binary, so no other agenthub-node is touched.
stop_window_node() {
	command -v pkill >/dev/null 2>&1 || return 0
	for window_node in "${APP_PATH:+$APP_PATH/Contents/MacOS/agenthub-node}" "$AGENTHUB_DIR/agenthub-node"; do
		[ -n "$window_node" ] || continue
		if pgrep -f "$window_node" >/dev/null 2>&1; then
			run pkill -f "$window_node" || true
			say "stopped the node started from $window_node"
		fi
	done
}

# unpath_file takes the lines ensure_path appended back out of one startup
# file: the marker, the line under it when it is the one this script wrote,
# and the blank line it put above them. The rest of the file is rewritten
# byte for byte, in place — through `cat >`, so a symlinked file stays a link.
unpath_file() { # unpath_file <file>
	[ -f "$1" ] || return 0
	grep -qF "$PATH_MARK" "$1" 2>/dev/null || return 0
	case "$1" in
	*/fish/conf.d/agenthub.fish)
		# The whole file is this script's.
		remove_path "$1"
		return 0
		;;
	esac
	if [ "$DRY_RUN" -eq 1 ]; then
		say "+ remove the AgentHub PATH lines from $(quote "$1")"
		return 0
	fi
	# shellcheck disable=SC2016 # the literal line ensure_path writes
	unpath_line='export PATH="$HOME/.local/bin:$PATH"'
	unpath_tmp="$TEMP_DIR/startup-file"
	awk -v mark="$PATH_MARK" -v line="$unpath_line" '
		skip { skip = 0; if ($0 == line) next }
		$0 == mark { held = 0; skip = 1; next }
		{
			if (held) print ""
			held = 0
			if ($0 == "") { held = 1; next }
			print
		}
		END { if (held) print "" }' "$1" >"$unpath_tmp"
	if (cat "$unpath_tmp" >"$1") 2>/dev/null; then
		say "removed the AgentHub PATH lines from $1"
	else
		warn "could not write $1; remove the \"$PATH_MARK\" line and the one under it yourself"
	fi
}

# default_data_directory is where the node keeps node.key and its database
# when nothing says otherwise (cmd/agenthub-node: os.UserConfigDir()/agenthub).
default_data_directory() {
	if [ "$OS_SLUG" = "darwin" ]; then
		printf '%s' "$HOME/Library/Application Support/agenthub"
	else
		printf '%s' "${XDG_CONFIG_HOME:-$HOME/.config}/agenthub"
	fi
}

# data_directory is the directory beside the database the registered unit
# names — node.key lives beside the database — or the default.
data_directory() {
	if [ -n "$SERVICE_DB" ]; then
		dirname "$SERVICE_DB"
	else
		default_data_directory
	fi
}

log_directory() {
	if [ "$OS_SLUG" = "darwin" ]; then
		printf '%s' "$HOME/Library/Logs/agenthub"
	else
		printf '%s' "${XDG_STATE_HOME:-$HOME/.local/state}/agenthub"
	fi
}

# purge_data deletes the node's identity and history. The default directory
# is the node's own and goes whole. A database the owner put anywhere else —
# ./data in a checkout, or a checkout itself, which is also a directory called
# agenthub — takes only its own files with it.
purge_data() {
	purge_dir="$(data_directory)"
	if [ "$SERVICE_READ" -eq 0 ]; then
		warn "the registered service could not be read, so a database kept somewhere other than $(default_data_directory) was not looked for"
	fi
	if [ "$(absolute_path "$purge_dir")" = "$(absolute_path "$(default_data_directory)")" ]; then
		remove_path "$purge_dir"
	else
		purge_db="${SERVICE_DB:-$purge_dir/agenthub.db}"
		for purge_file in "$purge_dir/node.key" "$purge_db" "$purge_db-wal" "$purge_db-shm"; do
			remove_path "$purge_file"
		done
	fi
	remove_path "$(log_directory)"
}

uninstall() {
	TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/agenthub-uninstall.XXXXXX")"
	trap cleanup EXIT INT TERM HUP

	# A .app bundle is a macOS install and nothing else. On Linux APP_PATH
	# still names /Applications/agenthub-desktop.app (it is computed for both),
	# and every step below that looks there would reach a directory this
	# uninstall has no business in — on a mac whose uname says Linux, which is
	# how the tests run, that is the developer's own app. Emptied here, it
	# matches nothing: no ah is taken from it, no link counts as pointing into
	# it, no process is matched by it and nothing under it is removed.
	if [ "$OS_SLUG" != "darwin" ]; then
		APP_PATH=""
	fi

	AH="$(uninstall_ah)"
	# Read before the service goes: the unit is the only record of a database
	# kept somewhere other than the default, and --purge needs to know where.
	read_service_registration
	if [ -n "$APP_PATH" ] && [ -f "$APP_PATH/Contents/Info.plist" ]; then
		uninstall_bundle_id="$(/usr/libexec/PlistBuddy -c "Print :CFBundleIdentifier" "$APP_PATH/Contents/Info.plist" 2>/dev/null || true)"
		[ -z "$uninstall_bundle_id" ] || BUNDLE_ID="$uninstall_bundle_id"
	fi

	remove_service
	if [ "$OS_SLUG" = "darwin" ]; then
		quit_running_app
	fi
	stop_window_node

	for uninstall_name in ah agenthub-node agenthub-mcp agenthub-desktop; do
		remove_link "$uninstall_name"
	done
	if [ -z "$APP_PATH" ]; then
		:
	elif exists "$APP_PATH/Contents/MacOS/ah" || exists "$APP_PATH/Contents/MacOS/desktop"; then
		remove_path "$APP_PATH"
	elif exists "$APP_PATH"; then
		say "left $APP_PATH alone: it does not look like the AgentHub app"
	fi
	if [ -d "$AGENTHUB_DIR" ]; then
		if holds_agenthub "$AGENTHUB_DIR" && [ "$(absolute_path "$AGENTHUB_DIR")" != "$(absolute_path "$HOME")" ]; then
			remove_path "$AGENTHUB_DIR"
		else
			say "left $AGENTHUB_DIR alone: it holds no AgentHub install"
		fi
	fi
	if [ -n "$PREFIX" ]; then
		remove_path "$PREFIX/$MARKER"
	fi

	uninstall_skill="$(skill_directory)"
	# A --prefix install never put one there, so a --prefix uninstall does not
	# go looking.
	if [ "$NO_SKILL" -eq 1 ] || [ -n "$PREFIX" ]; then
		:
	elif skill_is_ours "$uninstall_skill"; then
		remove_path "$uninstall_skill"
	elif exists "$uninstall_skill"; then
		say "left $uninstall_skill alone: it is not a copy this script installed"
	fi

	uninstall_entry="${XDG_DATA_HOME:-$HOME/.local/share}/applications/agenthub.desktop"
	if [ -f "$uninstall_entry" ] && grep -q '^StartupWMClass=agenthub-desktop$' "$uninstall_entry" 2>/dev/null; then
		remove_path "$uninstall_entry"
	fi

	for uninstall_rc in "${ZDOTDIR:-$HOME}/.zshrc" "$HOME/.bash_profile" "$HOME/.bash_login" \
		"$HOME/.profile" "$HOME/.bashrc" "${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/agenthub.fish"; do
		unpath_file "$uninstall_rc"
	done

	if [ "$OS_SLUG" = "darwin" ]; then
		for uninstall_cache in "Caches/$BUNDLE_ID" "WebKit/$BUNDLE_ID" "HTTPStorages/$BUNDLE_ID" \
			"Preferences/$BUNDLE_ID.plist" "Saved Application State/$BUNDLE_ID.savedState"; do
			remove_path "$HOME/Library/$uninstall_cache"
		done
	fi

	if [ "$PURGE" -eq 1 ]; then
		purge_data
	fi

	say ""
	say "done. AgentHub is uninstalled."
	if [ "$PURGE" -eq 1 ]; then
		say "The node's identity and database are deleted too; installing again makes a new node,"
		say "so machines paired with this one need to pair again."
	else
		say "Kept: the node's identity and database in $(data_directory), and its logs."
		say "Installing again brings the same node back with its pairings. To delete them as well:"
		say "  curl -fsSL $RAW_SELF | sh -s -- --uninstall --purge"
	fi
	say "Machines paired with this one still list it; remove it there with \`ah revoke <node-id>\` or from their app."
}

# --- main ----------------------------------------------------------------------

main() {
	parse_arguments "$@"
	if [ "$PURGE" -eq 1 ] && [ "$UNINSTALL" -eq 0 ]; then
		die "--purge only goes with --uninstall"
	fi
	if [ "$UNINSTALL" -eq 1 ] && { [ -n "$FROM" ] || [ -n "$VERSION" ] || [ "$CLI_ONLY" -eq 1 ]; }; then
		die "--uninstall removes whatever is installed; --from, --version and --cli-only do not go with it"
	fi

	# Everything below builds a path out of HOME. Unset, it would reach `rm -rf`
	# as an empty string and the shell would say "HOME: unbound variable" from
	# whichever line got there first.
	[ -n "${HOME:-}" ] || die "HOME is not set, so there is nowhere to install to; run this as a normal logged-in user, or pass --prefix DIR"

	OS="$(uname -s)"
	MACHINE="$(uname -m)"
	case "$OS" in
	Darwin) OS_SLUG="darwin" ;;
	Linux) OS_SLUG="linux" ;;
	*) die "$OS is not supported; macOS and Linux only (Windows has an installer on the releases page)" ;;
	esac
	case "$MACHINE" in
	arm64 | aarch64) ARCH_SLUG="arm64" ;;
	x86_64 | amd64) ARCH_SLUG="amd64" ;;
	*) die "unsupported processor $MACHINE; the releases page lists what is built" ;;
	esac

	# The desktop app ships for darwin as one universal .dmg and for linux as an
	# amd64 tarball only. A linux arm64 machine gets the command line archive,
	# and is told so rather than left wondering where the window is.
	if [ "$UNINSTALL" -eq 0 ] && [ "$OS_SLUG" = "linux" ] && [ "$ARCH_SLUG" = "arm64" ] && [ "$CLI_ONLY" -eq 0 ]; then
		say "no desktop build exists for linux arm64; installing the command line tools only"
		CLI_ONLY=1
	fi

	# Which of the two Linux desktop builds this machine can run. Probed here,
	# before any asset name is built, because it decides the download and not
	# just what is printed afterwards. DESKTOP_SUFFIX is what distinguishes the
	# two file names on the release page.
	WEBKIT_ABI=""
	WEBKIT_PROBED=0
	DESKTOP_SUFFIX=""
	if [ "$UNINSTALL" -eq 0 ] && [ "$OS_SLUG" = "linux" ] && [ "$CLI_ONLY" -eq 0 ]; then
		detect_webkit_abi
		case "$WEBKIT_ABI" in
		4.0) DESKTOP_SUFFIX="_webkit40" ;;
		# Everything else takes the 4.1 build: it is what every distribution
		# still adding WebKit2GTK ships, so where nothing is installed it is
		# also the ABI whose runtime the owner can obtain afterwards.
		*) DESKTOP_SUFFIX="" ;;
		esac
		# Every branch says which build it took and why, not only the 4.0 one.
		# The reason is the half that is worth reading: someone whose window
		# does not open needs to know whether the choice was made from what is
		# installed or made blind.
		#
		# Only when it is choosing a download. With --from the file was named on
		# the command line, nothing here selected it, and claiming otherwise
		# would tell someone who took the wrong archive by hand that the right
		# one had been picked for them.
		if [ -z "$FROM" ]; then
			case "$WEBKIT_ABI" in
			4.0) say "this machine has WebKit2GTK 4.0; taking the build linked against it" ;;
			4.1) say "this machine has WebKit2GTK 4.1; taking the build linked against it" ;;
			both) say "this machine has both WebKit2GTK ABIs; taking the 4.1 build" ;;
			*)
				if [ "$WEBKIT_PROBED" -eq 1 ]; then
					say "this machine has no WebKit2GTK runtime installed; taking the 4.1 build"
				else
					# Silence here was the original defect in another form: on a
					# Debian machine whose PATH has no ldconfig the 4.1 archive
					# was taken with nothing printed at all, which is the exact
					# failure this probe exists to prevent.
					say "could not read this machine's WebKit2GTK ABI (no readable ldconfig); taking the 4.1 build"
				fi
				;;
			esac
		fi
	fi

	# --- where things go -------------------------------------------------------

	if [ -n "$PREFIX" ]; then
		check_install_directory "--prefix" "$PREFIX"
		APP_PARENT="$PREFIX"
		BIN_DIR="$PREFIX/bin"
		AGENTHUB_DIR="${AGENTHUB_HOME:-$PREFIX/share/agenthub}"
	else
		BIN_DIR="$HOME/.local/bin"
		AGENTHUB_DIR="${AGENTHUB_HOME:-$HOME/.local/share/agenthub}"
		# /Applications when this account can write it, ~/Applications
		# otherwise. Never sudo: an installer that asks for the administrator
		# password to put a per-user background service in place is asking for
		# more than it needs.
		# AGENTHUB_APPLICATIONS exists for the tests, like AGENTHUB_LDCONFIG_PATHS:
		# a run that fakes uname still sees this machine's real /Applications,
		# and a test of what --uninstall leaves alone needs an app there that is
		# not the developer's own.
		applications="${AGENTHUB_APPLICATIONS:-/Applications}"
		if [ -w "$applications" ]; then
			APP_PARENT="$applications"
		else
			APP_PARENT="$HOME/Applications"
		fi
	fi
	APP_PATH="$APP_PARENT/agenthub-desktop.app"
	INSTALLED_APP=0
	INSTALLED_TREE=0

	if [ "$UNINSTALL" -eq 1 ]; then
		uninstall
		return 0
	fi

	# --- the tools this needs --------------------------------------------------

	CURL="$(command -v curl 2>/dev/null || true)"
	WGET="$(command -v wget 2>/dev/null || true)"
	if command -v shasum >/dev/null 2>&1; then
		SHA_CMD="shasum"
		SHA_ARGS="-a 256"
	elif command -v sha256sum >/dev/null 2>&1; then
		SHA_CMD="sha256sum"
		SHA_ARGS=""
	else
		die "neither shasum nor sha256sum is on PATH; without one the download cannot be verified"
	fi

	# --- get the archive -------------------------------------------------------

	TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/agenthub-install.XXXXXX")"
	trap cleanup EXIT INT TERM HUP

	if [ -n "$FROM" ]; then
		[ -f "$FROM" ] || die "--from $FROM is not a file"
		ARCHIVE="$FROM"
		ARCHIVE_NAME="$(basename "$FROM")"
		case "$ARCHIVE_NAME" in
		*.dmg) ARCHIVE_KIND="dmg" ;;
		*.tar.gz) ARCHIVE_KIND="tgz" ;;
		*) die "--from wants a .dmg or a .tar.gz, got $ARCHIVE_NAME" ;;
		esac
		SUMS="$(dirname "$FROM")/SHA256SUMS"
		if [ -f "$SUMS" ]; then
			verify "$ARCHIVE" "$SUMS" "$ARCHIVE_NAME"
		else
			warn "no SHA256SUMS beside $ARCHIVE_NAME; installing it unverified because you named the file yourself"
		fi
		say "installing from $ARCHIVE_NAME"
	else
		resolve_version
		if [ "$CLI_ONLY" -eq 1 ]; then
			ARCHIVE_NAME="agenthub_${VERSION}_${OS_SLUG}_${ARCH_SLUG}.tar.gz"
			ARCHIVE_KIND="tgz"
		elif [ "$OS_SLUG" = "darwin" ]; then
			ARCHIVE_NAME="agenthub-desktop_${VERSION}_darwin_universal.dmg"
			ARCHIVE_KIND="dmg"
		else
			ARCHIVE_NAME="agenthub-desktop_${VERSION}_linux_amd64${DESKTOP_SUFFIX}.tar.gz"
			ARCHIVE_KIND="tgz"
		fi
		ARCHIVE="$TEMP_DIR/$ARCHIVE_NAME"
		SUMS="$TEMP_DIR/SHA256SUMS"
		say "downloading $ARCHIVE_NAME"
		download_failed="release $VERSION has no $ARCHIVE_NAME.
Releases before the desktop builds carry the command line archives only — re-run with --cli-only, or pick a newer tag at https://github.com/${REPO}/releases"
		if ! fetch "$DOWNLOAD_BASE/$VERSION/$ARCHIVE_NAME" "$ARCHIVE"; then
			# The _webkit40 asset only exists from the release that introduced the
			# second Linux build. Asked for an older tag, a 4.0 machine would
			# otherwise be told its only option was --cli-only, when the plain
			# archive is right there — it is the 4.1 build, so it may not start,
			# and that is said rather than left to be discovered.
			[ -n "$DESKTOP_SUFFIX" ] || die "$download_failed"
			warn "release $VERSION has no $ARCHIVE_NAME; that tag predates the WebKit 4.0 build.
Falling back to agenthub-desktop_${VERSION}_linux_amd64.tar.gz, which is linked against WebKit2GTK 4.1 and may not start on this machine. For a build that matches it, pick a newer tag at https://github.com/${REPO}/releases"
			DESKTOP_SUFFIX=""
			ARCHIVE_NAME="agenthub-desktop_${VERSION}_linux_amd64.tar.gz"
			ARCHIVE="$TEMP_DIR/$ARCHIVE_NAME"
			say "downloading $ARCHIVE_NAME"
			fetch "$DOWNLOAD_BASE/$VERSION/$ARCHIVE_NAME" "$ARCHIVE" ||
				die "release $VERSION has no $ARCHIVE_NAME.
Releases before the desktop builds carry the command line archives only — re-run with --cli-only, or pick a newer tag at https://github.com/${REPO}/releases"
		fi
		fetch "$DOWNLOAD_BASE/$VERSION/SHA256SUMS" "$SUMS" ||
			die "release $VERSION has no SHA256SUMS, so the download cannot be verified; nothing was installed"
		verify "$ARCHIVE" "$SUMS" "$ARCHIVE_NAME"
	fi

	# --- install ---------------------------------------------------------------

	run mkdir -p "$BIN_DIR"
	if [ -n "$PREFIX" ]; then
		# So that the next run can tell this directory is one of ours before it
		# replaces anything in it.
		run mkdir -p "$PREFIX"
		run touch "$PREFIX/$MARKER"
	fi

	case "$OS_SLUG" in
	darwin)
		if [ "$ARCHIVE_KIND" = "dmg" ]; then
			install_macos_app
			INSTALLED_APP=1
		else
			install_tarball_tree
			INSTALLED_TREE=1
		fi
		;;
	linux)
		if [ "$ARCHIVE_KIND" = "dmg" ]; then
			die "a .dmg is a macOS disk image; on Linux pass the linux tar.gz"
		fi
		if [ "$NO_SERVICE" -eq 0 ]; then
			stop_linux_node
		fi
		install_tarball_tree
		INSTALLED_TREE=1
		if [ "$CLI_ONLY" -eq 0 ]; then
			install_desktop_entry
			webkit_hint
		fi
		;;
	esac

	ensure_path
	install_claude_skill

	# `ah service install` writes the unit that starts the node at login, and it
	# writes the absolute path of the agenthub-node beside this ah. Running it on
	# every upgrade — rather than only the first time — is what keeps an owner
	# who moved from ~/Applications to /Applications from being left with a unit
	# that points at a path this script just deleted.
	if [ "$NO_SERVICE" -eq 1 ]; then
		say "skipped the background service (--no-service)"
		SERVICE_STATE="not registered (--no-service); \"$AH\" service install registers it"
	else
		register_service
	fi

	if [ "$OS_SLUG" = "darwin" ] && [ "$CLI_ONLY" -eq 0 ] && [ "$ARCHIVE_KIND" = "dmg" ] && [ "$NO_OPEN" -eq 0 ]; then
		run open "$APP_PATH"
	fi

	closing_lines
}

main "$@"
