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

   Both screens show **both** fingerprints, labelled, in the same order: the
   other machine's and this machine's. Each is derived locally, with
   `identity.Fingerprint`, from the key that side actually received. A
   fingerprint that arrived in a descriptor is never displayed. If it were, a
   substituted key could travel with the fingerprint of the key it replaced, and
   the only check in the system would pass.

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

6. **Pairing writes identity and nothing else.** No `session_audience` row is
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
