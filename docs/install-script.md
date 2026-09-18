# install.sh

The one-line install for macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/SheldonChangL/agenthub/main/install.sh | sh
```

It downloads one release asset, **verifies it against that release's own
`SHA256SUMS` before anything is unpacked**, puts it somewhere this account can
already write, and registers the background node with launchd or
`systemd --user`. It never runs `sudo` and never asks for a password.

Piping a script from the internet into a shell is a thing worth being uneasy
about. Read it first:

```sh
curl -fsSL https://raw.githubusercontent.com/SheldonChangL/agenthub/main/install.sh | less
```

`--dry-run` is the other half of that: it prints every command it would run, in
order, and runs none of them.

## Flags

| Flag | What it does |
|---|---|
| `--version vX.Y.Z` | Install this release instead of the latest. `AGENTHUB_VERSION` in the environment does the same, which is how a piped invocation passes it. |
| `--cli-only` | Install `ah`, `agenthub-node` and `agenthub-mcp` without the desktop app, from `agenthub_<tag>_<os>_<arch>.tar.gz`. |
| `--no-service` | Do not register the background node. Nothing starts at login; `ah service install` does it later. |
| `--no-open` | Do not open the app at the end (macOS). |
| `--prefix DIR` | Install under `DIR` instead of `/Applications` and `~/.local`. Symlinks then go to `DIR/bin`, and nothing outside `DIR` is written. Mostly a test hook. |
| `--from FILE` | Install from a local `.dmg` or `.tar.gz` instead of downloading. A `SHA256SUMS` beside the file is still checked; if there is none, the script says so and continues, because you named the file yourself. |
| `--dry-run` | Print every command instead of running it. |
| `-h`, `--help` | The flag list. |

`AGENTHUB_HOME` overrides where the Linux and `--cli-only` trees are unpacked.

## What it picks

| Machine | Asset |
|---|---|
| macOS, Apple silicon or Intel | `agenthub-desktop_<tag>_darwin_universal.dmg` |
| Linux x86\_64 | `agenthub-desktop_<tag>_linux_amd64.tar.gz` |
| Linux arm64 | `agenthub_<tag>_linux_arm64.tar.gz` — there is no arm64 desktop build, and the script says so before it falls back |
| any, with `--cli-only` | `agenthub_<tag>_<os>_<arch>.tar.gz` |

Without `--version` the tag comes from
`https://api.github.com/repos/SheldonChangL/agenthub/releases/latest`, read with
`sed`; there is no `jq` dependency. A release that has no desktop asset for this
platform stops the install with a message naming `--cli-only`, rather than
installing half of something.

## Exactly what it writes

**macOS**

| Path | What |
|---|---|
| `/Applications/agenthub-desktop.app` | The app, with `ah`, `agenthub-node` and `agenthub-mcp` inside `Contents/MacOS` beside the `desktop` executable. `~/Applications/agenthub-desktop.app` instead when `/Applications` is not writable by this account — never `sudo`. |
| `~/.local/bin/ah` | A symlink to the `ah` inside the app. The directory is created if it does not exist, and the script prints a hint if it is not on `PATH`. |
| `~/Library/LaunchAgents/local.agenthub.node.plist` | Written by `ah service install`, not by this script. |
| `~/Library/Application Support/agenthub/` | The node's own database, created by the node on first start. |

**Linux**

| Path | What |
|---|---|
| `~/.local/share/agenthub/` | The unpacked app directory: `agenthub-desktop`, `ah`, `agenthub-node`, `agenthub-mcp`, `LICENSE`, `README.txt`. `AGENTHUB_HOME` moves it. |
| `~/.local/bin/ah`, `~/.local/bin/agenthub-desktop` | Symlinks into that directory. With `--cli-only`, `ah`, `agenthub-node` and `agenthub-mcp` instead. |
| `~/.config/systemd/user/agenthub-node.service` | Written by `ah service install`, not by this script. |
| `~/.config/agenthub/` | The node's own database, created by the node on first start. |

Plus a temporary directory under `$TMPDIR`, removed on exit by a trap, including
when the install fails or is interrupted. Nothing else is touched. In particular
nothing is written to `/usr/local`, `/opt`, `/etc`, or any systemd path outside
this user's own.

## The quarantine flag on macOS

After the app is in place the script runs
`xattr -dr com.apple.quarantine` on it.

That is deliberate, and it is not a way of getting around a security check. The
app is not notarized (issue #66), so the first launch would otherwise be the
"Apple could not verify" dialog and a detour through System Settings → Privacy &
Security. What that dialog is asking is whether the file is really what its
publisher built — and this script has already answered that question, with a
stronger check than the one being skipped: the release's own `SHA256SUMS`,
compared before a single byte was unpacked. A download whose digest did not
match never reaches this line, because the script exits first.

## Upgrades

Re-running the script upgrades in place. The app directory or tree is unlinked
and replaced rather than written into, so a running node keeps the copy it
already has open, and Linux does not fail with "text file busy".

`ah service install` runs again on every upgrade, not only the first time. The
unit records the **absolute path** of the `agenthub-node` beside the `ah` that
installed it, so an owner who moves from `~/Applications` to `/Applications` —
or from one `AGENTHUB_HOME` to another — would otherwise be left with a unit
pointing at a path the upgrade just deleted. On Linux the running node is
stopped before its directory is replaced, and the reinstall starts it again.

## Linux runtime

The Linux app links the system WebKit at run time. If
`libwebkit2gtk-4.1.so.0` is not on the library path the script warns and prints
the package for this distribution — `apt`, `dnf` or `pacman`, read from
`/etc/os-release`. The install still completes; `ah` and the node do not need
GTK, only the window does.

## Tests

`scripts/test-install.sh` runs in CI alongside `shellcheck -s sh install.sh` and
`sh -n install.sh`. It fakes `uname` through a `PATH` shim so both platforms'
`--dry-run` transcripts are checked on one runner, asserts that the right asset
is named and that the digest is compared before the archive is opened, that
`--prefix` keeps every path inside the prefix, and that no command the script
would run is a `sudo`. It then installs twice for real from a locally built
archive — once with a matching `SHA256SUMS` and once with a corrupted one — and
requires the corrupted one to be refused with nothing created.
