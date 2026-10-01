<img src="docs/icon.png" width="112" align="right" alt="">

# AgentHub

English | [繁體中文](README.zh-Hant.md)

**One window for every Claude Code and Codex session on all of your machines.**

AgentHub lists the coding-agent sessions on each of your computers, marks each
one active, idle or gone quiet, and lets an agent on one machine send a message
to an agent on another. It all stays on your own network: no account, no cloud
server, no telemetry. Free and open source under the MIT license.

![The Local sessions table: nine Claude Code and Codex sessions with their status, who can see each one, and inbox counts](docs/screenshots/local-sessions.png)
*Every session on this machine, with its status, who can see it, and how many messages are waiting.*

The window is in English and 繁體中文. The pictures use made-up data.

## Get started

One computer is enough to try it. To connect two, install AgentHub on both
first: step 2 of the setup needs both computers open at that step at the same
time.

### 1. Install

**macOS and Linux:** paste this into a terminal.

```sh
curl -fsSL https://raw.githubusercontent.com/SheldonChangL/agenthub/main/install.sh | sh
```

It checks the download against the release's checksum, installs the app and the
`ah` command, and starts AgentHub in the background. It also puts a Claude Code
skill in `~/.claude/skills/agenthub-watch`, so agents on this machine know how
to use `ah`; put `sh -s -- --no-skill` in place of the last `sh` to leave it
out. It never asks for your password.

**Windows:** download `agenthub-desktop_<version>_windows_amd64-installer.exe`
from the [Releases page](https://github.com/SheldonChangL/agenthub/releases)
and run it. AgentHub has never been run on a real Windows computer; the Windows
build is only built and tested in CI
([#21](https://github.com/SheldonChangL/agenthub/issues/21)).

The files are not signed, so a manual download makes macOS and Windows warn you
before the first launch. The guide explains
[how to let it through and how to check the file](docs/guide.md#if-your-computer-warns-about-the-download).

### 2. Open the app

- **macOS:** the install opens it when it finishes. Later, open
  agenthub-desktop from Applications.
- **Linux:** AgentHub in your applications menu, or `agenthub-desktop` in a new
  terminal.
- **Windows:** agenthub-desktop in the Start menu.

### 3. Follow the setup, on both computers

![First-run setup, step 1: three checks, a box saying what opening the network and searching it will show, and the "Get this machine ready" and "Use it on this machine only" buttons](docs/screenshots/first-run.png)
*A fresh install opens on a three-step setup. Nothing is shared until you choose it.*

1. **Get this machine ready.** One button starts AgentHub in the background,
   makes it start when you log in, lets other computers on your network reach
   it, and turns on searching the network. The box above the button says what
   that shows: while pairing is open, other computers on the network can see
   this one's name and address (and its platform, fingerprint and node ID).
   Prefer to keep it on one computer? Press **Use it on this machine only**.
2. **Connect another.** The other computer appears in the list once *both*
   have finished step 1. If the list says this machine is not looking, press
   **Start searching the network**. Still nothing? Open **Can't find the other
   machine?** on both and type the address one shows into the other. Send the
   request from one computer. The one that was asked compares the two codes
   with the other screen and presses **Same — approve**; then the one that
   asked compares them too and presses **Same — finish pairing**.
3. **Share sessions.** Tick the sessions the other computer may see, and choose
   whether it can only leave messages or also wake the agent.

If you approved and the other person never finished, your computer still trusts
theirs, and that does not expire. Remove it with **Revoke trust** on the
Network tab. The [user guide](docs/guide.md) covers every screen.

## What you can do

- **See every session and its status.** Claude Code and Codex, on a Mac, a
  Linux box or a Windows PC (the Windows build exists, but has never run on a
  real Windows computer), in one list. Each session shows `active`, `idle`,
  `inactive` or `unknown`, read from the agent's own files without touching it.
- **Share a session, one row at a time or many at once.** Each session's
  audience button has three choices: **Not published**, **Can leave messages**,
  or **Messages and waking**. A chosen-machines dialog handles the rest.
  [Share a session](docs/guide.md#share-a-session).
- **Read the inbox.** Messages from other machines wait in AgentHub's own inbox
  for each session. [Inbox and messages](docs/guide.md#inbox-and-messages).
- **Let agents talk to each other.** Four MCP tools (`agent_list`,
  `agent_status`, `agent_inbox`, `agent_send`) let an agent ask what an agent on
  another machine is doing and write to it.
  [Set them up](docs/guide.md#let-an-agent-use-the-four-tools).
- **Wake an agent with a message.** With two switches on, a message can start a
  turn in a Codex session with nobody at the keyboard. This has been seen
  working on one machine only, and it does not work for Claude Code yet.
  [Waking an agent](docs/guide.md#waking-an-agent).

![The audience menu on one row: Not published, Can leave messages, and Messages and waking (unavailable for a Claude Code session)](docs/screenshots/inline-publish.png)
*Sharing is a menu on each row, and it applies at once with an Undo.*

## Privacy in plain words

- **Nothing goes to the cloud.** Your computers talk to each other directly
  over your own network. There is no AgentHub server, no account to create and
  no telemetry.
- **Nothing is shared until you share it.** Every session starts as **Not
  published**. Pairing two computers shares nothing by itself.
- **Pairing needs both people.** Both screens show the same two fingerprints,
  short codes made from each computer's key. You compare them and each person
  approves on their own screen. If one group differs, you reject it.
- **A paired computer sees only a short description:** each shared session's
  ID, Claude Code or Codex, its status and where that came from, whether
  AgentHub manages it, when it was last active, and the working directory only
  if you allow it. Never your prompts or transcripts.
- **Searching the network mostly listens.** With searching on, this computer
  always listens for other computers' announcements, and announces itself only
  while a pairing window is open. While the setup sits at step 2, that window
  reopens by itself when it runs out.
- **Messages stay in AgentHub's own inbox.** Nothing is written into Claude
  Code's or Codex's files. A message reaches an agent only when the agent reads
  it, or when you turned on waking.

![Pairing: two fingerprints to compare against the other machine's screen, with "Same — approve" and "Different — reject"](docs/screenshots/network-pairing.png)
*Two machines pair only after both people compare the same codes on both screens.*

Details and design records: [developer guide](docs/developer.md#how-it-stays-private).

## Status

One person built this and uses it daily on a Mac and an Ubuntu box; nobody else
has used it yet. What was tested on two real machines is in
[docs/verification.md](docs/verification.md). The gaps below are real, including
one where a feature reports success and may have done nothing.

| What | State |
|---|---|
| Session list, status, sharing | Verified on macOS and Linux |
| Pairing with fingerprint comparison | Verified between two real machines, from the command line |
| Heartbeats (the signed update each machine sends its paired machines every 15 seconds), messages, inbox | Verified between two real machines |
| First-run setup (new in v0.1.9) | Checked in the dev mock and in tests. Searching the network was checked on two real machines with test nodes (2026-10-01). The whole setup in the app has not yet been run from start to finish on two real machines. |
| MCP tools, agent to agent across machines | Verified between two real machines |
| Waking a **Codex** session | Verified on one machine: a turn was observed |
| Waking a **Claude Code** session | **Unverified.** The node reports the message as woken, but no turn has been observed. [Details](docs/channel-push-not-observed.md) |
| Windows | **Never run on a real Windows computer**; built and tested in CI only. The background node starts at log-in only and is not restarted if it exits |
| Acceptance on real Windows, macOS and Ubuntu machines | Open: [#21](https://github.com/SheldonChangL/agenthub/issues/21) |
| Signed downloads | Not done, and not planned until this is worth a certificate |

## FAQ

**Does it send anything to the internet?** No. AgentHub talks only to
computers on your local network, normally just the ones you paired with. While
a pairing window is open, anyone on the same network can see this computer's
name and address. It reaches GitHub only when you run the install script.

**Why does macOS or Windows warn me?** The downloads are not signed, because a
signing certificate costs money every year. Each release ships checksums you
can compare against. The one-line install on macOS checks them for you, so it
shows no warning. [How to let it through](docs/guide.md#if-your-computer-warns-about-the-download).

**The other computer does not show up. What now?** Both have to finish step 1,
which turns on searching, and be on the same network: if only one searches,
neither sees the other. If step 2 says this machine is not looking, press
**Start searching the network**. A firewall or a guest Wi-Fi can still hide
them; then open **Can't find the other machine?** on both and type the address
one shows into the other. [Troubleshooting](docs/guide.md#troubleshooting).

**What can the other computer see after pairing?** Nothing, until you share a
session. After that it sees the short description above, never the
conversation.

**How do I remove it?** Run the install command with `sh -s -- --uninstall` in
place of the last `sh`. Your pairings survive a reinstall unless you add
`--purge`. On Windows, uninstall it from Settings → Apps.
[Upgrade and remove](docs/guide.md#upgrade-and-remove).

**Can a message wake Claude Code?** Not yet. The message arrives in the inbox,
and the agent can read it with `agent_inbox`, but waking a Claude Code session
has never been seen working. Waking Codex has been seen working on one machine.

## More

- User guide: [English](docs/guide.md) · [繁體中文](docs/guide.zh-Hant.md)
- [Developer guide](docs/developer.md): building, the command line, the local
  API, the privacy model in detail
- [SECURITY.md](SECURITY.md): how to report a vulnerability
- [CONTRIBUTING.md](CONTRIBUTING.md): how to help

## Support

AgentHub is free and always will be. If it saves you time and you want to say
so, there is a [Ko-fi page](https://ko-fi.com/sheldonchang). Nothing in the
software changes either way: no paid tier, no telemetry that would notice. Bug
reports, with a note about what you were trying to do, are worth more than money.

License: MIT — see [LICENSE](LICENSE).
