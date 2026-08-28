# Verification

Verified on 2026-08-28.

## Planning and contract audit

The same-day architecture/docs/issue audit re-ran `go test ./...`, `go vet
./...`, and the desktop module tests successfully. It also compared runtime
types with the checked-in protocol drafts and recorded two gaps rather than
overstating current readiness:

- [#18](https://github.com/SheldonChangL/agenthub/issues/18): the local heartbeat
  preview serialized full `Session` values and did not match the broker
  `SessionSummary` schema. Resolved; see below.
- [#19](https://github.com/SheldonChangL/agenthub/issues/19): the desktop table
  rendered untrusted provider metadata through `innerHTML`. Resolved; the
  renderer now uses DOM text APIs and a headless render check feeds hostile
  metadata through it.

## Export contract conformance

The heartbeat now emits the broker envelope defined in
`broker-protocol.schema.json`, with each public session projected into
`protocol.SessionSummary`.

Checked with a running node against real provider data:

- the served envelope validates against the checked-in schema using an
  independent JSON Schema implementation, not only the Go test's validator
- the previous flat shape fails that same schema on five counts, so the check
  is not vacuous
- session addresses are qualified as `<node-id>/<provider>:<id>`
- `providerSessionId`, `source`, `updatedAt`, and `metadataPath` are absent from
  the served bytes, and `additionalProperties: false` rejects them if reintroduced

Go tests cover the builder output, the HTTP response, and five negative schema
cases: an owner-local field on a summary, a private session in the export view,
an unqualified address, a heartbeat payload under the wrong envelope type, and
an unknown envelope field.

### Findings from the export boundary audit

A separate review of that change found the projection was **fail-open** and one
test was a false positive. Both are fixed, and the fixes are what the tests now
assert:

- `protocol.Summarize` wrote `visibility` as the constant `"public"` rather than
  copying the owner's decision. A private session passed to it produced a
  summary stamped public that satisfied the schema and every test in the
  package, leaving the SQL filter in a single caller as the only real defence.
  It now copies the value and refuses any session that is not published, and
  `TestSummarizeRefusesUnpublishedSessions` fails against the old behavior.
- The schema cases that mutate hand-built documents test the schema, not the
  projection, and could never have caught the above. They are labelled as such,
  and the projection has its own direct tests.
- A provider session ID is untrusted metadata, not a filename, so one
  containing `/` would split the qualified address `<node-id>/<provider>:<id>`
  in an unintended place. `validateSession` now rejects it. Node impersonation
  was never possible — `SplitQualifiedID` cuts at the first separator — and a
  test pins that behavior.
- `govulncheck` reports no vulnerabilities, including the `x/text` version the
  new schema validator pulls in. The validator is test-only and absent from
  `go list -deps ./cmd/agenthub-node`.

A second review of those fixes found three regressions they introduced, all now
resolved and covered by tests:

- Rejecting a bad provider session ID aborted the whole scan, so a single file
  under `~/.claude/projects` could disable discovery. Discovery now skips the
  record, counts it in `skipped`, and completes. Measured both ways: with the
  parser check relaxed the hostile record reaches the registry, is refused, and
  the other sessions still register.
- The heartbeat error naming a session and its visibility was returned in the
  HTTP body. The endpoint now returns a generic message and logs the detail.
- The registry rejects a separator on write, but a database written by an older
  build would not have been checked. The export projection now refuses such a
  row itself, and the new-database schema carries a `CHECK` constraint.

Two further reviews of those fixes found more, all resolved:

- Discovery skipped every error alike, so a cancelled context or an unavailable
  database reported a successful scan of zero sessions. Only `ErrInvalidSession`
  is skipped now; anything else fails the scan.
- Two tests asserted on paths they never reached. The discovery test's hostile
  record was rejected by the metadata parser before the store saw it, and the
  error-hygiene test hit a 501 rather than the error path. Both are rewritten
  against injected failures, and each was mutation-tested: reverting the fix
  makes them fail.
- `POST /v1/messages` returned raw store errors as 400, including
  `sql: database is closed` and the destination session ID. It classifies by
  sentinel now, as the other endpoints do.
- `writeRegistryError` treated any error containing "invalid" as the caller's
  fault, which matched driver errors and echoed the column and stored value.
  Classification is by sentinel only, and a test fails if the substring check
  returns.

## Audience model

Publishing now answers "to whom". Verified against a node holding real provider
data:

- every discovered session upgrades and registers at audience `none`
- `ah audience <id> selected node_laptop node_build --cwd` publishes to exactly
  those nodes; `ah list` shows `2 nodes`, and `all-paired` shows `all paired`
- the heartbeat carried only the two published sessions out of 1,040, still
  validated against the broker schema by an independent implementation, and
  dropped to one after `ah audience <id> none`
- a `selected` policy with no nodes publishes to nobody: the SQL predicate and
  `Audience.PublishesToAnyone` agree that an empty selection is not published

Both per-session flags default closed and are enforced where they matter rather
than only stored:

- `export_cwd` is read by the export projection, so the working directory is
  absent from a summary until the owner opts in
- `accept_messages` is read by `CreateMessage`, so a session that has not opted
  in accumulates no queue

A review of this change found two gaps that mattered only once a transport
exists, both closed here:

- `selected` was not enforced per recipient. The builder produced one envelope
  for everyone, so a session published to one node would have reached every
  peer. `BuildFor(peer)` now filters by the grant list, and `Build` is
  documented as the owner's preview rather than anything a peer receives.
- Publishing through the compatibility path turned on the working-directory
  export as a side effect, so `ah publish` shared the account and project name
  without asking. It leaves both flags closed; a test fails if that returns.

The stale `visibility` column is gone. It was still read by one count query
after everything else moved to `audience_mode`, which would have reported the
pre-upgrade public count — the value ADR-001 requires be discarded.

ADR-001's migration rule is covered by a test that builds a database with the
pre-audience schema, inserts a row marked `public`, opens it with the current
build, and requires audience `none` and an empty export view.

Payload schemas exist for `node.heartbeat` only. `node.hello`, `agent.message`
and `agent.ack` are reserved names whose payloads are unconstrained until issues
#11, #12 and #16 define them; nothing in the build emits them.

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
