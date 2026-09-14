#!/usr/bin/env bash
# Put ah and agenthub-node inside the thing that gets shipped.
#
# The desktop app does not install the service itself: it runs `ah service`
# (#108), and it finds ah at a fixed offset from its own executable — beside it,
# or in Contents/Resources. Nothing else. PATH used to be a last resort and is
# not any more, because a GUI that installs a launchd job must not run whatever
# binary named ah happened to come first on a stranger's PATH. That makes this
# script the only thing standing between a released .app and "ah was not found":
# whatever it fails to copy, the app cannot fall back to.
#
# Called as a wails postBuildHook (see desktop/wails.json), so `wails build`
# alone produces a complete app with no second command to remember, and callable
# on its own so the release workflow (#64) can package without going through
# wails at all:
#
#     desktop/build/bundle-binaries.sh darwin/arm64 path/to/Contents/MacOS/desktop
#
# The destination is always the directory holding the app executable: on macOS
# that is <app>.app/Contents/MacOS, on Linux and Windows the directory the app
# is unpacked into. Both are "beside this executable", which is the first place
# the app looks.
#
# AGENTHUB_RELEASE, if set, is stamped into the binaries the way the release
# workflow stamps the standalone ones, so `ah --version` inside the bundle
# answers with the tag rather than a revision.
set -euo pipefail

if [ "$#" -ne 2 ]; then
	echo "usage: $0 <goos/goarch> <path to the built app executable>" >&2
	exit 2
fi

platform=$1
app_binary=$2

goos=${platform%%/*}
goarch=${platform##*/}
if [ -z "$goos" ] || [ -z "$goarch" ] || [ "$goos" = "$platform" ]; then
	echo "$0: expected <goos>/<goarch>, got \"$platform\"" >&2
	exit 2
fi

# wails runs hooks from desktop/build/bin, the release workflow from wherever it
# likes; the repository is found from this script's own location either way.
script_directory=$(cd -- "$(dirname -- "$0")" && pwd)
repository=$(cd -- "$script_directory/../.." && pwd)

destination=$(cd -- "$(dirname -- "$app_binary")" && pwd)

suffix=""
if [ "$goos" = "windows" ]; then suffix=".exe"; fi

ldflags=""
if [ -n "${AGENTHUB_RELEASE:-}" ]; then
	ldflags="-X agenthub.local/agenthub/internal/buildinfo.release=${AGENTHUB_RELEASE}"
fi

echo "bundling ah and agenthub-node for ${goos}/${goarch} into ${destination}"
for command in ah agenthub-node; do
	# -trimpath and CGO_ENABLED=0 to match how CI and the release workflow build
	# these same two commands: what ships inside the app should not differ from
	# what ships beside it.
	(
		cd -- "$repository"
		GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
			go build -trimpath -ldflags "$ldflags" \
			-o "${destination}/${command}${suffix}" "./cmd/${command}"
	)
done

ls -l "${destination}/ah${suffix}" "${destination}/agenthub-node${suffix}"

# macOS refuses to run an unsigned executable on Apple silicon, and the Go
# linker's own ad-hoc signature is lost by nothing here — but a bundle-level
# signature would be, so this is where the ordering matters for #66: sign the
# nested binaries first and seal the bundle after this script has run, never
# before. Verified here so a broken nested signature is a build failure rather
# than a launch failure on somebody else's machine.
if [ "$goos" = "darwin" ] && [ "$(uname -s)" = "Darwin" ]; then
	for command in ah agenthub-node; do
		codesign --verify "${destination}/${command}" ||
			codesign --force --sign - "${destination}/${command}"
	done
fi
