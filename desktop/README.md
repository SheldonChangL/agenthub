# AgentHub Desktop

AgentHub Desktop is the owner-facing privacy console for a local
`agenthub-node`. It lists every owner-local Claude and Codex session, supports
search and status/provider/visibility filters, applies publish or unpublish to
multiple selected sessions, triggers discovery, and shows the current export
preview.

The app is an HTTP client only. It does not read provider files or SQLite and
does not write a second copy of session state. It accepts loopback node URLs
only because the local API has no authentication yet.

## Prerequisites

1. Start `agenthub-node` on its default `127.0.0.1:7462` address, or set a
   loopback `AGENTHUB_URL` before launching the desktop app.
2. Install Wails v2 for development builds.

## Develop and build

```sh
go test ./...
wails dev
wails build
```

The desktop app is a separate Go module so Wails and CGo do not affect the
cross-platform node or CLI builds.

## Current boundaries

- Publishing changes the current local `private | public` export preview. The
  per-node audience picker is planned in
  [issue #5](https://github.com/SheldonChangL/agenthub/issues/5) and
  [ADR-001](../docs/decisions/001-session-audience-and-export-boundary.md).
- The heartbeat dialog shows the current runtime payload. It is not yet a
  validated broker envelope;
  [issue #18](https://github.com/SheldonChangL/agenthub/issues/18) tracks that
  alignment.
- LAN nodes, pairing, remote delivery, and provider wake-up are not implemented.
- Provider metadata is untrusted.
  [Issue #19](https://github.com/SheldonChangL/agenthub/issues/19) tracks
  replacing dynamic `innerHTML` rendering with safe DOM text insertion before
  distribution.
