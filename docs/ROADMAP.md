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
Status: `done` · Depends on: SRV-30 · Also affects: freizone-server
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
- 2026-08-20 — **the group route could not work, and had been documented as
  though it did.** A group membership is not real until the invited account
  sends `join_accept`, and nothing in this bot ever sent one -- so a configured
  `FREIZONE_BOT_ROUTE_GROUP` left the bot invited for ever, sending into a group
  it was not a member of. Found by Andreas asking whether the bot can be invited
  to a group, which is the kind of question that only gets asked because the
  README claims something. I had tested the peer route and let the documentation
  stand in for verifying the other one.

  Fixed, and verified with a real group over a real server: the founder's member
  list goes from "invited, not accepted" to a full member, and a message from
  the bot arrives in the group rendered as intended. An invitation to any *other*
  group is left unanswered unless `FREIZONE_BOT_ACCEPT_GROUP_INVITES` says
  otherwise -- accepting freely means anyone who knows the address decides what
  this bot is a member of. Never declined either: declining is a signed fact
  that says something, and the honest state is that nobody asked.

  **A second gap of the same family came with it.** The command dispatch hung
  off the live stream alone, so anything arriving while the bot was down was
  stored and never answered -- and an invitation in that window never seen.
  Both paths now go through one `onReceived`, which is the same discipline
  SRV-30 applied to envelope handling, one layer up where the bot's own
  follow-up lives. Worth noticing that this is the third time in this project
  that a live path and a catch-up path drifted; it seems to be the default
  outcome of having two, unless one function owns both.

- 2026-08-20 — **and a third, found by Andreas asking how the join actually
  works.** An invitation is announced exactly *once*: the receive path reports
  it when the facts are new to the device and never again. So an invitation that
  arrived while no group was configured was folded, ignored, and never mentioned
  by anything afterwards -- configuring the group later and restarting did
  nothing at all.

  That is precisely the order the first run leads an operator into, since it
  prints "invite that address to the group it should post in" *before* anybody
  has looked up a group id. So the documented order was the only one that
  worked, and it was the less likely one.

  Fixed by reading the facts already held at startup rather than waiting to be
  told again, and verified in that order against a real server: invited with no
  group configured (ignored), then configured and restarted -- the bot finishes
  the invitation and the founder sees a full member. Four tests cover the
  decisions around it, including the two that must *not* join: already a member,
  and holding a group's facts after having been removed from it.

- 2026-08-20 — the receiving half works, verified against a real server: the
  daemon registers itself on first start, prints the one fact an operator must
  act on (its address, on stderr as well as in the log, plus `<state>/address`
  and `freizone-bot whoami`), holds the stream up, drains the queue and runs
  the periodic upkeep. `whoami` reads the address file rather than opening the
  account, so it answers while the daemon holds it.

  A daemon with no route **refuses to start** rather than warning: one that
  accepts messages with nowhere to put them is worse than one that is plainly
  not configured. Registering with no route is still a legitimate first run,
  which is why the address is printed before that refusal.

  Two things the real run turned up. The stream loop logged only *failures*, so
  a handled message produced no line at all — an operator could not tell a live
  stream from one that was merely connected; it now says so at debug, without
  the text, which is somebody's private message and already in the transcript.
  And the account-in-use refusal read its own fact twice and leaked
  `pkg/client`'s package prefix into a message a person reads, the same leak
  freizone-app fixed on 2026-08-16 — the CLI now strips it once, at the top.

  Also seen working in the wild rather than in a test: a confirmation that had
  never got out was re-sent on the next connect (`receipts_resent: 1`), which
  is exactly what the upkeep exists for.

  Still open in this item: the control socket, the routes and
  `freizone-bot send` — everything on the *sending* side.
- 2026-08-20 — **done.** The sending half: the control socket (`internal/ipc`,
  `internal/control`), named routes (`internal/outbound`), the durable queue
  (`internal/outbox`), the crude rate cap, and `freizone-bot send` / `status`.
  Driven end to end against a real server rather than only in tests: two
  messages delivered and read back through `devclient` in the shape they were
  meant to have, all five documented exit codes produced on demand, a failed
  delivery retried on its backoff and still in the queue after a daemon
  restart, and the rate cap suppressing seven of twenty-five and reporting
  exactly that count on the next message through — which the recipient saw.

  Newline-delimited JSON over the socket, not HTTP. Tempting, since it would
  reuse freizone-gateway's whole api idiom — and rejected because it turns "this
  bot opens no network listener" from a property of the *code* into a property
  of the *configuration*. Once the handlers are `http.Handler`s, a port is two
  lines away, and BOT-08's decision gets taken by accident instead of on
  purpose.

  One entry in the queue per **message × destination**, not per message: one
  entry covering three recipients would, on a retry after two succeeded, page
  those two again. Same rule `pkg/client`'s group fan-out already arrived at,
  one layer up.

  Shutdown order is deliberately not the reverse of startup — the control
  socket closes *first*, so nothing can be told "safely queued" about a message
  that will only go out after the restart.

  **The real run found a bug the tests could not.** `send` read standard input
  whenever it was not a terminal, which reads as harmless: a pipe has an end.
  Under a service manager, a CI runner or cron it does not — stdin is routinely
  an open pipe nobody writes to and nobody closes, and `send "disk full"` hung
  forever waiting for an EOF that was not coming. An alerting tool blocking at
  the moment it is needed is the worst failure it has available. The rule is
  positional now: an argument means the text is there and stdin is never
  touched; nothing there means it comes from stdin, which is the `cmd | send`
  case where the producer does close it. The convenience of accepting both at
  once was not worth a hang.

  Also fixed on the way: the suppression note read "1 further message **were**
  suppressed", and the test that should have caught it was written to accept
  either form -- which is to say it pinned nothing. Both corrected.

### BOT-02 — One-to-one routes
Status: `done` · Part of: BOT-01

Peer destinations alongside the group route: `StartConversation` on first
contact, and one outbox entry per (message × destination) so a retry after a
partial success cannot page the recipients who already have it.

- 2026-08-20 — shipped inside BOT-01 rather than after it. Both routes were
  needed at once because they are independent rather than alternatives -- with
  both configured a message goes to both, which is the whole point: the team
  channel *and* whoever is carrying the pager. Splitting them across two items
  would have meant building the outbox twice.

  One piece of this entry turned out not to be work: caching the peer's address
  rather than resolving it per message is already `pkg/client`'s behaviour, since
  `Endpoint` answers from the cached peer device before it resolves anything. So
  there was nothing to add here, only something not to re-implement.

### BOT-03 — Message shaping
Status: `in progress`

Deduplication keys, collapsing a storm into one line with a count, pairing a
`resolved` notice with the `firing` one it answers, and severity deciding which
route a message takes. This is what makes the bot usable during a real incident
rather than during a demonstration.

- 2026-08-20 — two of the three shipped: **severity routing** and
  **deduplication**. Verified against a real server, with the recipient's own
  transcript read back: three identical alerts thirty seconds apart arrived
  once, an unrelated alert in between arrived, and the severity map sent
  `critical` to the pager while refusing a `warning` mapped to a route that was
  not configured.

  Three rules that could each have gone the other way. **An unmapped severity
  goes everywhere**, because silently dropping one is the worst possible
  reading of a partial configuration. **An explicit `-route` beats the map**,
  since a person typing it is doing something out of the ordinary on purpose
  and configuration overriding that would make the flag a suggestion. And
  **the body is not part of what makes two messages the same** -- it routinely
  carries a timestamp or a load average that differs every time while the alert
  plainly does not, so hashing it would make the feature do nothing in exactly
  the cases it exists for.

  Deduplication runs *before* the rate cap. They answer different questions --
  "is this the same alert again" and "is too much leaving at once" -- and a
  repeat the deduper would swallow should not spend a slot in the rate window
  on its way to being dropped, or one flapping check exhausts the budget for
  everything else.

  Off by default, deliberately: deciding two alerts are one incident is a
  judgement the monitoring system upstream usually makes better, having the
  labels and the operator's own rules. This is for when it does not -- a shell
  script, a cron job, a check with none of that.

- 2026-08-20 — **and then the whole shape of a message changed, because the
  first version of the above had quietly turned this into an alerting tool
  again.** `Message` had `Severity` and `Source` as fields of its own, the
  renderer hard-coded `[CRITICAL]`, routing read severity and nothing else, and
  deduplication keyed on the three of them. Every later capability -- a build
  result, a scheduled digest, an answer to a command, a reading from a device --
  would have had to bend itself into alerting vocabulary or grow a second set of
  fields beside it.

  A message now carries a title, a body and **labels**. `severity` and `source`
  are conventions the renderer gives prominence to rather than special cases:
  `-severity` and `-source` are shorthands for `-label`, and nothing breaks when
  neither is present. Routing rules match any label
  (`kind:digest=group,severity:critical=peers`), deduplication keys on the title
  and the labels, and a message with no labels at all renders as plain text --
  which is what a joke of the day or a command's answer looks like.

  Verified with three messages down one pipeline: a one-liner with no labels, a
  CI result labelled `repo`/`branch`/`kind`, and an ordinary alert. All three
  arrived rendered as intended, and the alerting decoration appears only
  *because* the labels are there.

  Worth recording that this was the second time the drift happened: the plan had
  already been rewritten to say "alerting is the first capability, not the shape
  of the whole thing", and the implementation went straight back to fields
  anyway. Labels were the fix both times.

- **Open** — the third piece, pairing a `resolved` notice with the `firing` one
  it answers. Held back because it is the first thing that would make this bot
  hold state about *the world* rather than about its own recent output, and that
  brings questions the other two did not: what an open alert means across a
  restart, what to do with a `resolved` whose `firing` was never seen, and how
  long an alert stays open before it is assumed over. Worth deciding before
  building rather than discovering afterwards.

### BOT-04 — Standalone one-shot
Status: `planned`

`send --standalone` for a send-only host that runs no daemon, taking the same
account lock so the safety is structural rather than conventional. Must warn
when the inbox is filling and top up prekeys on a timer, since neither happens
without a daemon.

### BOT-05 — Command surface, deterministic
Status: `done`

Incoming Freizone messages as commands: an allow-list of who may command the
bot, a deterministic interpreter, an action registry, and a read-only action
set. This is where the interpreter seam is built *and used* — by a parser,
which is the only way to find out whether the seam is honest before an LLM
depends on it.

- 2026-08-20 — shipped: `internal/authz`, `internal/command`,
  `internal/action`, and the dispatch in the daemon. Actions are `help`,
  `ping`, `status` and `joke` — all read-only, none touching anything outside
  the process. Verified against a real server: an allow-listed sender got all
  four answers back in the chat they asked from, and an unlisted one got
  complete silence with the attempt recorded at debug.

  **The ordering is the point, and it now has a test.** Authorization runs
  *before* interpretation, so a sender who may not command the bot has their
  text never reach the interpreter at all -- not "reach it and be refused
  afterwards". Today the interpreter is a parser and the difference is
  invisible; the moment a model sits there, an interpreter that sees everything
  is one that anyone knowing the address can write prompts for. That invariant
  lived in `dispatch` with no test over it until this item, which was worth
  fixing on its own: `cmd/bot` had no tests at all, and the single most
  important rule in the repository was resting on a comment. Negative-controlled
  by moving the check after the interpreter, which fails the test.

  Fail-closed throughout: no commanders means no command surface, group
  commands are off unless switched on, and an unlisted sender gets silence
  rather than a refusal -- a refusal is an oracle confirming something is here
  and listening, and an amplification vector besides.

  The seam is honest rather than aspirational because the parser is built from
  the same `action.Spec` values a model-driven interpreter would render as tool
  definitions. If the parser had needed something they do not carry, tool
  definitions would be missing it too, and that surfaces now rather than at
  BOT-10. What the interpreter cannot reach is what makes it safe: it is handed
  four strings and a clock, and returns a name and more strings. No client, no
  registry, no configuration, no connection.

  `/joke` is deliberately in the shipped set. It is the cheapest proof that
  this command path carries something which is not an operations alert, and it
  is a joke *of the day* rather than a random one because "of the day" is a
  promise -- asking twice in an afternoon should not give two answers.

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
