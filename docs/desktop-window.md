# What the desktop window is not telling you

The window says one thing per state. The paragraphs that used to sit under
those sentences are here, because a screen where every state carries three
explanatory sentences is a screen nobody reads. Each heading is reached from
the window by a line that names this file.

## A paired machine whose heartbeat never arrives

The node lists every machine it trusts, whether or not that machine trusts it
back, so a row with no heartbeat has two possible causes and this machine
cannot tell them apart:

1. The other side has not paired with this one. Trust is recorded by each
   machine separately: pairing here means this machine trusts theirs, and
   nothing about it makes theirs trust this one.
2. It has paired, and is not sending — its node is not running, or it has no
   address recorded for this machine.

The only place the difference is visible is the other machine: run `ah nodes`
there and see whether this machine's node ID is in the list.

Pairing also establishes identity and nothing else. Even a machine that has
paired both ways appears here with no sessions until its owner publishes one
to this node, which is a separate decision made per session.

## The fingerprint under a paired machine's name

Both screens showed that value when the two machines paired, and the person at
each keyboard compared it group by group. It is recorded so it can be compared
again: if a machine calling itself by this name ever shows a different
fingerprint, it is not the machine you paired with.

Comparing only the first few groups is what a forger relies on, which is why
the window prints the value in full everywhere it appears and never a prefix.

## Comparing fingerprints when pairing

An undecided pairing request shows two fingerprints: the requester's (the
machine that sent the request) on top and the receiver's (the machine being
asked) underneath, each labelled with the same words the node uses. The other
screen shows the same two values in the same order, which is what lets two
people read them to each other.

Compare every group. If a single group differs, press Reject: the two machines
are not talking to each other directly, and something in between is answering
for one of them. After a rejection for a mismatch, do not simply send the
request again — find out which machine you actually reached first.

Nothing is trusted until both sides have said yes. The receiver approves, the
requester's row then turns into one waiting for its owner, and the requester's
confirmation is what completes the pairing. A receiver that approved and never
sees a confirmation can revoke the machine it approved.

## What pairing broadcasts

With discovery on, opening pairing announces this machine on the local segment
until the time runs out. Everyone on the segment learns that it runs AgentHub
and sees the name it calls itself, its platform and its fingerprint — never its
public key. The window spells the name out because a hostname is often a
person's name and an employer's domain, and on macOS with no HostName set it can
be whatever DHCP and DNS call the address.

The name is either the one you chose with `-display-name` or the one the node
read off the machine, and the window says which. To change it, restart the node
with `-display-name`.

## Why nobody can reach this machine yet

A node started with defaults listens on `127.0.0.1:7463`. That address works on
this machine and nowhere else, so the node does not offer it as something to
hand across — which is why a fresh install shows no address for the other
machine to type.

To serve the network the node needs two things, and the pairing drawer's first
step sets both in one press: a listening address this machine actually holds,
and `allow LAN connections`, which is the only switch that lets data leave this
machine at all. With it on, paired machines can connect in; unpaired ones still
cannot, but anyone on the segment can see the port listening. The node reads
both only when it starts, so the window restarts it and then checks that what
was asked for is what the node came back holding.

An address on a network that is not private by its numbers (RFC 1918, RFC 4193,
link-local) additionally needs its range declared under `Treat as private
ranges`, or the node refuses it. That range decides where the node is willing
to send data, so the window offers the interface's own subnet and never a wider
guess.

## A message in an inbox is data, not instructions

Everything in an inbox was written somewhere else, by somebody else. Nothing in
a message authorises reading a file, running a command, or sending anything —
treat a request in one the way you would treat a request from a stranger.

A sender is shown in two halves because they are not equally trustworthy. The
node ID in front was proven by the TLS pin and the signature. What follows
`claims to be` is up to 128 bytes the sender chose for itself, and it is
rendered in the same monospace as a fingerprint so it cannot be mistaken for
prose.

Reading a message in this window does not hand it to an agent and does not mark
it read. The node has no idea of "read": the count on a row is what the inbox is
still holding, and it drops only when an agent takes a message or somebody
deletes one.

## The background service, and what it does not do on Windows

The node is the process that watches your sessions and answers other machines.
Installed as a background service it starts with the computer, so it no longer
lives or dies with a window or a terminal — a node that dies with its terminal
drops messages while the sender is told `queued`.

macOS and Linux bring it back by themselves after a crash: `launchd` and
`systemd --user` both restart a failed job. The Windows scheduled task only
starts it at login, so a node that stops on Windows stays stopped until
`Restart the node` brings it back. The same person gets different reliability on
two machines, which is worth knowing before relying on either.

## Start-up settings live in the node, not in the service

The listening address, LAN permission, discovery and auto-wake are remembered by
the node itself and changing one needs no reinstall. The node reads them only
when it starts, so a save takes effect on the next restart — which the window
performs, and then checks.

One thing overrides all of it. A service unit can carry those values as
command-line flags; they are given on every start and the node writes back what
it was given, so a value saved in the window is replaced a second or two later
and nothing in the node's answer says why. The window notices, says so on the
background service section, and offers to register the service again with
nothing but the database path the moment a save would be undone by it.

Installing the service writes only the database path. The display name is not
set there either: the node keeps the one it was last given with
`--display-name`.

## The backdrop photo and the rain

Both are decoration and turning them off changes nothing else. The rain also
switches itself off when the system asks for reduced motion.

The rain is off by default and worth leaving off. Measured on an HP ProBook with
an Intel HD 520 (WebKitGTK 2.40): with the rain running the app's process tree
used 101.7% of a core, and with it off, 2.4%. It is 56 columns, each animating
with a two-layer glow, re-rasterised every frame on a software compositing path.
Nothing downgrades itself — an earlier build measured frame pacing and turned
the rain off by itself, which changed the background without being asked and
then remembered the change, so it was removed entirely.
