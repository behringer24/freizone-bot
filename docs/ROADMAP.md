# Roadmap — freizone-bot

Planned changes whose **essential** work lands in this repo (the automation
daemon that holds a Freizone account and connects Freizone to other systems in
both directions). Cross-repo and protocol-level items live in freizone-server's
`docs/ROADMAP.md` (core).

Each item has a short **reference code**; the prefix names the owning repo:

- `SRV-` — freizone-server (core)
- `APP-` — freizone-app
- `GAW-` — freizone-gateway
- `BOT-` — freizone-bot (this file)

A change spanning several repos is listed **once**, in the repo where the
essential work happens; its entry names the other repos it touches.

Status values: `planned` · `in progress` · `done` · `deferred`.

## How to read this file

Entries are short: what the item is, its status, and a dated log of what
happened. The reasoning — why an approach was chosen, what was rejected, which
trade-offs were accepted — lives in a per-topic document under
[`design/`](design/), linked from the entry, as in freizone-server and
freizone-app.

## Items

### BOT-01 — The alerting daemon
Status: `in progress` · Depends on: SRV-30 · Also affects: freizone-server
Design: [design/01-automation-daemon.md](design/01-automation-daemon.md)

The first capability, and deliberately larger than a minimal phase: a bot that
registers itself and can then do nothing is not a product. Everything needed to
page somebody from a shell — configuration, the account lifecycle including
self-registration, the receive loop and its maintenance, the local control
socket, `freizone-bot send`, a durable outbox, a group route, a crude rate cap,
the container and its workflow.

Acceptance: a `systemd OnFailure=` unit pipes into `freizone-bot send`, and the
message arrives in the operator's Freizone group with the bot as a member.

- 2026-08-17 — repo created, skeleton in place

### BOT-02 — One-to-one routes
Status: `planned`

Peer destinations alongside the group route: address resolution cached at
startup rather than per message, `StartConversation` on first contact, and one
outbox entry per (message × destination) so a retry after a partial success
cannot page the recipients who already have it. The outbox is designed for this
in BOT-01, so this is an addition rather than a rework.

### BOT-03 — Message shaping
Status: `planned`

Deduplication keys, collapsing a storm into one line with a count, pairing a
`resolved` notice with the `firing` one it answers, and severity deciding which
route a message takes. This is what makes the bot usable during a real incident
rather than during a demonstration.

### BOT-04 — Standalone one-shot
Status: `planned`

`send --standalone` for a send-only host that runs no daemon, taking the same
account lock so the safety is structural rather than conventional. Must warn
when the inbox is filling and top up prekeys on a timer, since neither happens
without a daemon.

### BOT-05 — Command surface, deterministic
Status: `planned`

Incoming Freizone messages as commands: an allow-list of who may command the
bot, a deterministic interpreter, an action registry, and a read-only action
set (`help`, `status`, `ping`, `mute`). This is where the interpreter seam is
built *and used* — by a parser, which is the only way to find out whether the
seam is honest before an LLM depends on it.

### BOT-06 — Spool fallback
Status: `planned`

`send` writes to a spool directory when the daemon is unreachable, and the
daemon drains it at startup. Closes the "page lost while the service was
restarting" gap — deliberately not by letting the CLI start a daemon.

### BOT-07 — Windows named pipe
Status: `planned`

The transport seam exists from BOT-01; only the implementation is deferred.
Needs an explicit descriptor denying remote access, since a named pipe is
reachable over SMB by default if the descriptor permits it.

### BOT-08 — Webhook receiver
Status: `planned`

Alertmanager's native integration, and **the first network listener this bot
has ever had** — which is why it is its own item with its own authentication
design, its own TLS question and its own abuse handling. Reuses the routes and
the outbox unchanged: the webhook becomes a second source for the same queue.

### BOT-09 — Server-assistant role
Status: `planned` · Also affects: freizone-server

Reporting to a Freizone operator what their own server is doing — the figures
`SRV-25` already computes, plus seat and quota warnings. Needs admin
credentials, so it belongs in a **separate instance**: its own account, state
directory, user and allow-list. The alerting bot has an ingress path from every
CI job on the machine; the assistant must not be reachable from there.

### BOT-10 — Interpretation by an LLM
Status: `planned`

Behind the interpreter interface BOT-05 establishes, with tool definitions
generated from the same action specs the deterministic interpreter reads. One
constructor call changes. The model never sees message bodies from third
parties, and its output can only ever name an action that already exists.

### BOT-11 — Transcript retention
Status: `planned`

An alerting bot's transcript grows without bound with content nobody will read.
`ClearTranscript` already exists in `pkg/client`; the retention policy belongs
here. Filed early because it is the kind of thing noticed when a disk fills.
