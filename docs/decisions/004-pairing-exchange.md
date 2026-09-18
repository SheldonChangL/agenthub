# ADR-004: the pairing exchange carries the keys; the people still compare them

Status: accepted. Issue [#62](https://github.com/SheldonChangL/agenthub/issues/62).

## Context

Until now pairing two machines meant running

```
ah pair <node-id> <name> <platform> <base64-public-key> <fingerprint>
```

on **both** machines, with the five values carried across by hand — in practice
over ssh, by copying a public key out of one terminal and into another. Issue
#61 made a node announce itself on the local network so the other machine can
see it in a list, but multicast does not always arrive: a different subnet, a
cable between two laptops, a firewall. On the owner's own two-machine rig it did
not arrive, and the fallback was still the copied key.

Copying a key is not just tedious, it is the step that loses people. It is also
the step where a mistake is invisible: nobody re-reads 44 base64 characters.

## Decision

A signed exchange carries the keys, and the fingerprint comparison stays exactly
where it was.

1. **Request and poll, never a call back.** The requester (A) dials the
   receiver's (B) existing peer TLS listener and posts a signed, undirected
   `pair.request`. B answers `202` with a request id and its own descriptor. A
   then *polls* `GET /v1/pair/requests/{id}` for the answer.

   B never dials A. On the networks this feature exists for, one direction works
   and the other does not — NAT, a firewall, a laptop with no reachable address —
   and a design that needed the reverse connection would fail in exactly the case
   it was written for. It also means B never has to be told where A is.

2. **The fingerprint comparison is mandatory, on both machines, and is not
   skippable.** B's owner runs `ah pair approve <id>`; A's owner runs `ah pair
   confirm <id>`. Each writes only its own trust store. An approval on one
   machine is not consent on the other: the person there has compared nothing at
   that moment, and a design where one screen decided for two would make the
   comparison optional in practice. There is no auto-accept and no skip, and
   nothing here is offered as a convenience toggle.

   Both screens show **both** fingerprints, in one canonical order: the machine
   that asked, then the machine it asked. Requester-first on both machines,
   rather than own-first on each, because own-first reverses the order between
   the two screens — two people reading down two lists then compare line one
   against line two. Each line carries the display name that machine calls
   itself and whether it is this machine or the other one, and the notice
   describes exactly what is printed. A notice that does not match the screen is
   worse than none: people stop reading it and start guessing.

   Each value is derived locally, with `identity.Fingerprint`, from the key that
   side actually received. A fingerprint that arrived in a descriptor is never
   displayed. If it were, a substituted key could travel with the fingerprint of
   the key it replaced, and the only check in the system would pass. Tests feed
   a descriptor whose fingerprint field lies, on both the incoming and the
   answer path, and assert the locally derived value is what is shown and
   stored.

   There is no local name for the peer. An earlier `--name` flag stored a
   locally chosen name as the peer's display name, so the two screens showed
   different names while the owner was being asked to check that the two screens
   agree.

3. **The TLS key must equal the descriptor key.** There is no CA here, so A's
   first connection cannot pin anything — it *records* the key that terminated
   the handshake instead, and pins every later request in that exchange to it. A
   then checks that the recorded key is the key in the descriptor B signed. If
   they differ, something completed the handshake with its own key and passed on
   the real machine's descriptor; A abandons the exchange and shows nothing,
   rather than showing a fingerprint for a machine it is not talking to.

   This check is not a replacement for the human comparison. It is the reason
   the human comparison is decidable: the key whose fingerprint is displayed is
   the key that will terminate every later connection to that peer.

4. **Consent is the pairing window.** B accepts a `pair.request` only while its
   owner has opened a pairing window (`ah pairing on`). Closed, the endpoint
   answers `403 PAIRING_CLOSED` and stores nothing — a node that is not pairing
   must not accumulate a list of strangers for its owner to find later. Closing
   the window expires what it collected, and a request expires five minutes after
   it arrives regardless. Nothing is written on expiry, and "rejected" and
   "expired" stay distinct on both screens.

5. **The owner surface and the peer surface stay separate muxes.** `approve` and
   `confirm` exist only on `Handler()`. The peer half — `POST /v1/pair/requests`
   and the poll — exists only on `PeerHandler()`, beside `POST /v1/challenge`,
   which already answers callers that are not in the trust store. A peer that
   could reach `approve` would be approving itself.

6. **A refusal travels, and takes its own trust row back.** The refusing side
   pushes a signed, directed `pair.reject` to the other machine's peer listener
   (`POST /v1/pair/requests/{id}/reject`), pinned to the key the exchange
   recorded, verified with `VerifyDirected` plus request-id equality. The
   receiver marks the request rejected; if it had already approved, it revokes
   the trust row that approval wrote.

   Nothing used to be sent from the asking side, on the argument that the other
   machine would expire on its own. Two-machine testing showed what that costs:
   the other owner had already approved, so refusing because the fingerprints
   did *not* match left that machine trusting exactly the key its owner had been
   told to refuse, with nothing on either screen saying so.

   Revocation is guarded twice, because it is destructive: the request row must
   record that this request is what wrote the trust (`trustedByRequest`), and
   the key stored under that node id must still be the key the request carried.
   A node paired months ago by some other route is not revoked by a refusal that
   happens to name it.

   **The approval writes the trust row first and decides second; the refusal
   decides first and revokes second.** Both orders exist so that whichever of
   the two loses the one atomic transition — `SettleFrom`, which moves the row
   and records `trustedByRequest` under a single hold of the request store's
   lock — is the side holding something it can undo. An approval that loses
   takes back the trust row it has already written. A refusal that wins has
   nothing of the approval's to find, because the approval will take it back
   itself. The order that came before this had an unguarded window in each
   direction: settle-then-write left a refusal reading between the two writes
   revoking nothing while the trust row was written behind it, and
   settle-then-revoke on the refusing side left a failed revoke returning early,
   with the row saying `rejected` and the owner's screen saying the trust "has
   been withdrawn" while the key was still in the store.

   A revoke that does not happen is recorded on the row (`trustLeftInPlace`) in
   the words the owner is shown, and every sentence about that request is built
   from it. Both ways it can happen — a stored key that is not this request's,
   and a store that refuses the revoke — say something is still trusted and name
   `ah revoke`. Nothing claims a withdrawal that did not occur; if even the
   recording fails, the refusal answers 500 rather than letting a row be read as
   a withdrawal.

   The guarantee is stated about the row, not about a state. An approval loses
   the transition to an expiry as well as to a refusal — the window runs out
   between the trust write and `SettleFrom`, the sweep marks the row `expired`,
   and the compensation records `trustLeftInPlace` on an expired row. The
   sentence the owner reads is therefore chosen by `trustLeftInPlace` before any
   state is looked at; while it was chosen by `rejected` first, an expired row
   with a key still in the store read "Nothing was trusted".

7. **Pairing writes identity and nothing else.** No `session_audience` row is
   created. Two machines that have paired can see nothing of each other until
   somebody sets an audience per session.

## Consequences

- `ah pair <five args>` stays. It is what still works when the two machines
  cannot open a connection to each other at all, and removing a working escape
  hatch to celebrate a new one is how a feature becomes a dependency.
- An older node has no such route and answers `404`. The CLI turns that into a
  sentence naming the cause and the two remedies, because "404" sends an owner
  looking for a typo in an address that is correct.
- The request id is the capability that guards the poll: 128 random bits chosen
  by B and returned only to whoever sent the request. Everything it reveals is
  either already public (B's descriptor) or is the answer B's owner just gave to
  that request. Authenticating the poll would need a trust relationship that
  does not exist yet — it is what the exchange is trying to create.
- The polling is driven by the owner reading the list (`ah pair pending` →
  `GET /v1/pair/requests`), not by a background loop. That is the only moment
  the answer is wanted, and a loop would keep dialling an address for five
  minutes after the owner stopped caring.
- Pending requests are bounded (16 per direction) and the peer route sits behind
  the existing peer rate limiter. The bound is not about memory: it is about the
  owner's list of fingerprints to compare not becoming a page of noise with the
  real machine buried in it.

  A bound keyed on the node id bounds nothing, because the node id is chosen by
  whoever sends the request: sixteen fresh ones filled the incoming list and the
  owner's real machine was answered `PAIRING_BUSY` during the very window they
  had opened to pair it. So the incoming list is bounded **per source address**
  (three), which is the one thing in an incoming request its sender cannot
  invent freely.

  A full incoming list **displaces only within one source address**: a newcomer
  may push out the oldest row still waiting *from its own address*, and is
  answered `PAIRING_BUSY` when that address has no row to give up. Displacing
  the oldest row overall was the first attempt and was worse than refusing —
  with three rows allowed per address, six addresses fill the list, and every
  request after that evicted whoever happened to be oldest. A flooder rotating
  source hosts could therefore make the owner's own machine show as
  `displaced`, which is the failure the bound exists to prevent. Keyed on the
  source host, a sender can only ever push out its own earlier attempt. The
  residual is accepted and named: an attacker with an unlimited supply of source
  addresses can still fill the list and make this node answer `PAIRING_BUSY`,
  but it can never evict a stranger's row — the owner sees a busy node rather
  than a request of theirs that silently disappeared. The displaced row says
  `displaced` rather than `expired`: the owner did not run out of time,
  something filled the list. Outgoing requests are never displaced — sixteen of
  them means the owner asked for sixteen, and silently cancelling one would be
  this node choosing which of their pairings to abandon.

- The window is a node-level state and does not need `-discover`. The window is
  consent; announcing over mDNS is discovery's half, and requiring the flag to
  open a window made this exchange unusable in exactly the case it exists for —
  two machines that cannot hear each other's announcements. A node that cannot
  announce opens the window anyway and says so in the same answer, with the
  address the other machine has to be given. `GET /v1/pairing/candidates` still
  answers `409 DISCOVERY_DISABLED`, because a candidate list really does need
  the network.

- Reading the list polls every pending outgoing row **concurrently**, with a
  three-second timeout each. The dialer's own ten seconds is right for
  delivering a message and wrong here: this runs while an owner waits at a
  prompt, and serially a handful of machines that had gone away was minutes of
  silence.

- `ah pair pending` shows only what still needs somebody to do something;
  `--all` adds the rows that finished, which are kept for ten minutes. A list
  where the one row awaiting a decision sat under four decided ones is how an
  owner misses their own pairing.

## Residual gaps

- **An approval this side gave cannot be expired by this side.** After `ah pair
  approve`, the machine that was asked has written its trust row and has no way
  to learn whether the other owner ever confirms: the requester polls, and a
  confirmation is a local act that sends nothing. A refusal is pushed and does
  revoke; silence is not. The row therefore stays `approved`, and its
  `nextStep` says so in words with a remedy — "wait for them to confirm; if they
  never do, undo it with `ah revoke <node-id>`" — in the answer to `approve`
  itself and in `ah pair pending --all`. Closing this properly needs a signed
  `pair.confirm` travelling back, or an expiry the approver can apply without
  guessing; both are more protocol than this issue should add, and either would
  have to distinguish "never confirmed" from "confirmed and the message was
  lost", which silence cannot.

- **A refusal that cannot be delivered is local only.** If the other machine is
  unreachable when `ah pair reject` runs, the refusal stands here and the answer
  says the other machine could not be told, naming the command its owner should
  run. There is no retry queue: this node has just decided not to trust that
  machine, and keeping a job that dials it would be the wrong thing to hold on
  to.

- **A crowded-out answer can no longer be polled.** `MaxPending` and
  `MaxPendingPerSource` count only rows still waiting, so until now nothing
  bounded the decided ones. Displacement is where that leaked: once the pending
  list is full, a source that still holds a row in it gives up its own oldest to
  make room for its next one, which puts its pending count back where it was and
  leaves a decided row behind for ten minutes. The loop has no end, up to
  `MaxPending` source addresses can be in it at once, and the peer limiter allows
  each of them 120 requests a minute.
  `MaxRetained` (4 × `MaxPending` = 64 rows in total) and
  `MaxRetainedPerSource` (2 × `MaxPendingPerSource` = 6 decided rows per source
  address) now bound the table. Only decided rows are ever dropped, oldest
  first, and the per-source bound is applied before the total one, so a flooder
  crowds out its own history rather than anybody else's and a row still waiting
  for the owner is never evicted. What is given up is the guarantee that both
  sides can poll for an answer during the whole `retainDecided` window: under a
  flood from one address, that address's older answers are forgotten early. The
  alternative was holding every answer a stranger can generate.

- **The per-source bound is a bound on a host, not on a machine.** `MaxPendingPerSource`
  keys the flood limit on the address a request arrived from, which is the one
  thing its sender cannot choose — but two machines can share that address:
  another container or user on this same host, or anything behind the same NAT
  when the owner's machine is behind it too. An attacker in that position holds
  three pending rows from the shared source and the owner's own request is then
  answered `TOO_MANY_FROM_SOURCE`, with the incoming list otherwise empty. The
  owner's remedy is to refuse the rows they do not recognise, which the list
  shows with their fingerprints; closing it properly would need a bound on
  something the sender cannot share, and at pairing time — before any key is
  trusted — there is no such thing.
