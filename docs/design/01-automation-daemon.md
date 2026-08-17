# Design: The automation daemon

Status: **in progress** · Roadmap: [BOT-01](../ROADMAP.md) · Also affects:
freizone-server (SRV-30)

freizone-bot holds a Freizone account, stays connected, and connects Freizone to
other systems in **both directions**: outbound, where something happens
elsewhere and becomes a message; and inbound, where a message causes something
to happen.

Ops alerting is the first capability rather than the shape of the whole thing.
It was chosen to go first because it forces the entire outbound path into
existence — account lifecycle, routing, durable delivery, retry — and because it
is immediately useful: it makes Freizone a secure alerting channel for people
who today hand every alert to Slack or Telegram.

## What belongs here and what does not

SRV-23 drew this line and it is the reason this repo exists separately:

> Command parsing, AI integration, IoT protocol adapters, webhooks, bot
> configuration — all of that is freizone-bot's. The core knows the Freizone
> protocol: nothing above it, nothing below it.

So nothing here decrypts, manages a session, tracks a processed-message id, or
re-derives a rule like "409 means delivered". Where the bot needs something the
core lacks, that is an `SRV-` item, not a local workaround. Ending exactly this
kind of second implementation is what SRV-23 was for; re-creating it one layer
up would be the same mistake wearing a different name.

The bot's own surface therefore begins at `Content.Text` — after `pkg/client`
has decrypted, deduplicated and classified.

## The shape

```
Sources                                   Routes
  control socket / CLI    ── BOT-01 ─┐            ┌── group:<id>
  schedule                ── later ──┤   router   ├── peers:<a,b,c>
  webhook                 ── BOT-08 ─┤  + outbox  │
  server events           ── BOT-09 ─┘            └── (broadcast, if SRV-16)
                                          │
                                          ▼
                                    pkg/client send

incoming Freizone message
  → authorization → interpretation → action registry → reply
```

The load-bearing idea is **named routes** rather than "the alert group". A
source addresses a route; several capabilities share one delivery-and-retry
path. When the webhook lands it becomes a second producer for the same outbox,
which is why the outbox is a package rather than a slice in `main`.

## One process owns an account directory

`pkg/client` has no lock of any kind, and two `Client` values over one directory
is not "racy under load" — it is destructive on the first concurrent send:

- `peers/<id>/session.json` is a whole-file write of an advancing ratchet. Two
  writers lose one advance, and the peer sees two envelopes claiming the same
  message number — a desync that heals only by re-keying, after the messages
  are gone.
- `processedIDs` lives in memory. Two processes each hold half the truth, so a
  redelivered envelope gets processed twice by whichever one did not see it.
- `ConsumeOneTimePrekey` in one process leaves the other believing the key is
  unspent.
- `Open` **itself** mutates: it settles in-flight sends to failed. So a CLI
  merely *opening* the directory while the daemon has a send in flight marks
  that live send as failed.
- `peerLocks` is an in-process mutex. It offers no cross-process protection
  while looking, at the call site, exactly as though it does.

Hence: **the daemon owns the directory; the CLI never opens it** and talks to
the daemon instead. SRV-30 adds the lock to the core as well, so the rule is
enforced by the kernel rather than by everyone remembering it.

The lock, not the socket, is the mutual-exclusion primitive. That ordering
matters in one concrete place: a leftover socket file may only be removed
*after* the lock is held. Without the lock, "stale" and "a daemon is running"
are indistinguishable, and unlinking would steal a live daemon's socket.

## The control channel

A local socket, addressed by config and defaulting inside the state directory —
so a CLI invocation that inherits the daemon's environment needs no extra
configuration, and in a container the data volume is the one thing certainly
shared.

**The parent directory is the gate, not the socket's mode.** `bind(2)` applies
the umask on some platforms, and between `Listen` and a later `Chmod` there is a
real window. So the directory is `0750`, owned by the daemon's user and an
operator group; the socket gets `0660` as well, as belt and braces; and traversal
is denied at the directory before the socket's mode is ever consulted.

The wire format is newline-delimited JSON: one request, one response, close. It
carries the daemon's version in every reply, because a package upgrade replaces
the binary on disk while the running daemon keeps its old code — the CLI and the
daemon are the same binary, but not necessarily the same build.

### Two rejected alternatives, with their reasons

**HTTP over the unix socket.** Genuinely tempting: it would reuse freizone-
gateway's `internal/api` idiom directly. Rejected because it turns "no network
listener" from a property of the *code* into a property of the *configuration*.
Once the handlers are `http.Handler`s, exposing them on a port is a two-line
change, and the webhook decision — which deserves its own item, its own
authentication design and its own TLS story — gets taken by accident instead.

**The CLI starting a daemon when none is running.** Sounds friendly; forks a
long-lived, key-holding process into whatever context a cron job happened to
have: wrong user, no supervision, environment from a crontab, inherited file
descriptors. Several concurrent jobs then race for the lock — the account stays
safe, but the operator gets flapping instead of a service. A daemon is a
service, and a service manager starts it. The gap this leaves (a page lost
while the unit restarts) is closed by BOT-06's spool directory, which is a
different mechanism without any of these problems.

## Acknowledging at the right moment

`send` returns once the message is **durably in the outbox**, not once it is
delivered. That is the honest acknowledgement point for an alerting tool: a cron
job wants a fast exit code, but `exit 0` must never mean "accepted into a buffer
that a restart discards". Delivery is asynchronous and retried; `--wait` exists
for a caller willing to block for confirmation.

An outbox entry is per **(message × destination)**, not per message. One entry
covering five recipients would, on a retry after three succeeded, page those
three again. This is the rule `pkg/client`'s group fan-out already learned —
a ratchet advance is not rolled back in a fan-out, because partial success means
some peers moved on — applied one layer up, where the bot owns a fan-out across
independent conversations.

## Registration is not idempotent

The failure that happens once in production and is then very hard to diagnose: a
crash between "the server created the account" and "the identity was written to
disk" leaves an orphaned account on the server, and the bot registers a *second*
one on restart. It consumes a second invite code and comes back with a
**different address** from the one the operator already invited to the group.

So the keys are generated and written **before** the request, with a marker
recording that a registration is in flight. On a start that finds the marker,
the account id is derived from the stored root key and looked up: present means
the registration did land, so clear the marker; absent means retry with the
*same* keys.

The one fact the operator must act on is the bot's address, so registration
makes it impossible to miss — logged prominently, written as a plain file, and
retrievable later.

## Maintenance, and why a bot needs a timer where the app does not

Four calls have to run periodically: topping up one-time prekeys, settling group
snapshot debts, recovering desynced sessions, and resending pending receipts.
`pkg/client` performs none of them on its own and documents no cadence.

They run on every `StreamConnected`, which is the app's rule and a good one — a
fresh connection is when whatever broke the last attempt has most likely passed.
**Plus a six-hour floor, which is this bot's own addition.** A phone reconnects
constantly: screen off, network handover, app resume. A server bot's stream
stays up for weeks, and then none of the four would ever run again — the prekey
pool would drain toward zero with nothing to notice, and a snapshot debt from
hour two would still be owed in week three. A minimum spacing of five minutes
keeps a flapping network from turning twenty reconnects into twenty maintenance
runs against an already-struggling server.

Two rules in the receive loop are easy to get wrong and are therefore stated at
the call site as well as here:

- **Acknowledge a decrypt failure once the core says it gave up.** A decrypt
  failure is deterministic, so a poison envelope blocks everything behind it
  forever unless it is acknowledged away.
- **`HandleIncoming` performs no network I/O, by design.** The bot fetches, the
  bot acknowledges. No convenience wrapper may hide that.

## Shutdown order is not the reverse of startup

Close the control socket **first**, so nothing new is accepted; then let the
outbox flush, bounded; then the stream and maintenance; and release the account
lock **last**, so nothing can take the directory mid-flush. Accepting a message
after deciding to shut down would mean acknowledging something that will only be
delivered after the restart — the one thing an alerting tool must not do
quietly.

## The interpreter seam

Interpretation produces a **value, never an effect**. The interpreter is handed
the message text, its sender, its chat and a clock, and returns the name of an
action plus parameters. It holds no client, no registry, no configuration and no
connection: it cannot do anything, only describe something.

That is what makes the seam real rather than aspirational, and it is why the
deterministic interpreter is built from the same action specifications an LLM
would later render as tool definitions. If the deterministic one needed
information those specs do not carry, the specs would be an insufficient basis
for tool definitions too — and that surfaces now rather than at the point where
a model depends on it.

**Authorization runs before interpretation**, permanently and deliberately. Not
"interpret, then refuse": a stranger's text must never reach the interpreter at
all, or a later LLM inherits a prompt-injection surface open to anyone who knows
the bot's address. This is the single most important invariant in the repo.

## Security

- **No network listener in v1.** Stated in the README as a property so that
  losing it becomes a visible decision rather than a drift.
- **Unattended private keys.** The identity is on disk in the clear, because
  there is nobody to type a passphrase at three in the morning. The perimeter is
  therefore the filesystem and the process: a `0700` state directory, a refusal
  to start when it is group- or world-readable, a dedicated user, and a hardened
  service unit.
- **The blast radius, stated plainly.** The bot is a full group member.
  Compromising the host means being able to impersonate it *and to read that
  group's future traffic*. So the standing recommendation is a group that exists
  only for alerts, containing only people who accept that an ops box sits inside
  that group's trust boundary. An alerting bot in the team's general chat
  silently extends that chat's confidentiality to a machine every CI job can
  write to.
- **Incoming messages are untrusted input from anyone** who knows the address —
  and every group member has it. The allow-list is configuration, never learned
  at runtime and explicitly not "whoever is in the group", because membership
  drifts without the operator being told. An empty allow-list disables the
  command surface entirely, and a sender who is not on it gets no reply at all:
  a refusal would confirm the bot exists and is listening.
- **The control socket is an authentication boundary.** Whoever can write to it
  can make the bot say "false alarm, all clear" into the alert group during a
  real incident. Suppression is as damaging here as spam.
- **Message bodies are never logged.** They routinely carry hostnames, internal
  addresses and stack traces, and they land permanently in every recipient's
  transcript. Title and severity are enough for an operational log.
