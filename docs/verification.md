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
- `ah audience <id> selected node_laptop00000000 node_build000000000 --cwd` publishes to exactly
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

## Node identity and pairing

The node now has a signing identity. Verified on macOS against a running node:

- the fingerprint is stable across restarts and `node.key` is the 32-byte seed at
  mode 0600, which is the Unix storage form
- `GET /v1/node` publishes the public key and fingerprint and nothing else

A review of this work found two defects that tests could not see:

- Signatures were taken over a re-serialization of the Go value. A payload
  serializes in field order when sent and in key order once decoded, and a
  `uint64` returns as a `float64`, so every cross-process verification would
  have failed. The payload now travels as the bytes the sender produced and the
  signature covers a length-prefixed encoding of the envelope's fields. A test
  encodes, decodes and verifies, and asserts the payload bytes are unchanged;
  flipping a switch that re-encodes the payload in transit makes it fail.
- `ed25519.Verify` panics on a wrong-sized public key, and looking up an unknown
  node yields a zero value, so a stranger's envelope could have stopped the
  process. `Verify` checks the length first, covered for nil, empty, truncated
  and oversized keys.

Also hardened: node identifiers are constrained to printable ASCII of 16 to 128
characters, so `node_a`, `node_a ` and full-width or Cyrillic lookalikes cannot
become separate trust entries that read identically; the key file is refused if
others can read it and is written through a temporary file; the sender label on
a message is parsed rather than stored as free text.

Payload schemas exist for `node.heartbeat`, `node.hello` and the four `pair.*`
types. `agent.message` and `agent.ack` remain reserved names whose payloads are
unconstrained until #16 defines them.

Only `node.heartbeat` has a producer. The pairing types are defined and
schema-tested ahead of the transport that will carry them, so nothing in the
build emits one.

### Review of the identity work: three further findings

**Key storage assumed Unix permission semantics.** Owner-only protection rested
on 0600 and `Chmod`, which Go documents as not producing an owner-only ACL on
Windows: the mode was a claim the platform did not keep. Storage is now
platform-specific — the raw seed at 0600 on Unix, a DPAPI blob bound to the
current Windows user inside a versioned envelope on Windows — and the loader
fails closed on a corrupt file, an undecryptable blob, a blob that decrypts to
the wrong length, a symlink or reparse point, anything that is not a regular
file, and a file too large to be a key. It never replaces a key it could not
read. DPAPI comes from `golang.org/x/sys/windows`, already in this module's
dependency graph for other platform calls, so the change adds no new module.

Verified on macOS: the whole suite passes, `-race` is clean, and both binaries
cross-compile for darwin, linux and windows on amd64 and arm64. The versioned
envelope's framing is tested on every platform, including a case per malformed
shape. **The DPAPI calls themselves are unverified: cross-compilation proves
they compile, not that they behave. They need a run on real Windows.**

**Key creation could strand an empty `node.key`.** The old write claimed the
final name with `O_EXCL` and only then wrote the seed to a temporary file, so any
failure in between left a 0-byte `node.key` that every later start refuses, and
only a human deleting the file could recover the node. The content is now formed
in full, written to a temporary file and flushed before the final name exists at
all, then linked into place; linking is atomic and fails if the name exists, so
a losing concurrent start reads the winner's key instead of overwriting it. A
watcher test stats `node.key` throughout creation and fails if it is ever
observed empty — restoring the old ordering makes it fail, which is how the test
was checked. Sixteen concurrent starts on one directory agree on one
fingerprint and leave no temporary file behind.

**`BuildFor(peer)` accepted any non-empty identifier.** `Audience.PublishesTo`
returns true for an `all_paired` session and any non-empty string, so
`all_paired` meant "anyone who supplies a node id" rather than "every node this
owner paired with". `BuildFor` now refuses a recipient absent from
`trusted_nodes` with `ErrPeerNotTrusted`, covered for unknown, empty, trusted,
selected and revoked peers; revoking a node stops its heartbeats immediately
without the owner revisiting each session's audience. `Build` remains a union
because it is the owner's own preview, and a test pins that.

### Re-review of the same work: three further items

**The no-hard-link fallback still had the original window.** The first fix kept a
fallback that created the final name with `O_EXCL` and then wrote into it, so on a
filesystem without hard links a crash could still leave an empty or truncated
`node.key` — the invariant held only on the primary path. The fallback is gone.
Linking is now the only install route, and when it fails the install fails with
`ErrKeyStorageUnsupported` naming the fix: move the data directory off a
FAT/exFAT volume or network share. That is recoverable by an operator; a
half-written identity is not. A test injects a failing link and asserts the error
is `ErrKeyStorageUnsupported`, that `node.key` does not exist afterwards, and
that the directory is left with no entries at all — not even the temporary file.
A second test shows a later start on a working filesystem still creates a key.

**`readKeyFile` claimed more than it checked.** Its comment said the handle
re-check closed the `Lstat`/open race, but confirming that both observations are
regular files does not show they are the same file. It now compares them with
`os.SameFile` and refuses the read if the path changed. The comparison is
unit-tested against two files that differ only in identity — same directory, same
mode, same size — because forcing the interleaving inside `readKeyFile` would
need brittle timing. The comment now also states the real limit: the window is
small and reaching it needs write access to a directory that is mode 0700 and
owned by the same user the key protects, so this is defence in depth.

**Trailing whitespace in the PR diff.** `git diff --check origin/main...HEAD`
flagged four blank-but-tabbed lines in the generated
`desktop/frontend/wailsjs/go/models.ts`. They are now empty lines; the file is
TypeScript, so nothing about the generated output changes. The check passes.

## Heartbeat delivery contract

Two properties a future receiver would have had to work around are now producer
invariants. Neither adds a receiver or a transport: nothing in this build sends
or accepts a heartbeat.

**A heartbeat is bound to the node it was built for.** The envelope carries a
signed `recipientNodeId`, included in the length-prefixed signable bytes between
`nodeId` and the payload. Verified by test:

- a heartbeat built for node A fails verification at node B even though both
  trust the sender, and the error is `ErrNotAddressed` rather than a signature
  failure, because the sender really is who it claims to be
- substituting, removing, or expecting no recipient after signing all fail; the
  substitution case is what proves the signature covers the field, and removing
  the recipient from the signable bytes makes exactly that case pass again —
  measured, not assumed
- `VerifySender` refuses a directed envelope, so a receiver cannot accept a
  heartbeat through a call that only checks the signature; an undirected
  envelope still verifies that way
- authenticity is decided before the address, so only a cryptographically
  authentic envelope can be reported as built for another node. Checked at a
  node that is not the recipient: an intact heartbeat answers `ErrNotAddressed`,
  while a rewritten recipient, a removed or malformed signature, another node's
  key, an unusable key and a rewritten payload all answer `ErrUnsigned` first
- the local node ID is validated with the shared node-ID rule before it is
  accepted as a destination, so an unusable identity cannot match a deserialized
  envelope carrying the same unusable value
- `NewEnvelope` refuses `node.heartbeat`, and a directed envelope requires a
  recipient that passes the shared node-ID rule, so an undirected or
  unaddressable heartbeat cannot be constructed
- the owner preview is addressed to the local node: it remains the union of
  everything published anywhere, and `GET /v1/heartbeat` is asserted to name the
  local node, so a peer that obtained a copy would have to reject it
- the published schema requires `recipientNodeId` for `node.heartbeat` and
  constrains it to the node-ID pattern; the runtime envelope validates against
  it, and dropping the field or shortening the value fails validation
- the exact signable bytes are pinned for a directed and an undirected envelope,
  including the `0:` an absent recipient contributes

**The outbound sequence is persisted.** The in-memory counter was replaced by
one SQLite-backed counter owned by the registry, reserved by a single atomic
`UPDATE ... RETURNING` guarded at `MaxInt64`. Verified by test:

- a new builder over the same store, and a reopened registry, both continue
  upward; the pre-fix code returns 1 twice and the test names that
- 128 concurrent allocations produce 128 distinct non-zero values, and mixed
  owner-preview and per-peer builds never repeat one
- a database with no counter table upgrades, starts at 1, and publishes nothing
- opening a database that has the counter table but no counter row fails with
  `ErrSequenceUnavailable` and creates nothing: the regression test advances the
  counter, deletes the row, closes, reopens, and asserts both the error and that
  no row was re-created. Before this fix `Open` recreated the row at zero and
  the next heartbeat republished sequence 1 — a replay carrying this node's own
  signature. A counter table holding two rows, a non-numeric value, a negative
  value, or none of this store's columns is refused the same way, and an
  existing valid row is left exactly as found (41 stays 41, so the next
  allocation is 42)
- dropping the **whole** counter table while the node is stopped also fails
  closed. Table existence was the only evidence that the migration had ever run,
  so a deleted table read as "written before this build" and the upgrade path
  recreated it at zero — republishing sequence 1 under this node's identity and
  signature. The upgrade now writes a marker row in `schema_markers` in the same
  transaction as the counter, and the marker is the evidence. The regression
  test allocates three sequences, closes, drops the table, reopens, and asserts
  `ErrSequenceUnavailable`, that the table was not recreated, and that the
  marker survived; reverting to the table-existence check makes exactly that
  test pass again, which is how it was checked
- every disagreement between the two is refused rather than guessed: counter
  without marker table, marker table without this migration's row, a marker
  value from another version, a marker table whose columns this store did not
  write, and a valid marker whose counter table is gone. A genuine pre-counter
  database — neither marker nor table — still upgrades, starts at 1, and
  publishes nothing, and a new database gets both objects and exactly one marker
  row
- at `MaxInt64` allocation fails with `ErrSequenceExhausted`, repeatedly, and
  the stored value does not move: no wrap, no zero, no reuse
- a missing counter row answers `ErrSequenceUnavailable` and not
  `ErrSequenceExhausted`: they are different damage. Neither is repaired by
  writing a fresh counter, because that restarts at zero under the same node
  identity and republishes sequences a receiver may already hold; the safe
  recoveries are a backup whose high-water mark cannot roll back, or a new node
  identity with explicit re-pairing
- neither an exhausted nor a missing counter produces an envelope, from either
  build path
- the published schema requires `sequence` to be at least 1 — the lowest value
  the persisted allocator can hand out — and rejects 0 and negative values

Checked against a running node on macOS arm64, with an isolated database and
empty provider roots so no real session data was involved:

- `GET /v1/heartbeat` named the local node in `recipientNodeId`, equal to
  `nodeId`, and exported zero sessions
- two calls returned sequences 1 and 2; the node was stopped, restarted on the
  same database, and the next call returned 3 — the case an in-memory counter
  answers with 1
- with the node stopped, the counter row was deleted from that database and the
  node was started again: it refused to start with `outbound heartbeat sequence
  is unavailable: the counter row is missing, so the last published sequence is
  unknown`, and the counter table still held zero rows afterwards
- on a second database, two heartbeats were served (sequences 1 and 2), the node
  was stopped, and the entire `heartbeat_sequence` table was dropped with the
  `sqlite3` CLI. The node refused to start with `outbound heartbeat sequence is
  unavailable: the migration marker records a counter this database no longer
  has`, exited non-zero, left the table absent, and left the marker row
  `heartbeat_sequence|v1` in place

**What the marker cannot do.** It detects inconsistent loss — one half of the
evidence gone while the other contradicts it. It cannot detect the loss of all
of it. A database rolled back to a backup taken before any heartbeat, or deleted
and recreated together with `node.key`, presents nothing to contradict and is
indistinguishable from a first start, as it would be to any scheme that keeps
its high-water mark in the file it is protecting. Local storage authenticity is
not something this build can infer; ruling out a full rollback needs a monotonic
store outside the database or a node identity that rotates with it, and neither
is implemented or claimed here.

One consequence worth stating: a database created by an earlier commit of this
branch has the counter table and no marker, so this build refuses to open it.
Nothing has been released with the unmarked layout, so this affects only a
working copy taken mid-review, and the fix is to delete that scratch database.

Not verified: no Windows or Linux host ran this build; no second implementation
has reproduced the new signable bytes; and no receiver exists, so
`VerifyDirected` is exercised only by tests.

## Automated checks

```sh
go test ./...
go test -race ./...
go vet ./...
```

The suite covers provider metadata parsing, Codex App Server JSON-RPC/status
parsing, status inference, audience-none-by-default persistence, audience and
export-flag preservation, allowlisted/signed heartbeat output, per-peer export
filtering, recipient-bound heartbeats and their signable bytes, persisted
outbound sequence monotonicity and exhaustion, node identity and trust
invariants, message inbox policy, HTTP origin rejection, pagination, qualified
addressing, CLI calls, and loopback-only binding.

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

The desktop module has its own suite (`go test -race ./...` in `desktop/`)
covering batch audience writes with partial failures, full pagination, trusted
node management, hostile metadata rendering, unreachable-node handling, and
rejection of non-loopback node URLs.

Not verified: the app's visual rendering was not captured, because screen recording permission was unavailable to the shell used for this run.

## Toolchain security baseline

Every check in this document is run with a toolchain at or above the floor both
modules declare, currently **go 1.27.0**. That floor is a prerequisite, not a
formality: the node, CLI and desktop binaries link the toolchain's standard
library, so a green suite built by an unpatched toolchain still ships that
toolchain's `crypto/tls`, `crypto/x509` and `net/http` defects. A `go` directive
is enforced by the go command, so an older toolchain fails the build instead of
producing an artifact nobody re-scans.

Measured on 2026-08-28 while raising the floor from `go 1.25.0`:

- `govulncheck ./...` on the old floor with `go1.25.0 darwin/arm64`: exit 3,
  26 reachable standard-library vulnerabilities, highest reported fix 1.25.13
- `govulncheck ./...` on `go1.27.0 darwin/arm64`: exit 0, no vulnerabilities
- `go test ./...`, `go test -race ./...` and `go vet ./...` pass in both modules
- `go build ./...` and a compile-and-link pass over every test binary succeed
  for `linux/amd64` and `windows/amd64`
- `go mod tidy` under 1.27.0 leaves `go.mod` and `go.sum` byte-identical in both
  modules, so the floor moved without dependency churn

`internal/buildpolicy` holds no production code; its one test reads both
`go.mod` files and fails if either floor drops below 1.27.0.

CI does not yet pin the toolchain. That is tracked in
[issue #22](https://github.com/SheldonChangL/agenthub/issues/22); until it
lands, the floor is enforced by the go command on each contributor's machine.

## Required real-host acceptance

On one Windows and one Ubuntu host with the target providers installed:

1. Start `agenthub-node` with a temporary database.
2. Confirm discovery counts match provider session metadata on that host.
3. Confirm all sessions are private and heartbeat is empty.
4. Publish one disposable session and confirm only that session appears in heartbeat.
5. Confirm running/recent sessions transition through active, idle, and inactive as documented.
6. Confirm paths containing spaces and non-ASCII characters round-trip through `ah list/status`.
