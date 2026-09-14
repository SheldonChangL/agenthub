#!/usr/bin/env bash
# Put ah, agenthub-node and agenthub-mcp inside the thing that gets shipped.
#
# The desktop app does not install the service itself: it runs `ah service`
# (#108), and it finds ah at a fixed offset from its own executable — beside it,
# or in Contents/Resources. Nothing else. PATH used to be a last resort and is
# not any more, because a GUI that installs a launchd job must not run whatever
# binary named ah happened to come first on a stranger's PATH. That makes this
# script the only thing standing between a released .app and "ah was not found":
# whatever it fails to copy, the app cannot fall back to.
#
# agenthub-mcp is in that list for exactly the same reason: the MCP config
# dialog resolves it through the same search (desktop/mcpconfig.go), so leaving
# it out would reproduce the very message this script exists to prevent, one
# binary over.
#
# Called as a wails postBuildHook (see desktop/wails.json), so `wails build`
# alone produces a complete app with no second command to remember, and callable
# on its own so the release workflow (#64) can package without going through
# wails at all:
#
#     desktop/build/bundle-binaries.sh darwin/arm64 path/to/Contents/MacOS/desktop
#
# darwin/universal is accepted and is what wails passes for a universal build:
# each command is built for both architectures and merged with lipo, which is
# what wails does for the app executable itself after this hook has run.
#
# The destination is always the directory holding the app executable: on macOS
# that is <app>.app/Contents/MacOS, on Linux and Windows the directory the app
# is unpacked into. Both are "beside this executable", which is the first place
# the app looks.
#
# AGENTHUB_RELEASE, if set, is stamped into the binaries the way the release
# workflow stamps the standalone ones, so `ah --version` inside the bundle
# answers with the tag rather than a revision.
#
# Signing is ad-hoc by default, which is all a local build needs. Real signing
# (#66) sets AGENTHUB_CODESIGN_IDENTITY to a Developer ID and
# AGENTHUB_CODESIGN_FLAGS to the flags notarization requires, for example:
#
#     AGENTHUB_CODESIGN_IDENTITY="Developer ID Application: ..." \
#     AGENTHUB_CODESIGN_FLAGS="--options runtime --timestamp" \
#         desktop/build/bundle-binaries.sh darwin/universal path/to/.../desktop
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

# wails computes ${platform} as darwin/universal and hands it to this hook
# before doing its own lipo, so "universal" arrives here as a GOARCH go build
# has never heard of. Both halves get built and merged instead.
architectures=("$goarch")
if [ "$goarch" = "universal" ]; then
	if [ "$goos" != "darwin" ]; then
		echo "$0: universal is a darwin-only arrangement, got \"$platform\"" >&2
		exit 2
	fi
	if ! command -v lipo >/dev/null 2>&1; then
		echo "$0: darwin/universal needs lipo, which this machine does not have" >&2
		exit 2
	fi
	architectures=(amd64 arm64)
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

staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT

# Every command the app resolves through its own search: ah for the service
# panel, agenthub-node because the install now pins it by path, agenthub-mcp for
# the config dialog.
binaries=(ah agenthub-node agenthub-mcp)

echo "bundling ${binaries[*]} for ${goos}/${goarch} into ${destination}"
for binary in "${binaries[@]}"; do
	slices=()
	for architecture in "${architectures[@]}"; do
		slice="${staging}/${binary}.${architecture}"
		# -trimpath and CGO_ENABLED=0 to match how CI and the release workflow
		# build these same commands: what ships inside the app should not differ
		# from what ships beside it.
		(
			cd -- "$repository"
			GOOS="$goos" GOARCH="$architecture" CGO_ENABLED=0 \
				go build -trimpath -ldflags "$ldflags" \
				-o "$slice" "./cmd/${binary}"
		)
		slices+=("$slice")
	done
	if [ "${#slices[@]}" -eq 1 ]; then
		mv -- "${slices[0]}" "${destination}/${binary}${suffix}"
	else
		lipo -create -output "${destination}/${binary}${suffix}" "${slices[@]}"
	fi
done

for binary in "${binaries[@]}"; do
	ls -l "${destination}/${binary}${suffix}"
done

# A bundle's signature seals its contents, and wails signs the bundle before it
# runs this hook — so copying anything in afterwards invalidates the seal
# ("a sealed resource is missing or invalid"), and on Apple silicon an app whose
# signature does not verify is killed at launch rather than merely warned about.
# Sealing again here is not optional tidying; without it this script would ship
# a .app that does not start.
#
# That ordering is the whole of this script's bearing on real signing (#66):
# nested code is signed first, the bundle last, and anything that adds files to
# the bundle has to run before the bundle is sealed. A signing step added later
# goes after this script, not before it.
if [ "$goos" = "darwin" ] && [ "$(uname -s)" = "Darwin" ]; then
	identity=${AGENTHUB_CODESIGN_IDENTITY:--}
	sign_flags=()
	if [ -n "${AGENTHUB_CODESIGN_FLAGS:-}" ]; then
		# shellcheck disable=SC2206 # a flag list is meant to split on spaces
		sign_flags=(${AGENTHUB_CODESIGN_FLAGS})
	fi
	bundle=$destination
	case "$bundle" in
	*/Contents/MacOS) bundle=$(dirname -- "$(dirname -- "$bundle")") ;;
	*) bundle="" ;;
	esac
	for binary in "${binaries[@]}"; do
		codesign --force --sign "$identity" \
			${sign_flags[@]+"${sign_flags[@]}"} "${destination}/${binary}"
	done
	if [ -n "$bundle" ]; then
		echo "re-sealing ${bundle}"
		codesign --force --sign "$identity" \
			${sign_flags[@]+"${sign_flags[@]}"} "$bundle"
		# Checked, not assumed: a bundle that fails this is one that would fail
		# to launch, and finding that out here beats finding it out on the
		# machine of the colleague who installed it.
		codesign --verify --deep --strict "$bundle"
	else
		# Silence here is what "the packaging step did not run" looks like from
		# outside. A destination that is not Contents/MacOS is a bare directory,
		# not a bundle, so there is nothing to re-seal — but say so, because the
		# other reading is that sealing happened and passed.
		echo "$0: ${destination} is not an app bundle's Contents/MacOS;" \
			"nested binaries were signed but no bundle was re-sealed or verified" >&2
	fi
fi
