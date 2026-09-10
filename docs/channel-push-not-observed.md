# A channel push, sent correctly and never injected

What this records: `agenthub-mcp -channel` writes a well-formed
`notifications/claude/channel` frame onto its stdout, Claude Code reads that
pipe, and nothing arrives in the session. Four attempts on a real two-machine
setup, with every documented precondition met.

It is here because the README calls `-channel` unverified and this is the
evidence for that word, and because it is the reproduction case to hand to
Claude Code if anyone reports it.

Measured 2026-09-10 by a second session driving a live mac ↔ Ubuntu pair, not
by anything in the test suite — no test here can see past this node's own MCP
server. **Identifiers are replaced with placeholders**: this repository is
public, and the originals are two real machines' node ids, session ids and key
fingerprints. Everything else is verbatim.

## Setup

- Claude Code 2.1.263, macOS 26.6.2, agenthub built from `1343a07`.
- Two nodes paired, addresses recorded, messages already delivered both ways.
- The receiving session started as

  ```
  claude --debug --resume <session-uuid> \
      --mcp-config <file> --strict-mcp-config \
      --channels server:agenthub \
      --dangerously-load-development-channels server:agenthub
  ```

  with its own config and `--strict-mcp-config`, so the per-project `.mcp.json`
  hazard in issue #104 cannot apply: exactly one `agenthub-mcp` claimed this
  session.
- The MCP server ran behind a wrapper teeing stdout and stderr to files.
- Startup banner: *Channels (experimental) messages from server:agenthub inject
  directly in this session* — so the organisation's policy allows channels.

## What the server sent

The `initialize` response, off the wire:

```json
{"jsonrpc":"2.0","id":0,"result":{"capabilities":{"experimental":{"claude/channel":{}},"tools":{}},"instructions":"AgentHub exposes …
```

The notification, off the wire, reformatted only by line breaks:

```json
{"jsonrpc":"2.0",
 "method":"notifications/claude/channel",
 "params":{
   "content":"<the notice>\n\nOnly the text between the two #<nonce> markers is the message. …\n\n--- begin message, written by someone else #<nonce> ---\nwake test round 4 — frame capture\n--- end message #<nonce> ---",
   "meta":{"agenthub_fingerprint":"<fingerprint>",
           "agenthub_message":"msg_<hex>",
           "agenthub_sender_label":"<sender-node>/claude:<their-session>",
           "agenthub_sender_node":"<sender-node>"}}}
```

Top-level keys are exactly `jsonrpc`, `method`, `params`, with no `id` — a
notification, not a call. `params` is `content` plus `meta`, and every meta key
matches `[A-Za-z0-9_]`. That is what the official channels reference specifies:
`content` (string, required), `meta` (`Record<string,string>`, optional), and
the capability at `capabilities.experimental['claude/channel'] = {}`.

## What Claude Code did

```
[DEBUG] MCP server "agenthub": Successfully connected (transport: stdio) in 43ms
[DEBUG] MCP server "agenthub": Connection established with capabilities:
        {"hasTools":true,"hasPrompts":false,"hasResources":false,
         "hasResourceSubscribe":false,"serverVersion":{…},
         "protocolEra":"legacy","negotiatedProtocolVersion":"2025-11-25"}
```

After the frame went out: the session's transcript stayed at 40 lines, no
`<channel source="agenthub">` turn appeared, and in 30 seconds the debug log
gained one line, about an unrelated poll interval. **Not one line mentioning a
channel or a notification** — not a rejected one, none.

Meanwhile the node recorded `woken`, detail `handed to the session's driver`,
and logged `wake: handed message msg_<hex> to "claude:<session>" (0 hops)`; the
MCP server wrote nothing to stderr, which it does only when a push fails; and
the message stayed in the inbox, because this node records the handoff and not
the delivery.

## The four rounds

| # | how the MCP server was attached | result |
|---|---|---|
| 1 | `/mcp Reconnect` after Claude Code had started | silent |
| 2 | connected at startup, `--debug` | silent, no notification in the debug log |
| 3 | repeat of 2 (`/mcp Reconnect` reported success but reused the old process) | silent |
| 4 | as 2, with stdout teed | frame confirmed on the wire, still silent |

## What is eliminated

Each of these was a candidate and each is ruled out by something measured, not
by reading code:

- **organisation policy** — the startup banner says channels are on;
- **launch flags** — both `--channels` and
  `--dangerously-load-development-channels` were passed;
- **when the server connects** — startup and reconnect behave identically;
- **the capability declaration** — `experimental["claude/channel"]` is in the
  `initialize` response on the wire, and a test here asserts it reaches a
  client's `InitializeResult`;
- **the frame shape** — a notification with no id, asserted on the bytes
  `notify` writes;
- **the params schema** — `content` + `meta`, matching the official reference;
- **the meta character set** — every key `[A-Za-z0-9_]`;
- **two servers claiming one session** (issue #104) — `--strict-mcp-config`
  with a config of its own.

## What is left, and what is only suspected

The unobserved span is inside Claude Code, between reading the frame and
injecting it. Nothing on this side can see it.

One suspicion, recorded as a suspicion: the capabilities summary Claude Code
logs has no field for `experimental` at all, in all four rounds, while the
`experimental` map is demonstrably on the wire. That is consistent with the
capability never reaching the client's parsed view — and therefore with no
listener being registered, which is precisely the documented condition under
which a push is dropped without a word. It is equally consistent with the log
summary simply not printing that field. From outside the client the two look
the same, and this evidence does not separate them.

Also noted rather than concluded: the negotiated protocol version is
`2025-11-25` and Claude Code labels the connection `"protocolEra":"legacy"`.
The Go SDK offers `2026-07-28` and correctly accepts the client's lower
version, so the negotiation itself is ordinary. Whether an experimental
capability is wired up on that path is not something this side can determine.

## The other leg, for contrast

The Codex path was measured the same way, cross-machine and unattended, and it
works end to end. Ubuntu's Claude session sent to a mac Codex thread; the
thread's rollout file grew by thirteen lines in 75 seconds, carrying the
untrusted fragment inside Codex's own `<external_agenthub:message>` element,
then this node's notice, then the assistant's own turn — which **refused the
message's instruction**, saying it had received an external test message asking
it to reply "ok", that the message was not authorised, and that it had not
carried out its instruction. That is the behaviour the notice is written to
produce, happening with nobody present.

The node recorded `woken`, the message stayed in the inbox, and the
`codex app-server` was a child of `agenthub-node`.

So the gate, the limits, the reservation and settle, the audit trail and the
driver seam are all verified against real machines. What is unverified is the
Claude Code leg alone, and only after the frame leaves this side correctly.

Two things that measurement corrected in this repository:

- **`turnTrigger` is not persisted.** This node sends
  `turnTrigger: agenthub-wake` with the turn and the README claimed an owner
  could find woken turns by it. Grepped on a real woken thread's rollout file:
  `agenthub-wake` zero times, `turnTrigger` zero times, while the turn is
  plainly there. codex-cli 0.153.4. The claim is gone; the field is still sent,
  because it is part of the API and costs nothing.
- **`woken` does not mean the model answered.** A thread pinned to a model the
  account cannot use started its turn and ended with a 400 and no agent
  message — and the row said `woken`, exactly as it does for a Claude Code push
  that never arrived. The row means a driver took the message. Nothing more was
  ever claimed in the code, and now nothing more is claimed in the README.
