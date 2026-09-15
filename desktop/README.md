# AgentHub Desktop

AgentHub Desktop is the owner-facing privacy console for a local
`agenthub-node`. It lists every owner-local Claude and Codex session, supports
search and status/provider/audience filters, applies one audience and export
policy to multiple selected sessions, manages manually trusted nodes, opens and
closes pairing mode and shows who is advertising, reads what other nodes have
queued for a session, triggers a provider rescan, and shows the current signed
heartbeat preview.

The app is an HTTP client only. It does not read provider files or SQLite and
does not write a second copy of session state. It accepts loopback node URLs
only, because the owner's API has no authentication and stays on loopback for
that reason.

## Prerequisites

1. Start `agenthub-node` on its default `127.0.0.1:7462` address, or set a
   loopback `AGENTHUB_URL` before launching the desktop app.
2. Install Wails v2 for development builds.

## Window layout

Three views behind the title-bar tabs:

- **本機 session** — the table. Filters are three titled groups (provider,
  status, audience): chips within a group OR, groups AND, and each chip's count
  is what it would match with the other groups still applied. Column headers
  sort; the default is last activity, newest first. Filters, sort and search
  persist in `localStorage`. Selecting rows floats an action bar over the
  table. Each row has two actions: the inbox drawer (with 送出紀錄 and
  喚醒紀錄 tabs reading `/v1/outbound` and `/v1/wakes`) and `resume` (copies
  `claude --resume <id>` or `codex resume <id>`). The MCP config it used to
  offer is still reachable as `openMCPConfig`, with no entry point on the row:
  everything the four MCP tools do, `ah` does too.
- **區網** — paired nodes with presence and a red mark when a node has no
  recorded address; the pairing window and the advertising machines open as a
  drawer from the list's foot.
- **設定** — the background service panel, this node's identity (id,
  fingerprint, copyable public key) and appearance switches for the backdrop
  photo and the falling digits (which also stop under `prefers-reduced-motion`).

The functional contract the redesign was built against, including the copy the
tests assert verbatim, is `docs/ui-contract.md`; the design canvas sources are
under `docs/ui-redesign/`.

## Develop and build

```sh
go test ./...          # includes the frontend static checks and runs the node tests
wails dev
wails build
```

Frontend tests import `frontend/src/app.js` directly: `configure()` injects
fake bindings and `boot({ start: false })` wires the DOM without starting the
intervals. Run them with `npm test` in `frontend/`. To look at the window
without a node, `npx vite` in `frontend/` and open `/dev/mock.html`, which
boots the real markup and `app.js` against fake data.

The desktop app is a separate Go module so Wails and CGo do not affect the
cross-platform node or CLI builds.

`wails build` also runs `build/bundle-binaries.sh` as a post-build hook, which
builds `ah`, `agenthub-node` and `agenthub-mcp` for the same target (`.exe` on
Windows) and copies them in beside the app executable — `Contents/MacOS` in the
bundle, the unpacked directory on Linux and Windows — then seals the macOS
bundle again, since adding files to a signed bundle invalidates its signature.

Those copies are how the app finds them. The service panel runs `ah service`,
the MCP dialog names `agenthub-mcp`, and both use the same search: the
`AGENTHUB_AH` / `AGENTHUB_MCP` / `AGENTHUB_NODE` override, beside the
executable, `Contents/Resources`, then this checkout's `bin/` — never PATH,
because installing a launchd job or a systemd unit must not mean running
whatever binary of that name happened to come first there. The checkout
fallback is taken only from `desktop/build/bin` (or a `.app` under it): the
`go.mod` check alone would have accepted any world-writable directory an app
was dropped into. An override has to be a regular file, and on macOS and Linux
one with the execute bit; Windows decides by extension and has no mode bit to
check, so there a regular file is enough.

The install also passes `--node-binary` explicitly, so the `ExecStart` written
into the launchd job or systemd unit names the bundled `agenthub-node` rather
than whatever `ah` would have found on PATH.

`darwin/universal` is accepted and is what `wails` passes for a universal
build: each command is built for both architectures and merged with `lipo`.
Set `AGENTHUB_RELEASE` to stamp a tag into the bundled binaries. Signing is
ad-hoc by default; `AGENTHUB_CODESIGN_IDENTITY` and `AGENTHUB_CODESIGN_FLAGS`
(for example `--options runtime --timestamp`) are there for real signing (#66).
The script can also be called on its own with `<goos>/<goarch>` and the path to
the app executable.

## Current boundaries

- Audience choices are implemented and persist across discovery. They decide the
  per-peer export view, and the publisher delivers that view to each paired node
  that has a recorded, policy-permitted address. This app cannot record an
  address: that is `PUT /v1/nodes/{id}/address` on the node, outside the app, and
  a peer without one is silently skipped.
- Pairing is a manual trust operation: the owner copies the peer identity and
  compares the full fingerprint out of band. The `pair.*` wire messages have
  schemas but still no producer or consumer, so there is no automated exchange
  (#62, under Step 9 #63).
- The heartbeat dialog shows the actual signed, schema-validated envelope. It is
  the owner's union preview; the per-peer envelopes that actually go out are
  built by `BuildFor` and are never the same document.
- The Network view shows paired nodes with their presence: remote sessions they
  have authorised for this node, and online or offline from the last snapshot's
  expiry. An agent sees the same authorised sessions through `agenthub-mcp`
  (#56), though not the node list or presence. Provider wake-up is half done,
  and which half decides what an owner sees: Codex sessions are woken through
  the app-server's `thread/resume` + `turn/start` (#58, `internal/codexdriver`),
  gated by `-auto-wake` and the session's own flag. Claude Code has no working
  push — `agenthub-mcp -channel` writes a well-formed frame that never arrives,
  measured on real machines four times and recorded in
  `docs/channel-push-not-observed.md` (#57) — so for a Claude session a message
  waits in the inbox until something reads it (Step 8, #60).
- Provider metadata is rendered through DOM text APIs and covered by a hostile
  metadata regression test; see closed
  [issue #19](https://github.com/SheldonChangL/agenthub/issues/19).
