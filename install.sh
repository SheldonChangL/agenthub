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

AH=""
SERVICE_STATE=""

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
  --prefix DIR       install under DIR instead of /Applications or ~/.local;
                     symlinks then go to DIR/bin
  --from FILE        install from a local .dmg or .tar.gz instead of
                     downloading; a SHA256SUMS beside it is still checked
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

path_hint() {
	case ":${PATH}:" in
	*":$BIN_DIR:"*) ;;
	*) say "note: $BIN_DIR is not on your PATH. Add it: export PATH=\"$BIN_DIR:\$PATH\"" ;;
	esac
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
	say "agenthub-desktop is running; asking it to quit before its bundle is replaced"
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
	die "agenthub-desktop is still running after 10 seconds, and replacing a running app's bundle breaks it.
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
	if [ "$CLI_ONLY" -eq 1 ]; then
		link_into "$AGENTHUB_DIR/agenthub-node" "agenthub-node"
		link_into "$AGENTHUB_DIR/agenthub-mcp" "agenthub-mcp"
	else
		link_into "$AGENTHUB_DIR/agenthub-desktop" "agenthub-desktop"
	fi
}

# webkit_hint says what to install when the runtime the Linux app links is
# missing. The app is dynamically linked against the system WebKit, so a missing
# library is a window that never opens and no message anywhere.
webkit_hint() {
	if ! command -v ldconfig >/dev/null 2>&1; then
		return 0
	fi
	if ldconfig -p 2>/dev/null | grep -q 'libwebkit2gtk-4\.1\.so\.0'; then
		return 0
	fi
	webkit_id=""
	if [ -r /etc/os-release ]; then
		webkit_id="$(sed -n 's/^ID_LIKE=//p;' /etc/os-release | tr -d '"' | head -n 1)"
		if [ -z "$webkit_id" ]; then
			webkit_id="$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"' | head -n 1)"
		fi
	fi
	say "warning: libwebkit2gtk-4.1.so.0 was not found; the window will not open until the runtime is installed:"
	case "$webkit_id" in
	*debian* | *ubuntu*) say "  sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0" ;;
	*fedora* | *rhel*) say "  sudo dnf install gtk3 webkit2gtk4.1" ;;
	*arch*) say "  sudo pacman -S gtk3 webkit2gtk-4.1" ;;
	*) say "  install GTK 3 and WebKit2GTK 4.1 with your package manager (the README.txt beside the app names the package)" ;;
	esac
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
	say "  node: $SERVICE_STATE"
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

# --- main ----------------------------------------------------------------------

main() {
	parse_arguments "$@"

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
	if [ "$OS_SLUG" = "linux" ] && [ "$ARCH_SLUG" = "arm64" ] && [ "$CLI_ONLY" -eq 0 ]; then
		say "no desktop build exists for linux arm64; installing the command line tools only"
		CLI_ONLY=1
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
		if [ -w /Applications ]; then
			APP_PARENT="/Applications"
		else
			APP_PARENT="$HOME/Applications"
		fi
	fi
	APP_PATH="$APP_PARENT/agenthub-desktop.app"
	INSTALLED_APP=0
	INSTALLED_TREE=0

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
			ARCHIVE_NAME="agenthub-desktop_${VERSION}_linux_amd64.tar.gz"
			ARCHIVE_KIND="tgz"
		fi
		ARCHIVE="$TEMP_DIR/$ARCHIVE_NAME"
		SUMS="$TEMP_DIR/SHA256SUMS"
		say "downloading $ARCHIVE_NAME"
		if ! fetch "$DOWNLOAD_BASE/$VERSION/$ARCHIVE_NAME" "$ARCHIVE"; then
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
			webkit_hint
		fi
		;;
	esac

	path_hint

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
