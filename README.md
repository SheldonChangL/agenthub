# AgentHub

AgentHub is a privacy-first, local control plane for coding-agent sessions. The MVP discovers Claude Code and Codex CLI/App sessions, normalizes their status, and exposes an owner-local view through an `agenthub-node` daemon, the `ah` CLI, and a desktop app.

AgentHub targets Windows, macOS, and Ubuntu. The shared core is pure Go; provider process discovery is platform-specific. Cross-compilation is part of verification, while each provider must still be installed and supported on the target host.

Privacy is the default: discovered sessions start with audience `none`, and the peer listener stays on loopback unless the owner passes `-allow-lan` and names a private address. With that set, paired nodes exchange signed heartbeats and messages over TLS pinned to the key recorded at pairing, carrying only what each session's audience authorizes for that peer.

## MVP status

- Local SQLite session registry
- Claude Code and Codex filesystem discovery
- Codex App Server JSON-RPC initialize/thread-list client boundary
- Managed and unmanaged session model
- Conservative `active`, `idle`, `inactive`, and `unknown` status inference
- Persistent Ed25519 node identity, signed envelopes, and a schema-validated heartbeat preview
- Heartbeats bound to their recipient and a heartbeat sequence that survives restarts
- Local HTTP API and `ah` CLI
- Message inbox, bounded and deduplicated, reachable from paired nodes
- Per-session audience, working-directory export, and inbound-message policy
- Manual fingerprint pairing, trust storage, revocation, and desktop management
- Broker envelope schema and MCP tool schemas, both in use
- Architecture and issue plan for authenticated multi-node operation
- No wake-up: an agent reads its inbox when asked, and nothing hands it a message (Step 8, issue #60)
- Nothing writes into a provider's session files or process, by design
- Pairing still needs the peer's public key by hand, though a node can now announce itself for a while and see who else is announcing (Step 9, issues #61 and #62)
- No release or installer: installing means building from source, though CI now
  uploads a build of every binary for six platforms (Step 10, issues #64 and #67)

The remote export contract, per-node audience model, signing identity, manual
trust workflow, and the authenticated peer transport between nodes are all
implemented and have been exercised between two machines
([verification.md](docs/verification.md)), and `agenthub-mcp` gives an agent four
tools over those pipes — also exercised between two machines, each running its
own Claude Code. What is missing is wake-up, so a message waits until someone
asks their agent to look, and everything needed for someone else to install
this. Those are Steps 8 to 10, tracked from
[issue #1](https://github.com/SheldonChangL/agenthub/issues/1).

## Roadmap and release gates

| Track | State | Source of truth |
|---|---|---|
| Local MVP | Implemented and tested | [spec](docs/spec.md), [verification](docs/verification.md) |
| Remote export contract | Implemented and schema-validated | [architecture](docs/architecture.md), [broker protocol](docs/broker-protocol.schema.json) |
| Per-node privacy and network exchange | Implemented and exercised between two hosts | [issue #1](https://github.com/SheldonChangL/agenthub/issues/1), [verification](docs/verification.md) |
| MCP server: four tools an agent calls | Implemented and exercised between two hosts | [issue #56](https://github.com/SheldonChangL/agenthub/issues/56), [verification](docs/verification.md) |
| Automated pairing, wake-up, distribution | Planned | issues [#60](https://github.com/SheldonChangL/agenthub/issues/60), [#63](https://github.com/SheldonChangL/agenthub/issues/63), [#67](https://github.com/SheldonChangL/agenthub/issues/67) |
| Desktop metadata rendering hardening | Implemented and regression-tested | [issue #19](https://github.com/SheldonChangL/agenthub/issues/19) |
| Writing into a provider's files or process | Never, by design | [ADR-002](docs/decisions/002-mcp-surface-trust-boundary.md), [architecture](docs/architecture.md) |

## Build and test

Both modules require **Go 1.27.0 or newer**, declared in `go.mod` and
`desktop/go.mod`. The floor is a security requirement, not a language-feature
one: these binaries link the toolchain's standard library, so an unpatched
toolchain ships its `crypto/tls`, `crypto/x509` and `net/http` vulnerabilities
into the built artifact regardless of how the tests do. The `go` directive makes
the go command refuse an older toolchain rather than build quietly against it.

```sh
go version   # must report go1.27.0 or newer
go test ./...
mkdir -p bin
go build -o bin/agenthub-node ./cmd/agenthub-node
go build -o bin/ah ./cmd/ah
go build -o bin/agenthub-mcp ./cmd/agenthub-mcp
bin/ah --version   # which commit these binaries came from
```

Every binary reports the revision it was built from — `ah --version`, the node's
first log line, and the version `agenthub-mcp` sends at initialize. A build from
a modified tree says so, because a revision that does not describe the source
points at code nobody ran.

CI builds all three for six platforms and attaches them to each run, with a
`SHA256SUMS` and a `BUILD` file naming the run and the revision. The artifact
zip records the executable bit, and `gh run download`, `unzip` and Archive
Utility all keep it. `actions/download-artifact` does not — it extracts without
applying modes — so a workflow that consumes these has to `chmod +x` them. On a
pull request the revision inside the binary is the ephemeral merge commit
GitHub built, not a commit on the branch; `BUILD` records both.

## Run locally

```sh
mkdir -p data
go run ./cmd/agenthub-node --db ./data/agenthub.db
```

In another terminal:

```sh
go run ./cmd/ah discover
go run ./cmd/ah list
go run ./cmd/ah status <session-id>
go run ./cmd/ah publish <session-id>
go run ./cmd/ah unpublish <session-id>
go run ./cmd/ah audience <session-id>
go run ./cmd/ah audience <session-id> all-paired --cwd
go run ./cmd/ah audience <session-id> selected node_laptop00000000 node_build000000000 --cwd --messages
go run ./cmd/ah nodes
go run ./cmd/ah pair <node-id> <display-name> <platform> <public-key> <fingerprint>

# Pairing alone does not make delivery happen: a peer with no recorded address
# is skipped, silently, while `ah send` still answers `queued`. With --discover
# the address is learned from the peer's own announcements and this is
# unnecessary — see "Two machines" below. Without it, record it by hand:
go run ./cmd/ah nodes address <node-id> 192.168.1.20:7463

go run ./cmd/ah revoke <node-id>
go run ./cmd/ah send <session-id> "please review the schema"
go run ./cmd/ah inbox <session-id>
```

`selected` accepts only node IDs that already appear in `ah nodes`; pairing an
unknown node is a separate, explicit owner action.

The node listens on `127.0.0.1:7462` by default. Set `AGENTHUB_URL` for the CLI or pass `--url`.

The owner's API remains loopback-only and stays there; peer traffic uses a
separate TLS listener on `127.0.0.1:7463` by default. Trust records are created
by hand, and the receiving side authenticates every envelope against them:
signature and recipient binding on all of them, plus expiry and a strictly
advancing sequence on heartbeats, and message-id deduplication on messages.
Session list responses are paginated; `ah list` follows every page automatically.

## Run the node as a background service

A node started from a terminal lives as long as that terminal — or as long as
the agent session that started it. Messages sent to this machine while the node
is down are not delivered, and the sender's `ah send` still says `queued`. So
the node should belong to the operating system, not to a window:

```sh
bin/ah settings set --peer-listen 192.168.1.10:7463 --allow-lan=true --discover=true
bin/ah service install --db ./data/agenthub.db
```

The settings come first and the service carries only `--db`, because the node
remembers them (see [What this node remembers](#what-this-node-remembers)). A
unit file full of flags was a second copy of the configuration: changing one
switch meant reinstalling the service, and the desktop app could not change it
at all.

`install` registers the node with launchd (macOS) or `systemd --user` (Linux):
it starts at login and is restarted if it exits. `agenthub-node` is looked for
beside `ah`, then on `PATH`; pass `--node-binary` to name it. A relative `--db`
is made absolute, because a service has no working directory of yours. The
command ends by asking the node whether it answers, and says so either way.

It still accepts `--peer-listen`, `--allow-lan`, `--discover`,
`--treat-as-private` and `--auto-wake`, and still writes them into the unit,
because installations made that way are out there. It says so when you use
them: a flag in the unit is given on every start and therefore overrides
anything saved later, which is how a setting changed in the app appears to do
nothing. `--display-name` is the one flag it has never taken: the node
remembers a chosen name, and a flag on every start would pin the installed name
over any later rename.

```sh
bin/ah service status      # installed? running? is the node answering?
bin/ah service restart     # apply a saved setting: launchctl kickstart -k / systemctl --user restart
bin/ah service uninstall   # stop it and remove the registration
```

Uninstall removes the service and nothing else: `node.key` and the database
stay where they are, so installing again brings the same node back and every
pairing holds. To change a setting, `ah settings set ...` then `ah service
restart`; `install` is needed again only for a new binary path or database.

On Linux a user service starts when you log in. For a machine that should run
the node with nobody logged in, `loginctl enable-linger <user>` is the one
extra step, and `install` prints it. Logs: `~/Library/Logs/agenthub/node.log`
on macOS, `journalctl --user -u agenthub-node` on Linux. Windows is not
supported yet; `install` says so rather than guessing.

The desktop app offers the same three actions as buttons, so nobody has to
know what launchd is.

## Two machines

This is the walkthrough that was actually run between a MacBook and an Ubuntu
22.04 box joined by a direct Ethernet cable, recorded in
[verification.md](docs/verification.md). Substitute your own addresses.

Most of it is done in the desktop app. The `ah` commands below each step are
the same thing from a terminal — useful over SSH, and useful when something has
gone wrong and you want to see the raw answer.

**1. A node on each machine.** The defaults keep everything on loopback, so a
second machine needs to be told otherwise:

```sh
# on each machine, with its own address in -peer-listen
bin/agenthub-node --db ./data/agenthub.db \
  --peer-listen 192.168.1.10:7463 --allow-lan --discover
```

`--peer-listen` is where peers connect back to, and it is the address that gets
announced, so it has to be an address other machines can reach — not loopback.
`--allow-lan` is what permits that. `--discover` turns on finding peers, and
without it the pairing commands below refuse and say so.

### What your machine calls itself

While pairing mode is open, the node announces a display name to everyone on
the segment, and it is printed at startup so you can see what that is:

```
node display name "sheldon.chang mac" (read from this machine)
```

By default it is read from the machine — `ComputerName` on macOS, the hostname
elsewhere — and it keeps following the machine on every start. That default
matters on macOS: with no `HostName` set, `gethostname()` answers from DHCP and
DNS, so a node using it can announce a name belonging to whoever held the
address before you.

To pick your own, and pin it against any later change:

```bash
bin/agenthub-node --db ./data/agenthub.db --display-name "the machine on my desk"
```

It sticks, so the flag is not needed on later starts, and the node id does not
change — existing pairings survive a rename. To hand the name back to the
machine, pass the flag empty — or as nothing but spaces:

```bash
bin/agenthub-node --db ./data/agenthub.db --display-name ""
```

The name is stored in the form it will be announced in, which is not always the
form you typed: NFD becomes NFC, `☕️` loses its variation selector, runs of
spaces collapse. That is so what you read back is what the segment sees. A name
is refused outright only if no announcement could carry it — over 64 bytes, or
made of nothing that renders.

A peer you have already paired with keeps the name it recorded at pairing time;
re-pair to update it there.

### What this node remembers

`--peer-listen`, `--allow-lan`, `--discover`, `--treat-as-private` and
`--auto-wake` work the way `--display-name` does: give one and it is recorded,
leave it off and the recorded value applies. So a service needs only `--db`,
and the desktop app can change a setting without reinstalling anything.

```sh
bin/ah settings                                   # what is running, and where each value came from
bin/ah settings set --allow-lan=true --peer-listen 192.168.1.10:7463
bin/ah settings set --allow-lan=false             # booleans take =false, so a switch can be closed
bin/ah settings set --treat-as-private 122.122.0.0/16   # replaces the whole declaration
bin/ah settings set --clear-private-ranges        # withdraws it
bin/ah service restart                            # settings apply at startup, so this is the step that matters
```

Every start prints all five with where each came from — `flag`, `remembered` or
`default`:

```
setting peer-listen = 192.168.1.10:7463 (remembered)
setting allow-lan = true (remembered)
setting discover = true (remembered)
setting treat-as-private = none (default)
setting auto-wake = false (default)
```

That is deliberate for `--allow-lan`, which is the only switch here that lets
anything leave this machine: remembered, it appears on no command line, so the
log is the one place it is visible.

A remembered value is validated on every start, with the same rules a flag gets.
If it has stopped being valid — the network was renumbered, a declared range no
longer covers the address — the node refuses to start, says the value was
remembered rather than typed, and names the flag to replace it with.

`--listen` is not remembered. The owner's API has no authentication and is safe
only because reaching it means being on this machine, so it stays a flag,
checked on every start. Neither are `--db`, `--claude-root`, `--codex-root` or
the interval flags.

These settings are read when the node starts and are wired into listeners built
once, so nothing is reloaded live: saving one and restarting are two steps, and
both `ah settings` and the API say when a saved value is not the running one.

If the two machines are on a direct cable in a range that is not private —
`122.122.0.0/16`, say — add `--treat-as-private 122.122.0.0/16` **on both**.
Without it each node refuses to list the other, because it will not deliver to
an address outside the ranges it trusts.

**2. Find each other.** Open the desktop app on both, go to the 區網 tab, and
press 開啟配對模式 on one. It appears on the other's candidate list within a
few seconds. From a terminal that is `bin/ah pairing on` and `bin/ah
candidates`.

Nothing in that list is verified. Every field was chosen by whoever sent the
packet, and the fingerprint shown is the one announced — a hint for finding the
right machine, never proof of which it is. The panel says so, and flags a row
whose name or fingerprint collides with another's.

**3. Compare the fingerprints, then pair.** Click the candidate. The dialog
fills in what was announced and deliberately leaves the public key and
fingerprint fields empty: the announcement carries no key, and that fingerprint
field is your statement that you compared one on the other machine's screen.

So compare them. Both apps show the node's own fingerprint; they must match
group for group, since comparing the first few is what an attacker defeats. Then
get the peer's public key from its own machine — `bin/ah node` there, or the
本機公鑰 line in that machine's own pairing dialog, which has a copy button —
and complete the dialog on each side.

**On each side.** Trust is recorded per machine: pairing on the mac tells the
mac who the Ubuntu box is and nothing else, and until the same is done over
there, that machine will neither accept this one's messages nor send it a
heartbeat. `ah peers` on the other machine saying `No paired nodes` is what
half-done looks like.

From a terminal the same thing is:

```sh
bin/ah node                                            # on each machine, to read and compare
bin/ah pair <their-node-id> <their-name> <their-platform> <their-public-key> <their-fingerprint>
```

The public key still has to be carried across by hand. Automating that exchange
while keeping the fingerprint comparison is issue #62.

With `--discover` running, each node learns the other's address from the
announcements; no `PUT /v1/nodes/{id}/address` is needed.

**4. Publish a session.** Pairing on its own shares nothing. In the app's 本機
tab, tick the sessions and press 設定公開對象…, then choose who and tick 允許對方
排入訊息 and 允許這個 session 主動送出訊息. Doing many at once is why the app
exists.

```sh
bin/ah list                                            # your own sessions
bin/ah audience <session-id> all-paired --messages --outbound
```

The two boxes are the same two flags: `--messages` lets other nodes send to it,
`--outbound` lets it send out. Without `--outbound` the node refuses its own
outgoing messages, on the sending side, and says so.

**5. Send, and read.** Sending is the one step with no button: it is what an
agent does, through the MCP tools in the next section. From a terminal:

```sh
bin/ah peers                                           # what they published, and the address to send to
bin/ah send --from <your-session-id> <node-id>/<their-session-id> -- "hi"
bin/ah outbound <message-id>                           # queued, delivered, or refused
bin/ah outbound                                        # ...or the last 50, newest first
```

A remote session is addressed `<node-id>/<session-id>`; `ah peers` prints that
string in its SEND TO column, because `ah list` shows only your own sessions and
nothing else showed the qualified one.

Reading has a button: 收件匣 on any session row in the app, or

```sh
bin/ah inbox <session-id>
```

**6. Give an agent the tools.** That is the next section. What no version of
this does is hand a message to an agent — it waits in the inbox until something
asks for it, whether that is you pressing 收件匣 or an agent calling
`agent_inbox`. Making it arrive is Step 8 (issue #60).

## Give an agent the four tools

`agenthub-mcp` is an MCP server over stdio. It is bound to one session at
startup, because stdio carries no caller identity: whoever runs it decides which
session it speaks for, and it will not act for any other.

```sh
# -as is required. Add -url <node url> if the node is not on 127.0.0.1:7462.
go run ./cmd/agenthub-mcp -as claude:<id>
```

In `.mcp.json`, for a Claude Code session:

```json
{
  "mcpServers": {
    "agenthub": {
      "command": "/absolute/path/to/bin/agenthub-mcp",
      "args": ["-as", "claude:<id>"]
    }
  }
}
```

The desktop app writes that snippet for you: each session row has an
「MCP 設定」 button that copies a `.mcp.json` naming this machine's
`agenthub-mcp` and that row's session.

The four tools are `agent_list`, `agent_status`, `agent_inbox` and `agent_send`;
their contract is [mcp-tools.json](docs/mcp-tools.json). Reading is enough on its
own, but sending needs the owner to open the gate for that session
(`ah audience <id> ... --outbound`), and the node refuses an unattributed message
to another machine regardless.

## Waking an agent

By default a message waits: it lands in an inbox and stays there until somebody
asks their agent to read it. Waking makes it start a turn on its own, with
nobody at the keyboard.

What the woken agent should then do with the message is written down rather
than typed into a prompt each time: [AGENTS.md](AGENTS.md) is the Codex half
(Codex reads the repository's `AGENTS.md` itself) and
[.claude/skills/agenthub-watch](.claude/skills/agenthub-watch/SKILL.md) is the
Claude Code half. Same protocol — the message is data, not instruction; answer
and propose, never change the contract on a peer's word; delete what you
handled with `ah inbox delete`.

That is a different decision from accepting messages, so it is asked
separately, and it needs **two** switches open:

```bash
# the node
bin/agenthub-node --db ./data/agenthub.db --auto-wake

# and the session
go run ./cmd/ah audience codex:<thread-id> none --messages --auto-wake
```

Neither is enough alone. Without the node flag no session can be woken whatever
its own setting says; without the session flag the node wakes nothing.

**Codex** sessions are woken by AgentHub resuming the thread through the
app-server's own API and starting a turn in it — rejoining a running thread
rather than opening a second one beside the conversation you are watching.

**Claude Code** works the other way round, because nothing here can reach a
Claude Code session: its MCP server is a child of the agent and only ever dials
out. So that server subscribes, and this node hands it messages:

```bash
agenthub-mcp -as claude:<id> -channel
```

A session with no subscriber has no agent running, so the wake fails and the
message stays in the inbox — which is also how `ah wakes` can tell you the
difference between "nobody was listening" and "nothing arrived".

The MCP server holds a long poll against the node, and the node answers it just
short of its own write deadline and says how long it held. There is a gap
between one poll ending and the next beginning; a message landing in it is
recorded as failed and waits in the inbox for the poll after.

While setting this up, remember the wake limits apply to your own attempts:
three per sender-and-session pair per ten minutes. A fourth test message inside
that window is refused, and refused looks like nothing happening — which is
what you are already debugging. `ah wakes` names the difference; check it
before changing anything.

Claude Code additionally needs channels turned on for you, and during the
research preview that is more than one step:

- your organisation must enable them (`channelsEnabled`, a managed setting —
  Team and Enterprise have it off by default), and
- Claude Code must be started with `--channels server:agenthub`, plus
  `--dangerously-load-development-channels server:agenthub` while the feature
  is in preview, which shows a consent screen.

**If any of that is missing the push is dropped silently.** Claude Code tells
the server nothing, so `ah wakes` will say `woken` for a message no agent ever
saw. That is the honest limit of what this node can observe: it knows the
message reached a live MCP server, not that a turn ran.

**And those steps are not known to be sufficient.** On a real two-node setup —
a mac and an Ubuntu box, paired, messages delivered both ways — the push has
not been observed arriving. Three attempts, with the organisation's channels
enabled and Claude Code started with both flags and showing "messages from
server:agenthub inject directly in this session":

- the node recorded `woken`, detail `handed to the session's driver`, and
  logged `wake: handed message … to "claude:…"`;
- the MCP server wrote nothing to stderr, which it only does when a push
  fails, so as far as it knows the notification went out;
- the receiving session's transcript did not grow, and Claude Code's own
  `--debug` log recorded no notification at all — not a rejected one, none.

Connecting the server at startup and connecting it later with `/mcp Reconnect`
behaved identically. The frame this server writes is a valid JSON-RPC
notification with no id, and the capability does reach a client's
`InitializeResult`; both are asserted by tests here.

A fourth attempt captured the server's stdout, and the frame is correct on the
wire — so it is lost after Claude Code reads it, past the last point anything
here can observe. Until that is understood, treat `-channel` as unverified: the
Codex path is the one with a turn observed at the other end. The full
reproduction, with what is eliminated and what is only suspected, is in
[docs/channel-push-not-observed.md](docs/channel-push-not-observed.md).

**One server per session, and `.mcp.json` does not give you that.** `-as` names
a session; an MCP config is per project. Two Claude Code sessions in one
working directory load the same config, so both start an `agenthub-mcp` with
the same `-as`, and with `-channel` both subscribe for that one session. The
node keeps one: the later subscriber displaces the earlier, which is told to
stop and does. So messages for that session go to whichever agent started last,
and the session the id actually belongs to is left silent — with `ah wakes`
still saying `woken`. Give each session its own config, or start the server
with `--strict-mcp-config` and a config of its own.

### What a woken turn may do

**It approves nothing.** Codex asks before it runs a command, changes a file,
or widens its permissions, and there is nobody present to answer. Every such
request is refused, so a woken turn runs with exactly the permissions the
session already had and cannot acquire more.

**It cannot reply outward unless you opened that too.** `--outbound` is a third,
separate switch.

**Its answer is bounded, its arrival is not.** A peer you have paired with can
start turns on your machine whenever it likes, within the limits below. If that
peer is compromised, revoking it (`ah revoke <node-id>`) or closing `--auto-wake`
is the remedy — the switches bound what a turn can do, not whether it happens.

A session with broad standing tool permissions and waking open is one whose
permissions a stranger's message can invoke without asking anyone. AgentHub
does not shrink those permissions and cannot; that combination is yours to
avoid.

### Loops and limits

Two machines that both wake automatically would answer each other until someone
noticed, and that costs real money. Three limits stop it:

| | limit |
|---|---|
| one machine → one session | 3 wakes / 10 minutes |
| one session, any source | 12 / hour |
| this node, everything | 60 / hour |

The first is keyed on the node id the signature proves, never on the session
label a sender writes — so a peer's sessions share one bucket, and nothing a
sender chooses about itself buys another. Messages that never leave this
machine share a bucket of their own.

plus a hop count that stops an exchange after 4 automatic wakes. A message
stopped by any of them stays in the inbox and can be read by hand.

### Seeing what happened

```bash
go run ./cmd/ah wakes                    # everything, newest first
go run ./cmd/ah wakes codex:<thread-id>  # one session
```

Refusals are in there too: an empty answer means the node was quiet, and a
refused row means something arrived and was held back.

To find a woken turn in your own Codex history without taking this trail on
trust, look for the `<external_agenthub:message>` element Codex wraps the
message in, or the notice text inside it. This node also sends
`turnTrigger: agenthub-wake` with the turn, but do not look for that: grepped
on a real woken thread's rollout file it appears zero times, as does
`turnTrigger`, while the turn itself is plainly there. codex-cli 0.153.4 does
not persist it.

**A `woken` row means a turn was handed over, not that the model answered.**
Both are real: a thread on a model the account cannot use started its turn and
ended with a 400 and no agent message, and the row still said `woken`. So did
every Claude Code attempt, none of which arrived at all. The row is the
furthest this node can see — that a driver took the message — and the place to
confirm a turn ran is the agent's own history.

The reasoning behind all of it, including what it deliberately does not solve,
is [ADR-003](docs/decisions/003-waking-with-nobody-present.md).

## Desktop app

The desktop app is the owner's management surface for the privacy model. It
lists local sessions, filters by provider/status/audience/working directory,
applies an audience policy to a selection, manages manually paired nodes, opens
and closes pairing mode with the candidate list beside it, and reads what other
nodes have queued for a session. Sending is not there: that is what an agent
does through the MCP tools.

It lives in `desktop/` as a separate Go module so that Wails' CGo requirement never reaches `agenthub-node` or `ah`, which stay CGo-free and cross-compilable.

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@latest
cd desktop
wails dev     # live-reload development
wails build   # produces build/bin/agenthub-desktop.app
```

The app requires a running node and talks to it over the same local HTTP API as the CLI. It refuses non-loopback node URLs, because the owner's API has no authentication and stays on loopback for that reason.

When the node is not running, the panel under the header offers to install it
as a background service, with the node's flags as form fields: the address other
machines connect to is picked from this machine's interfaces, `--allow-lan` is
ticked when that address is not loopback and explained as the one switch that
lets anything leave the machine, and a non-private address pre-fills
`--treat-as-private` with that interface's own subnet — the range the cable
carries, not a wider guess. When the node is running, the same panel says whether it
is a service and offers to remove it. Both buttons run `ah service`, so the
app and the CLI cannot disagree; the app looks for `ah` beside itself, in the
source tree, then on `PATH`, and `AGENTHUB_AH` names it explicitly.

## Privacy model

`ah list` is an owner-local view and can show private sessions. Publishing
decides which paired nodes receive a session in the heartbeat built for them:

```text
discovered session -> audience: none -> absent from every export view
                                       |
                                       +-- all_paired -> every paired node, including future ones
                                       +-- selected   -> only the nodes the owner named
```

Publishing answers *to whom*, not merely *whether*. `all_paired` and `selected`
differ the moment a new node is paired, so they are separate choices rather than
a list that happens to hold everything.

Two per-session flags default closed: the working directory travels only when
the owner opts in, and a session accepts queued messages only when the owner
opts in.

Pairing a node establishes identity only. It publishes nothing: the audience is
a separate, per-session decision. Revoking a node withdraws trust and every
grant it held, in one step, so re-pairing later does not restore access.

The preview includes only sessions published to at least one audience and is
projected into an allowlisted `SessionSummary`: the qualified AgentHub address,
provider, status, management
mode, `statusSource`, last-seen time, and working directory. Provider source,
provider session ID as a separate field, internal update time, metadata paths,
transcript bodies, and prompt contents are excluded, and the published schema
rejects them. The working directory is omitted unless that session's
`exportCwd` flag is enabled.

The model is documented in
[ADR-001](docs/decisions/001-session-audience-and-export-boundary.md) and is now
implemented. A database written by an earlier build upgrades with every session
at audience `none`, including rows previously marked public: that flag controlled
a local preview at a time when no remote peer existed, so it was never consent to
share with one.

Queued AgentHub messages are stored in the local SQLite database. They are not written into a Claude or Codex session's files or process — that is a decision, not a stage — and a successful `ah send` means queued. For a remote destination `ah outbound <message-id>` reports what became of it later, and nothing hands the message to an agent.

See [architecture](docs/architecture.md), [MVP specification](docs/spec.md), [multi-node plan](docs/multinode-plan.md), [broker protocol](docs/broker-protocol.schema.json), and [MCP tool contract](docs/mcp-tools.json).

The Codex App Server client boundary is implemented and schema-tested, but is not enabled in the node's default scan path yet. See [Codex App Server notes](docs/codex-app-server.md).

## Local API

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/discover` | Rescan provider metadata; reports per-provider counts and `skipped` |
| `GET` | `/v1/sessions?page=1&pageSize=50` | List owner-local sessions |
| `GET` | `/v1/sessions/{id}` | Read one session |
| `PUT` | `/v1/sessions/{id}/visibility` | Compatibility: `public` means the explicit all-paired choice |
| `GET` | `/v1/sessions/{id}/audience` | Read one session's export policy |
| `PUT` | `/v1/sessions/{id}/audience` | Replace one session's export policy |
| `POST` | `/v1/sessions/audience` | Apply one policy to many sessions |
| `GET` | `/v1/heartbeat` | Owner preview of a signed heartbeat; union of sessions published to any audience, addressed to this node so no peer can accept it |
| `GET` | `/v1/nodes` | List paired nodes |
| `POST` | `/v1/nodes` | Manually trust a node whose full fingerprint the owner compared |
| `DELETE` | `/v1/nodes/{id}` | Revoke trust and every grant that node held |
| `PUT` | `/v1/nodes/{id}/address` | Record where a paired node is reachable. Delivery skips a peer without one, silently, while `ah send` still answers `queued`. `ah nodes address <node-id> <host:port>` is this call |
| `GET` | `/v1/node` | This node's own identity and fingerprint |
| `GET` | `/v1/node/settings` | The start-up settings in effect, where each came from (`flag`, `remembered`, `default`), what the next start will use, and whether those differ |
| `PUT` | `/v1/node/settings` | Remember some or all of `peerListen`, `allowLan`, `discover`, `treatAsPrivate`, `autoWake`. Validated with the node's own start-up rules; answers `restartRequired: true`, because these are read only at startup |
| `GET` | `/v1/peers` | Presence: paired nodes, online state, and the sessions each has authorised for this node. `ah peers` renders it, including the address to send to |
| `POST` | `/v1/messages` | Queue a message for a local session, or — with `from` naming a local session whose owner opened outbound — for a session on a paired node |
| `GET` | `/v1/inbox/{id}` | Read a local inbox, in pages: `limit` (1–200) and `after` (the `next` value a full page carries) |
| `DELETE` | `/v1/inbox/{id}` | Empty one session's inbox |
| `DELETE` | `/v1/inbox/{id}/{messageId}` | Drop one message; `ah inbox delete <session-id> <message-id>` is this route |
| `GET` | `/v1/outbound?limit=50` | What this node has queued for peers, newest first: `limit` (1–200) and `after` (the `next` value a full page carries). No bodies — state, attempts and the last error |
| `GET` | `/v1/outbound/{id}` | What became of one queued message |
| `GET` | `/v1/pairing` | Whether this node is advertising, and what the announce loop last managed to send |
| `POST` | `/v1/pairing` | Open the window, optionally `{"seconds":N}` (30s–15m, default 5m) |
| `DELETE` | `/v1/pairing` | Stop advertising now |
| `GET` | `/v1/pairing/candidates` | Machines advertising right now. Every field is the sender's own claim |

The four pairing endpoints exist only under `-discover`; without it they answer
`409 DISCOVERY_DISABLED` rather than an empty list, because "nobody is
advertising" and "this node is not looking" are different answers and only one
of them means the owner should keep waiting.

Advertising also needs somewhere for a peer to connect back to, and that is the
peer listener's own bound address — so it needs `-allow-lan` *and* a
`-peer-listen` on this machine's network address. What gets announced is that
one address, sent from that address and out of the interface holding it, because
a receiver lists an offer only when the address it carries is the address the
datagram came from.

Opening the window is refused with `409 NO_ANNOUNCEABLE_ADDRESS` when there is
no such address, and the message says which case applies: a loopback listener
that no other machine can reach, an IPv6 listener that is perfectly reachable
but cannot be discovered while announcements go out on the IPv4 group, a
`-peer-listen` naming a host rather than one address, or an address whose
interface cannot carry a multicast packet — a point-to-point or VPN interface,
where the address is fine and the announcement has nowhere to go. That last one
is checked when the window is asked for rather than at startup, because an
interface can lose the ability after boot. Announcing anyway would advertise an
address nothing is listening on: the peer would see a candidate that looks
right, with a matching fingerprint, and get a refused connection.

A node with `-discover` joins the group on every interface that can carry it,
re-checked every ten seconds so an adapter plugged in after startup is picked up
without a restart — which is how a peer whose own listener is on a direct cable
gets heard rather than silently missed. `GET /v1/pairing` carries `announcing`
because an open window and a machine that is actually sending packets are
separate facts: it reports how many addresses this node can announce, when it
last tried and last succeeded, and why nothing is going out.

Nothing in the candidate list is verified and appearing in it grants nothing.
The fingerprint shown is the one announced, which is a hint for finding the
right row and never evidence; what settles identity is comparing the
fingerprint of the key that arrives in the handshake, on both machines.

The peer listener serves a separate mux on `:7463` over TLS: `POST /v1/challenge`, `POST /v1/heartbeat`, and `POST /v1/messages`. It is never the owner's API.

See [verification notes](docs/verification.md) for the tested platform matrix and remaining runtime checks.
