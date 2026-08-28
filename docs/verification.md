# Verification

Verified on 2026-08-28.

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


Process enumeration is restricted inside the Codex execution sandbox, so the smoke run conservatively reported `unknown` for affected unmanaged sessions. The program treats missing process evidence as unknown by design.

## Required real-host acceptance

On one Windows and one Ubuntu host with the target providers installed:

1. Start `agenthub-node` with a temporary database.
2. Confirm discovery counts match provider session metadata on that host.
3. Confirm all sessions are private and heartbeat is empty.
4. Publish one disposable session and confirm only that session appears in heartbeat.
5. Confirm running/recent sessions transition through active, idle, and inactive as documented.
6. Confirm paths containing spaces and non-ASCII characters round-trip through `ah list/status`.
