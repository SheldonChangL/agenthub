# Verification

Verified on 2026-08-28.

## Planning and contract audit

The same-day architecture/docs/issue audit re-ran `go test ./...`, `go vet
./...`, and the desktop module tests successfully. It also compared runtime
types with the checked-in protocol drafts and recorded two gaps rather than
overstating current readiness:

- [#18](https://github.com/SheldonChangL/agenthub/issues/18): the current local
  heartbeat preview serializes full `Session` values and does not yet match the
  broker `SessionSummary` schema.
- [#19](https://github.com/SheldonChangL/agenthub/issues/19): the desktop table
  must render untrusted provider metadata with DOM text APIs before a desktop
  distribution or LAN release.

Neither finding changes the verified local privacy behavior: private sessions
are absent from the current preview, and the node still rejects non-loopback
bind addresses. They are release gates for connecting that preview to a broker
or distributing the desktop app.

## Automated checks

```sh
go test ./...
go test -race ./...
go vet ./...
```

The suite covers provider metadata parsing, Codex App Server JSON-RPC/status parsing, status inference, private-by-default persistence, visibility preservation across rediscovery, public-only heartbeat output, node identity stability, message inbox behavior, HTTP origin rejection, pagination, CLI calls, and loopback-only binding.

## Build matrix

| Target | Compile | Runtime smoke |
|---|---:|---:|
| macOS arm64 | pass | pass |
| macOS amd64 | pass | pending real Intel host |
| Ubuntu/Linux amd64 | pass | pending real host |
| Ubuntu/Linux arm64 | pass | pending real host |
| Windows amd64 | pass | pending real host |
| Windows arm64 | pass | pending real host |

Cross-compilation verifies AgentHub source and dependency portability. It does not prove that a specific Claude/Codex version is installed or stores metadata identically on each host.

## macOS real-data smoke

An isolated AgentHub database scanned the installed providers without modifying their data:

- 255 Claude sessions discovered
- 783 Codex sessions discovered
- 1,038 total sessions registered
- every session remained `private`
- heartbeat exported zero sessions before any explicit publish action
- `ah node`, `ah discover`, `ah list`, and `ah heartbeat` succeeded
- paginated `ah list` returned all 1,038 rows plus its header
- a foreign browser Origin received HTTP 403
- Go vulnerability scanning reported no known reachable vulnerabilities


Process enumeration is restricted inside the Codex execution sandbox, so that smoke run conservatively reported `unknown` for affected unmanaged sessions. The program treats missing process evidence as unknown by design.

## macOS desktop app run

A second macOS run on the same day exercised the desktop app against a live node, outside the Codex sandbox, so process evidence was available:

- 1,040 sessions discovered (256 Claude, 784 Codex)
- status inference produced 2 `active`, 3 `idle`, 1,035 `inactive`
- the `active` rows correlated with the provider sessions actually running at that moment, each reporting `statusSource: metadata_process_heuristic`
- heartbeat exported zero sessions until an explicit publish, exported exactly one after it, and returned to zero after unpublish
- `ah send` queued a message and `ah inbox` read it back
- `wails build` produced `agenthub-desktop.app` for darwin/arm64 and the app launched against the running node

Adding the desktop app did not disturb the node or CLI: `go.mod` and `go.sum` of the root module were unchanged, and `CGO_ENABLED=0` cross-compilation of `agenthub-node` and `ah` still passed for all six targets in the matrix above.

The desktop module has its own suite (`go test -race ./...` in `desktop/`) covering batch visibility writes with partial failures, full pagination, unreachable-node handling, and rejection of non-loopback node URLs.

Not verified: the app's visual rendering was not captured, because screen recording permission was unavailable to the shell used for this run.

## Required real-host acceptance

On one Windows and one Ubuntu host with the target providers installed:

1. Start `agenthub-node` with a temporary database.
2. Confirm discovery counts match provider session metadata on that host.
3. Confirm all sessions are private and heartbeat is empty.
4. Publish one disposable session and confirm only that session appears in heartbeat.
5. Confirm running/recent sessions transition through active, idle, and inactive as documented.
6. Confirm paths containing spaces and non-ASCII characters round-trip through `ah list/status`.
