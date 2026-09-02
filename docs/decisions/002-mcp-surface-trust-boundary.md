# ADR-002: What the MCP surface trusts, and what it cannot defend

## Status

Accepted

## Date

2026-09-02

## Context

Step 7 gave agents a way to reach AgentHub: `agenthub-mcp`, a stdio server the
agent launches, exposing four tools. Three of them return metadata this
installation observed. The fourth, `agent_inbox`, returns **what a person on
another machine wrote**, into the same context window as the agent's own
instructions.

That is a boundary the project did not have before, and it is not the boundary
it first appears to be. This ADR records what each defence does, and — more
importantly — what none of them do.

## Decisions

### 1. Content filtering is not attempted

A message body is returned byte for byte, escaping aside. No keyword list, no
"suspicious instruction" heuristic, no rewriting.

Filtering cannot survive paraphrase. Any list that catches "ignore previous
instructions" misses "disregard the earlier guidance", and an attacker writing
the second costs nothing more than one writing the first. What such a filter
would reliably produce is the belief that inbox content has been made safe.

The defences are therefore structural, and both are about what happens around
the content rather than what is in it.

### 2. The inbox is not the only channel, and framing covers only it

A fresh-context review found the limitation this ADR's first draft missed.

A peer's **session summaries** are also written by that peer. Its `cwd`,
`status` and session ids arrive over the wire, are stored unvalidated, and are
served by `agent_list` with no notice, no sender attribution, and no untrusted
framing (#76). A peer can put a paragraph of instructions in a working directory
and the agent reads it inside the one payload the server's own `Instructions`
string vouches for.

That is worse than the inbox, not better, precisely because it looks like
observed metadata. The first draft of this ADR said "everything else here is
metadata this installation observed"; both halves of that were false, and the
sentence has been removed from the code comment it came from.

What distinguishes the inbox is not that it is the only attacker-authored
content — it is that its content is unmistakably a message from a person, so
framing can be applied to it at all. #76 validates the other channel at the
receiving edge, which is the only place it can be done once for every reader.

### 3. Presentation: content is data-shaped, never prose-shaped

A body is its own JSON field. The notice addressed to the reading agent is a
*sibling* of the messages, not a wrapper, so no message can appear to be part of
it. Every message carries the sender's node id and, for a peer, the fingerprint a
person can compare out of band.

What this achieves: an instruction inside a message cannot be mistaken for an
instruction from the server, because the two never share a string. What it does
not achieve: stopping an agent from *choosing* to follow an instruction it can
plainly see is from a stranger. That is the model's judgement, and this project
does not control it.

### 4. Outbound authorisation bounds the consequence

`allowOutbound` is per session and closed by default, separate from
`acceptMessages`: willing to receive is not willing to send.

It is checked **before** the destination is resolved, so a closed session cannot
be used to enumerate what is visible.

**Where it is enforced matters, and the honest answer is not flattering.** The
check lives in `agenthub-mcp`, a client of the node. `POST /v1/messages` knows
nothing about the flag, so any process on this machine can post directly —
including an agent with a shell, which can read the node URL from this process's
argv.

So this closes the path an agent takes by *following an instruction it read*,
which is the path inbox content actually opens. It is not a boundary against an
agent that has decided to work around it. #75 moves enforcement to the node,
where the policy lives.

### 5. The caller's identity is fixed at startup, not per call

A stdio MCP server is launched by the agent as a child process, and the protocol
carries nothing saying which session called it. A server that accepted a session
id per call would let any agent on this machine read any session's inbox and send
as any session — the local half of `acceptMessages` and audience would stop
meaning anything.

`-as` binds one process to one session, and the process refuses to start without
it. `Binding`'s field is unexported and the server type is unexported, so there
is no literal a caller inside the package can write either.

### 6. Remote data comes from presence, never the registry

The node applies each peer's audience when it accepts that peer's heartbeat, so
presence already **is** the authorised view. Reading the registry for remote
sessions would be a second implementation of that filter, free to disagree with
the first — and the one that disagreed is the one an agent would see.

Enforced structurally: `agenthub-mcp`'s dependency closure contains no registry,
no `database/sql`, and no SQLite driver, asserted by a test that runs
`go list -deps` on the command.

### 7. A peer describes only its own sessions

The node authenticates who sent a heartbeat but does not check that the session
ids inside name that sender (#72). Believed, a paired peer could attribute a
session to a third node that authorised nothing, or send a bare local-form id
colliding with one of this machine's own sessions — after which asking about that
session could return the peer's fabrication.

`Peers()` requires every row to be `<sender's nodeId>/<provider>:<id>` and
discards the whole snapshot of any peer that violates it. Only that peer's:
failing the entire call would let one paired peer blank the owner's view of their
own machine.

## What this does not defend against

Recorded because a threat model that lists only solved problems is an
advertisement.

- **A compromised peer, within what its audience authorised.** It can send
  messages to sessions that accept them, and it sees the sessions its owner
  published to it. Nothing here reduces that; revocation is the answer, and it
  is immediate for anything not yet delivered — trust is looked up per request,
  and revoking drops the peer's presence in the same transaction. What is already
  in an inbox stays, and is shown without a fingerprint.
- **An agent that decides to comply.** Presentation makes the provenance of a
  message unmistakable. It cannot make a model refuse.
- **An agent that works around the gate.** Until #75, `allowOutbound` is
  enforced by a client of the node rather than the node. An agent that reasons
  its way to `curl`-ing the owner's API is not stopped by it. This is the gap
  between "an agent following instructions" and "an agent pursuing a goal", and
  only the first is addressed today.
- **The owner's own API.** It is loopback-only, and anything that can reach it
  can already restart the process. `agenthub-mcp` additionally refuses a
  non-loopback node URL, but that is a guardrail against misconfiguration, not a
  trust boundary — any local port-forward defeats it, and legitimately.
- **Wake-up.** Nothing yet hands a message to an agent unprompted. When it does
  (#60), content will reach an agent's reasoning with **nobody present**, which
  is a different risk from the same content read on request. `autoWake` (#59) is
  a third gate for exactly that reason, and #59 is a merge condition for Step 8,
  not a follow-up.
- **Provider injection.** Out of scope by decision (#16), unchanged.
- **A peer that lies about its own state.** Presence content is unvalidated
  (#76), a peer sets its own expiry so it can appear online forever (#77), and a
  peer can fill a session's inbox or make `agent_inbox` fail outright by
  exploiting JSON expansion against the client's read cap (#78, #79). None of
  these read another owner's data; they degrade or mislead this one. They are
  open, and they are the reason this section exists.

## Consequences

- Three independent flags, all closed by default: `acceptMessages` (inbound),
  `allowOutbound` (outbound), `autoWake` (unattended, #59). Each answers a
  different question, and no call that cannot express a choice may make one —
  `SetVisibility` resets all three, which means publishing a session closes a
  gate the owner had opened. That is the safe direction and it is tested in both
  directions, but it will surprise someone.
- The MCP surface is four tools and cannot grow quietly: a test asserts the
  exact set, and another refuses any import of a database driver or `os/exec`.
- #72 must fix the attribution gap at the node, where every reader benefits —
  the desktop reads the same endpoint with no such check (#80). The check in
  `Peers()` stays regardless: it is the layer that hands the answer to an agent,
  and it should not assume its upstream.
- The findings this document exists to record were found by a reviewer with no
  prior context, after three rounds of review by people who had watched it being
  built. The most serious — a peer omitting one field to have its message
  rendered as originating on the owner's own machine — was in code written the
  same day, by someone who had just finished writing that inferring trust from
  string shape was the mistake to avoid.
