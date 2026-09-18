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
# POSIX sh only: this runs under macOS /bin/sh (a bash in POSIX mode) and under
# dash on Debian and Ubuntu, so no arrays, no [[, no local, no +=.
set -eu

REPO="SheldonChangL/agenthub"
API_LATEST="https://api.github.com/repos/${REPO}/releases/latest"
DOWNLOAD_BASE="https://github.com/${REPO}/releases/download"

VERSION="${AGENTHUB_VERSION:-}"
CLI_ONLY=0
NO_SERVICE=0
NO_OPEN=0
DRY_RUN=0
PREFIX=""
FROM=""

TEMP_DIR=""
MOUNT_POINT=""

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

It never uses sudo. Read it before you run it: curl -fsSL <url> | less
EOF
}

say() {
	echo "$*"
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
	if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
		rm -rf "$TEMP_DIR"
	fi
	exit "$cleanup_status"
}

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

# --- what machine is this -----------------------------------------------------

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
# amd64 tarball only. A linux arm64 machine gets the command line archive, and
# is told so rather than left wondering where the window is.
if [ "$OS_SLUG" = "linux" ] && [ "$ARCH_SLUG" = "arm64" ] && [ "$CLI_ONLY" -eq 0 ]; then
	say "no desktop build exists for linux arm64; installing the command line tools only"
	CLI_ONLY=1
fi

# --- where things go ----------------------------------------------------------

if [ -n "$PREFIX" ]; then
	APP_PARENT="$PREFIX"
	BIN_DIR="$PREFIX/bin"
	AGENTHUB_DIR="${AGENTHUB_HOME:-$PREFIX/share/agenthub}"
else
	BIN_DIR="$HOME/.local/bin"
	AGENTHUB_DIR="${AGENTHUB_HOME:-$HOME/.local/share/agenthub}"
	# /Applications when this account can write it, ~/Applications otherwise.
	# Never sudo: an installer that asks for the administrator password to put a
	# per-user background service in place is asking for more than it needs.
	if [ -w /Applications ]; then
		APP_PARENT="/Applications"
	else
		APP_PARENT="$HOME/Applications"
	fi
fi
APP_PATH="$APP_PARENT/agenthub-desktop.app"

# --- the tools this needs -----------------------------------------------------

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

fetch() { # fetch <url> <destination>
	if [ -n "$CURL" ]; then
		run "$CURL" -fsSL -o "$2" "$1"
	elif [ -n "$WGET" ]; then
		run "$WGET" -q -O "$2" "$1"
	else
		die "neither curl nor wget is on PATH; install one, or download the release by hand"
	fi
}

# --- which release ------------------------------------------------------------

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

# --- get the archive ----------------------------------------------------------

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
		say "no SHA256SUMS beside $ARCHIVE_NAME; installing it unverified because you named the file yourself"
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

run mkdir -p "$BIN_DIR"

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

# --- install ------------------------------------------------------------------

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

install_macos_app() {
	MOUNT_POINT="$TEMP_DIR/mnt"
	run mkdir -p "$MOUNT_POINT"
	run hdiutil attach -nobrowse -readonly -quiet -mountpoint "$MOUNT_POINT" "$ARCHIVE"
	run mkdir -p "$APP_PARENT"
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

case "$OS_SLUG" in
darwin)
	if [ "$ARCHIVE_KIND" = "dmg" ]; then
		install_macos_app
	else
		install_tarball_tree
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
	if [ "$CLI_ONLY" -eq 0 ]; then
		webkit_hint
	fi
	;;
esac

path_hint

# --- the background service ---------------------------------------------------

# `ah service install` writes the unit that starts the node at login, and it
# writes the absolute path of the agenthub-node beside this ah. Running it on
# every upgrade — rather than only the first time — is what keeps an owner who
# moved from ~/Applications to /Applications from being left with a unit that
# points at a path this script just deleted.
if [ "$NO_SERVICE" -eq 1 ]; then
	say "skipped the background service (--no-service); \"$AH\" service install registers it"
else
	say "registering the background node"
	run "$AH" service install
	run "$AH" service status
fi

if [ "$OS_SLUG" = "darwin" ] && [ "$CLI_ONLY" -eq 0 ] && [ "$ARCHIVE_KIND" = "dmg" ] && [ "$NO_OPEN" -eq 0 ]; then
	run open "$APP_PATH"
fi

if [ -n "$VERSION" ]; then
	say "done. AgentHub $VERSION is installed; \"$AH --version\" says which build."
else
	say "done. \"$AH --version\" says which build is installed."
fi
