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

- 2026-08-20 — **a peer on another server could not be configured at all**, and
  the README said it could. `Deliver` passed an empty server to
  `StartConversation`, which means "mine", so every recipient was looked up on
  the bot's own server. In a product whose premise is that servers federate,
  the peer route reached one server only. Nothing failed loudly: an account that
  lives elsewhere is simply not there, and the result reads like a deleted
  account.

  The cause was that **nothing in Go parses the `id*server` form**.
  `pkg/address.Normalize` handles the id half -- separators, checksum -- and
  knows nothing about a server; only freizone-app's Dart side ever split on the
  star. So the composite address, which is the form a person actually copies out
  of the app, existed in the protocol and in one client and nowhere else.

  Fixed with `internal/config/address.go`: `ParsePeer` / `ParsePeers` /
  `ParseGroupID`, and `outbound.Destination` carrying a `Server`. Parsing sits in
  `config` rather than `outbound` so a bad recipient fails when the
  configuration is read instead of at the first message -- the worst possible
  moment to discover that a destination was never addressable. Four things are
  refused there by name: a truncated id (strict, unlike `ResolvePeer`, which
  completes a prefix because a person is typing), a group id in the peer route or
  an account id in the group route (they differ by one character), a duplicate
  recipient, and any single bad entry in the list.

  Reading the app's parser side by side with this new one afterwards showed the
  cost of there being two: they disagreed in three places, and one of the three
  was a misroute rather than a cosmetic difference. `*local`, and a bare
  trailing `*`, mean "whatever server this resolves against" -- the format says
  so explicitly -- and this parser read `local` as a hostname, turning a
  documented spelling into `https://local`, which fails as an unreachable server
  instead of as a misread address. Fixed here; the other two (canonical
  rendering keeping the default scheme, and no answer at all for "are these two
  server spellings the same server") are recorded in freizone-server's `SRV-31`,
  which gives the composite `id*server` form one home in `pkg/address` and
  leaves this file a thin wrapper holding only the bot's own policy.

- 2026-08-20 — **and then the parser left**, to freizone-server's `SRV-31`,
  which is where the `id*server` format now lives (`pkg/address.Parse` /
  `ParseFull` / `NormalizeServer` / `SameServer`). What stayed here is only what
  is the *bot's* rule rather than the format's: a recipient must be complete and
  never a prefix, an account goes in the peer route and a group in the group
  route, no duplicates, a list is all-or-nothing. About forty lines, and each of
  them is a decision this bot makes.

  Two things improved by not being ours any more. The duplicate check moved to
  `SameServer` and immediately caught a case it had been letting through:
  `id*example.org` and `id*http://example.org` are one recipient, since the
  scheme is how this bot happens to reach a server rather than part of who lives
  there, and a check comparing rendered strings had been delivering to them
  twice. And six places were building an address by hand with
  `AccountID + "*" + Server` -- the status response, two log lines, the
  first-run banner, `whoami`, the address file -- each keeping whatever spelling
  of the server it had been configured with. So the address an operator was told
  to invite did not match the address in the file sitting next to it. All six now
  go through `client.Identity.Address()`, and each picked the rendering it
  actually wanted: hyphenated in the banner a person reads off a screen,
  canonical everywhere something is compared, stored or piped.

  **This is the fourth time in one session that documentation ran ahead of the
  code**, after SRV-23's status, the group route, and the join catch-up. The
  common thread is worth naming: each was a sentence written while designing
  something, which then read as a description of what had been built. All four
  were found by Andreas asking a question the docs had already answered
  confidently -- which is not a review process.

### BOT-03 — Message shaping
Status: `done`

Deduplication keys, collapsing a storm into one line with a count, and labels
deciding which route a message takes.

Pairing a `resolved` notice with the `firing` one it answers was part of this
item and has been **dropped** — see the 2026-08-23 entry.

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

- 2026-08-23 — **the third piece is dropped, not deferred:** pairing a
  `resolved` notice with the `firing` one it answers.

  It had been open pending decisions about restart semantics, an unmatched
  `resolved`, and expiry. What actually settled it is that the ground it stood
  on is gone. The pairing key was going to come from a monitoring tool's
  payload -- `fingerprint`, plus a `status` of `firing` or `resolved` -- and
  `BOT-08` decided the bot reads no sender's payload at all. Without that, the
  key would have to be guessed, and the only honest guess is "same title, same
  labels", which is the deduplication key and does not distinguish a thing
  starting from the same thing ending.

  It could be done by having the sender supply the key -- `?dedup=` already
  exists and could carry it. Dropped anyway, and this is the reason worth
  recording: **firing/resolved is one domain's vocabulary.** A build result, a
  digest, a sensor reading and a chat answer have no such pair, and building the
  state machine for it would put an alerting concept back at the centre of a
  bridge that had just been cleared of them. Whoever needs "it is over now"
  sends a message that says so.

  What replaces it, for anybody who wants the effect: send the resolution as a
  message with the same labels and a `dedup` key of your own. The bot will not
  correlate them, and a person reading the chat will.

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

### BOT-08 — HTTP ingress
Status: `done`

An HTTP endpoint for anything that can only POST, and **the first network
listener this bot has ever had** — which is why it is its own item with its own
authentication design, its own TLS question and its own abuse handling. Reuses
the routes and the outbox unchanged: the ingress is a second producer for the
same queue.

- 2026-08-23 — **done**, `internal/webhook`, and the shape of it came from a
  correction rather than from the plan. My first design specialised toward one
  monitoring tool: an Alertmanager adapter, N alerts per POST, grouping,
  pairing a `resolved` with its `firing` by `fingerprint`. Andreas rejected it
  — *"freizone-bot soll alertmanager und seine formate gar nicht kennen"*, one
  POST is one message, talk about general length limits instead. That is the
  third time in this repo that a design of mine drifted toward alerting, so it
  is written down as a standing rule rather than a note on this item.

  What the correction bought is not just principle, it is *most of the
  complexity gone*. The whole grouping-and-pairing apparatus existed for one
  reason: some senders batch several events into one request, and **reading the
  body is the only way to know they did.** Refuse to read the body and the
  question disappears. So:

  **The body is the text. The query string is everything else** — `title`,
  repeated `label=key:value`, an optional `route`, an optional `dedup`. The same
  message model as `freizone-bot send`, over HTTP. The query rather than headers
  because the URL is the one thing every sender lets you configure, while plenty
  will not let you add a header.

  A sender that posts JSON therefore gets its JSON in the chat, as text. That is
  the honest outcome of not knowing formats, and the fix for anybody who dislikes
  it is on the sending side.

- 2026-08-23 — **length limits, general rather than per-format.** Three, and each
  answers a different question, which is why they are three numbers and not one:
  the request body caps at 1 MiB (a misconfigured sender must not decide how much
  memory this process uses — `413`, nothing sent); the message reaching a chat
  caps at 4000 characters and 60 lines (a phone screen — shortened, and it *says*
  `(cut short)`); labels cap at 20 per request (they are rendered into the message
  and used as a deduplication key, so an unbounded number is both unreadable and
  a way to defeat deduplication).

  The chat limit moved to `outbound.TrimToChatSize`, shared with the CLI and with
  a command's reply — `internal/declared`'s own constants now point at it.
  "How much text belongs in a chat message" does not depend on how the text
  arrived, and two answers to it would have differed only by which code path
  somebody looked at. Chosen generously rather than tightly because the README
  documents piping twenty lines of a log in, and because the protocol is nowhere
  near binding: a Freizone server accepts a 512 KiB request body.

- 2026-08-23 — **one accept path, deliberately.** `handleSend` and the ingress
  both call `daemon.accept`, which owns routing, deduplication, the rate cap and
  the queue. Extracted *before* the ingress was written rather than after: three
  times in this repo a second path has quietly drifted from the first (the group
  join, the command dispatch, the invitation catch-up), and an ingress that
  resolved its own routes or skipped the deduplicator because nobody remembered
  it would have been the fourth. There is nothing to keep in step because there
  is one path.

  Authentication is a bearer token per sender, in a file, `chmod 600`, compared
  in constant time. Per sender rather than one shared, so a single sender can be
  switched off without switching off the rest and so a log line can say who sent
  something — with one shared token, "who is flooding us" has no answer. A
  listener configured with no tokens file is refused at startup: an ingress
  nobody is authorised to use would accept everything. A token under 24
  characters is refused, since nothing here rate-limits guessing. Tokens are
  never quoted back into an error or a log line.

  Two of my own tests found their own bugs, both of them mine rather than the
  code's, and one of them the same Windows lesson twice: the token file's
  permission check fired on every Windows run, because Go synthesises `0666`
  there. `account.checkPrivate` had already solved that exact problem, so the
  fix was to follow the pattern already in the repo rather than invent one.

  Verified against the running daemon with `curl`, not only in tests: `401`
  without a token, `404` on another path, `405` on a `GET`, `400` on an empty
  request, `202` with `queued for 1 destination(s)` — and the log line naming
  the sender and the title, with the body **absent from the log**, which is the
  rule the rest of the bot follows and the one worth checking rather than
  assuming.

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

### BOT-12 — Actions an operator can declare
Status: `done`

Adding a command meant editing `internal/action/builtin.go` and rebuilding,
which is honest for a Go binary and useless to the person actually running one.
Two declarable kinds in a JSON file (`FREIZONE_BOT_ACTIONS_FILE`): a **fixed
reply**, and an **HTTP request** whose answer becomes the reply.

- 2026-08-21 — **done**, `internal/declared`. The kinds were chosen by what they
  cost rather than by what they enable. A fixed reply executes nothing, so it
  adds no attack surface at all and covers more than it sounds like it does --
  rotas, runbook links, canned answers. A request reaches out of the machine but
  reaches something that *already decides for itself* whether to act, so the bot
  holds a URL and a token instead of a shell.

  **Not built, and this is the decision the item exists to record:**
  `restart=systemctl restart nginx` in a configuration file. It is the obvious
  third kind, it is what everybody asks for, and it is remote code execution for
  anybody who gets a message past the allow-list -- arriving dressed as a
  convenience feature. Anything that has to run on the host belongs behind an
  endpoint, which is the same boundary an operator would want anyway and which
  the request kind already reaches.

  **A declared action is still a registered action**: closed set, `Spec`,
  parameters validated by `Registry.Execute`. So nothing here widens what an
  interpreter can reach, and `BOT-10` inherits the narrowing for free -- the
  patterns hold for a model's output because a model also goes through `Execute`.
  Requests set `Sensitive`, which had existed since `BOT-05` with nothing setting
  it; a fixed reply does not, since it changes nothing.

  Two guards on a URL rather than one. Parameters are percent-encoded going in,
  *and* the filled URL has to still resolve to the scheme and host the file
  named -- checked again on redirects, capped at three. A bot sits inside a
  network and carries a token, so being made to fetch a URL of the caller's
  choosing is the thing worth two locks. Patterns on parameters are anchored even
  when written unanchored, because `[0-9]+` unanchored accepts `12; and more`,
  which is not what anybody writing a pattern means. A failing pattern is *not*
  quoted back to the sender: it is a regular expression, they are in a chat
  window, and it would amount to advice on how to construct something that
  passes.

- 2026-08-21 — **the response format, which was the open question.** An endpoint
  needs none. Requiring one would have undone the point: an endpoint written for
  this bot is an endpoint somebody had to write for this bot, which is barely
  better than recompiling. So text is the reply as it stands, JSON is decomposed
  by shape (a string is itself; a list becomes one line per entry; an object
  becomes sorted `key=value` lines, the same rendering labels already get), and
  `field` narrows to a dotted path when the interesting part is buried. Sorted,
  because Go's map order would otherwise reshuffle an unchanged answer and make
  the deduplicator treat two identical things as different.

  Two things it refuses rather than renders. A non-2xx is a failure, not a reply,
  and its body is shown only when small and textual -- an HTML error page is the
  *likeliest* thing a misconfigured endpoint returns, and one pasted into a group
  transcript cannot be un-sent. HTML or binary with a 200 gets described, not
  pasted: status, type, size.

  The answer carries the action's name in front of it. Not decoration: that text
  was written by another system, an endpoint reflecting its input is one an
  outsider can write through, and without a line saying where it came from a
  group member cannot tell what the bot *found* from what the bot is *saying*.

  **A bug my own test found**, worth writing down because the shape recurs: a
  list rendered `LineLimit` entries plus an `… and N more` line, and then the
  outer size cap trimmed the last line to get back under the limit -- throwing
  away precisely the line that said what had been dropped. Two truncations in
  sequence, the second silently undoing the first one's honesty. The inner cap is
  one line short now, for that reason and with the reason written next to it.

  Also caught while writing tests: three separate URL mistakes were being
  reported as whichever check ran first, so `file:///etc/passwd` came back as
  "names no host" and a bare path as "is not http or https". Each has its own
  sentence now. And a declared name colliding with a built-in used to reach
  `Registry.Register`, which *panics* on a duplicate -- right for a wiring
  mistake in Go, wrong for a name somebody typed in a file. It is a refusal to
  start with one sentence now, not a stack trace.

  Loud about doing nothing: declared actions with no `FREIZONE_BOT_COMMANDERS`
  are a configuration that cannot be reached at all, so that combination warns
  at startup instead of coming up looking fine.

### BOT-13 — The build CI never did
Status: `done`

- 2026-08-21 — the release image build for `v0.1.0` failed on `COPY go.mod
  go.sum`: **there was no `go.sum` in this repo, and never had been.**

  The cause was a decision that was right and a consequence that was missed.
  `pkg/client` was changing alongside this repo, so builds resolved it through a
  gitignored `go.work` pointing at a freizone-server checkout next door —
  deliberately not a `replace` in `go.mod`, since that travels with the
  repository and would have had CI compile against whichever branch happened to
  be sitting there. What went unnoticed is that **a workspace keeps its hashes
  in `go.work.sum`**, so `go.sum` was never generated, and the first thing that
  ever needed one was the release image build. In public, on a tag.

  One step behind it, a second failure was waiting: `go.mod` named
  freizone-server `v0.22.0`, which predates `SRV-30` and `SRV-31` and therefore
  half the API this bot calls. Fixing `go.sum` alone would have moved the error
  from Dockerfile line 5 to line 8.

  Both are one root cause: **a local build and a CI build that were not the same
  build**, with nothing running the second one until a release. This repo had no
  workflow that compiled or tested anything — only publish-on-tag. So did
  freizone-server and freizone-gateway, but neither depends on a sibling
  checkout, so their standalone build gets exercised locally every day. This was
  the first repo in the family where the two diverged, which is why it bit here
  first and why it bit at the worst moment.

  Fixed: `go.mod` names `v0.23.0`, `go.sum` is committed, `go.work` is gone, and
  `.github/workflows/ci.yml` runs the standalone build on every push — download,
  verify, `tidy -diff`, gofmt, vet, build, `test -race`. Verified by doing the
  thing that had failed rather than reasoning about it: the image builds locally
  and its entrypoint answers.

  A diagnosis worth recording, because I got it wrong first: the module proxy
  answered `unknown revision v0.23.0` and I concluded the tag had not been
  pushed. It had. `proxy.golang.org` negative-caches a version asked for before
  it existed — and my own earlier attempt to name that version is what put it
  there. `git ls-remote` could not correct me because `origin` is an SSH remote
  and this environment holds no key, so the only evidence I had was the stale
  cache. A plain HTTPS `GET .../@v/v0.23.0.info` needs no credentials and would
  have answered the question directly.

### BOT-14 — The control group was read and then ignored
Status: `done`

- 2026-08-21 — `FREIZONE_BOT_CONTROL_GROUP` was parsed into the configuration
  and **used nowhere**. No `Chown`, no group lookup, nothing. So the security
  model's own sentence -- "the socket's parent directory is the gate: 0750,
  owned by the daemon's user and an operator group you choose" -- described a
  mechanism that did not exist.

  It failed closed, which is the only reason this was not worse: the directory
  is 0750 owned by the daemon's user, so nobody else could send either way. But
  an operator who set the variable believed they had granted specific people the
  ability to page, and nothing told them otherwise. Configured access that does
  not exist is worse than access that is plainly unavailable, because it is
  reasoned about as though it works.

  Found while answering a question about whether the bot should run in a
  container at all -- the answer needs this mechanism, since it is what lets a
  host's systemd unit send into a containerised daemon without handing that unit
  the docker socket, which is root-equivalent. The documentation had confidently
  answered a question the code could not.

  Now: the group is resolved *before* anything is created, applied to both the
  directory and the socket (granting one without the other grants nothing), and
  a failure stops the daemon. A numeric gid is accepted as well as a name,
  because a container has no `/etc/group` entry for a host group and requiring a
  name would leave the container case unconfigurable. On Windows a group is
  refused rather than ignored: the mode bits there are synthesised, so honouring
  it would be exactly the same false promise in a new place -- that is `BOT-07`.

- 2026-08-21 — the README now answers *when* to use the image, which it had
  never said. The binary is static and the image is distroless, so the container
  buys no isolation that was not already there -- it is a distribution and
  lifecycle mechanism, and every part of it is also a systemd unit. The plain
  binary is right when the bot is the machine's own pager, which is the flagship
  `systemd OnFailure=` case; the container is right when the bot is a service
  among services. And on a farm, one bot per host means every one of those hosts
  can read the group's future traffic, so the shape you want is one bot the
  others reach -- which needs `BOT-08`, or SSH in the meantime.

## Rejected

Things decided against, kept here so they are not proposed again as though new.
Each was wanted by somebody, including sometimes by me.

### `/addrecipient` and any command that edits the recipient list
Decided 2026-08-23. `/listrecipients` and `/routes` were built instead, and they
only read.

The recipient list is **configuration**. A chat command that edited it would
route around whatever review that configuration has — a unit file in git, an
Ansible role, a colleague looking at a diff — and the persistence question has
no good answer: if the change survives a restart the config file no longer says
who is on the list, and if it does not, the command lied.

The blast radius is the other half. Adding a recipient means every future
message goes there too, so one command turns "may send this bot a message" into
"receives everything this bot will ever say" — hostnames, stack traces, whatever
gets reported — and nothing looks wrong afterwards.

A nonce confirmation was considered and would not have fixed it. A nonce is not
authentication: it stops a *single* message from changing state, which helps
against a mistyped command and against a future model-driven interpreter being
talked into naming the action, and does nothing at all against a commander
account somebody else is holding.

If runtime change is ever wanted, the shape is re-reading the configuration on a
signal, where the file stays the single source of truth.

### Pairing a `resolved` notice with its `firing`
Decided 2026-08-23, was part of `BOT-03`. The pairing key was to come from a
monitoring tool's payload, and `BOT-08` decided the bot reads no sender's payload
at all. More fundamentally, firing/resolved is one domain's vocabulary: a build
result, a digest, a sensor reading and a chat answer have no such pair.

### Operator-scriptable actions (`ACTION_restart=systemctl restart nginx`)
Decided at `BOT-01`, re-confirmed at `BOT-12`. Remote code execution for whoever
gets a message past the allow-list, arriving dressed as a convenience feature.
Anything that must run on the host belongs behind an endpoint that decides for
itself, which `BOT-12`'s request kind already reaches.

### Go plugins for new actions
Considered at `BOT-12`. Linux and macOS only, requires an exact toolchain match,
and a plugin is arbitrary code in a process holding long-lived private keys —
the same trust as recompiling, with worse ergonomics.

### An adapter for any sender's payload format
Decided at `BOT-08`. One POST is one message and the body is never parsed. A
named adapter would make one monitoring tool's vocabulary the centre of gravity
of a general bridge. It is also what removed the grouping and pairing complexity
rather than relocating it: batching is only knowable by reading the body.

### HTTP over the unix control socket
Decided at `BOT-01`. Tempting, since it would have reused freizone-gateway's
whole API idiom — and it turns "this bot opens no network listener" from a
property of the code into a property of the configuration. Once the handlers are
`http.Handler`s a port is two lines away, and `BOT-08` gets decided by accident.
(`BOT-08` has since been built, deliberately, with its own authentication.)

### The CLI starting a daemon when none is running
Decided at `BOT-01`. Forks a long-lived, key-holding process into whatever
context a cron job happened to have: wrong user, no supervision, inherited file
descriptors — and N concurrent jobs then race for the account lock.

### BOT-15 — A guide, separate from the reference
Status: `done`

- 2026-08-23 — the README had grown to 559 lines and 25 headings and had become a
  *reference*: good for looking something up, unusable as a path from nothing to
  a working bot. Asked whether the repo had setup help, the honest answer was
  "there is a section called Getting it running, and no, it does not".

  What a first-time operator specifically did not get: **no systemd unit for the
  daemon** (only the one-shot sender unit, while the security section implicitly
  promises a hardened one), no `docker run` example, no end-to-end order in one
  place -- the container-or-binary decision sat 230 lines *after* the install
  instructions -- and no "how do I know it works" step.

  `docs/SETUP.md` is the walkthrough: one path, seven steps, both deployment
  shapes complete, a troubleshooting table, and a closing list of what catches
  people out. The README keeps the reference and links to it twice. Splitting
  rather than rewriting, because a guide and a reference have different readers
  and merging them is what produced the 559 lines.

  **Three of my own drafted claims were wrong** and were caught by checking them
  against the code rather than by remembering: the join log line says `joined a
  group` and not `joined the configured group`, and `status` prints
  `connected: true` / `outbox: N waiting` where I had written the chat
  command's wording (`connected: yes` / `queued: N`). That the two surfaces word
  the same facts differently is a small wart of its own, left alone.

  **And two defects in the systemd unit I had drafted**, both of which would have
  shipped a unit that does not work. `ProtectSystem=strict` makes the filesystem
  read-only apart from what a unit explicitly asks for, so a hand-created
  `/var/lib/freizone-bot` would have been unwritable -- `StateDirectory=` is what
  makes it work, and it sets the mode too. And `StartLimitIntervalSec=` /
  `StartLimitBurst=` belong in `[Unit]`, not `[Service]`, where systemd has
  silently ignored them since v229.

  Checked mechanically rather than by reading: every `FREIZONE_BOT_*` variable
  the guide names exists in `config.go` (11 of 11), and every README anchor it
  links to resolves (9 of 9). Not checked end to end -- the local test instances
  were not running by then, and starting somebody's containers to test a document
  is the wrong trade.

- 2026-08-23 — **`send`, `status` and `whoami` no longer demand a server
  address.** `config.Load` insisted on one for every subcommand, though only the
  daemon talks to a server: `send` and `status` speak to the local socket and
  `whoami` reads a file. So the flagship `systemd OnFailure=` unit had to carry a
  setting it has no use for, and a forgotten one answered a send with "the bot
  has no default server to register against" -- which is not what the caller was
  doing. Found while writing the guide, which would otherwise have had to
  document the requirement as though it made sense.

### BOT-16 — Every spelling of an address, everywhere
Status: `done`

- 2026-08-23 — Andreas configured `FREIZONE_BOT_ROUTE_GROUP="p5stj*chatcentral.de"`
  -- the compact form the app displays -- and this bot refused it on two counts.
  The rule he then stated is general and applies to **every** address input, for
  groups as much as for accounts: the whole id, the hyphenated display form, the
  short prefix, with `*server`, with `*local`, with a bare `*`, with none.

  **My strictness was the wrong decision**, and worth recording as such. The
  argument was that configuration is not typed under time pressure, so a
  truncated id should fail rather than resolve to whoever happens to match. It is
  wrong twice over. In principle, because every one of those forms is one the app
  *displays*, so refusing any of them means the form most likely to be pasted is
  the one that does not work. And in fact, because completing a prefix is not
  guessing: `ResolvePeer` has the server complete it and then verifies the
  returned id against the returned root key, and `StartConversation` takes an
  `addressOrPrefix` -- the core could do this all along and only this bot's
  configuration layer refused.

  What genuinely needed guarding was the one case I had not separated out: an
  **ambiguous** prefix. That is an error now, naming every match, because quietly
  picking one would send somebody's messages to a group they did not mean.

- 2026-08-23 — a bug found on the way: `config.Load` called `ParseGroupID` and
  **discarded the result**. So it validated one value and then used another --
  the raw string, in whatever spelling was pasted. `p5stj*chatcentral.de` would
  have passed validation and then been used verbatim as a group id. Now stored.

  Resolution needed somewhere to live, since a prefix cannot be resolved without
  an open account. `cmd/bot/group.go`: `resolveGroup` at startup matches the
  configured value against `Client.Groups()`, and `configuredGroup()` /
  `isConfiguredGroup()` are what everything downstream uses. No match is not an
  error -- that is the ordinary first run, where the group is configured before
  the invitation has arrived, and the invitation carries the whole id. A failed
  listing is not an error either: the invitation path resolves it anyway, and
  refusing to start over one local read would be worse.

  `outbound.Resolve` takes the resolved group id as a parameter now rather than
  reading `cfg.RouteGroup`, because a configured prefix is not a destination.

  One thing deliberately left half-done, with the reason in the code: the check
  that a peer route got an account and a group route got a group works on a whole
  id and not on a prefix, because `address.VersionOf` normalises before reading
  the marker and so needs all 21 characters. The marker *is* the first character,
  so re-deriving it here would work -- and would mean copying the bech32 charset
  into this repository, which is exactly what SRV-31 stopped. The fix belongs in
  `pkg/address` as a `VersionMarkerOf`, and a test pins the current behaviour so
  that adding it makes this repository's tests fail rather than quietly leaving
  the gap open.

### BOT-17 — A changed server was silently ignored
Status: `done`

- 2026-08-23 — an admin on `chat.behringer24.de` tried to invite the bot as
  `qk86f*chatcentral.de` and got a 404. The address was not the problem: that
  account was registered on a **local test instance**, and chatcentral.de had
  never heard of it. The 404 was correct.

  What made it hard to get out of is the part worth fixing. `EnsureRegistered`
  returned a stored identity without comparing its server to the configured one,
  so pointing `FREIZONE_BOT_SERVER` at the intended server and restarting logged
  `account ready` and carried on talking to the old one. A configuration change
  that is silently ignored, at the exact moment somebody is trying to work out
  why nothing arrives.

  It now refuses to start, naming both servers and both ways out: an account
  cannot move between servers -- its address *is* `id*server` and its keys are
  published there -- so either the original server goes back, or a fresh state
  directory registers a second account on the new one, which gets a new address
  and has to be invited again. `SameServer` does the comparison, so adding or
  dropping the default scheme is not mistaken for a move.

  Verified live against a copy of a real account directory, both ways: the
  refusal fires on a genuinely different server, and does not fire when the same
  server is spelled without its scheme.

- 2026-08-23 — **the same rule, one input further, and this was the worst place
  it was broken:** `FREIZONE_BOT_COMMANDERS`. `authz.New` put the raw configured
  string into its set and compared it against the canonical account id the
  receive path reports -- so the hyphenated form, which is what the app copies,
  matched **nobody**.

  And it failed closed *and* silently, which is the shape that costs the most. A
  sender who is not on the allow-list gets no reply at all, deliberately, since a
  refusal tells whoever asked that something is here and listening. So the
  operator sets the variable, sends a command, hears nothing, and has no way to
  tell the allow-list apart from a wrong group or a stopped bot.

  `ParseCommanders` now reduces every spelling to the canonical id -- hyphenated,
  upper case, `*server`, `*local` -- and refuses what genuinely cannot work: a
  group id, a duplicate, and **a prefix**. The prefix refusal is the interesting
  one, because it goes the other way from everywhere else: a recipient prefix is
  *completed* and verified, while an allow-list entry is checked against whoever
  happens to send something, so a prefix there would authorise everybody whose id
  begins with it. Five characters of a bech32 id is not an authorisation decision
  anybody means to make, and the error says so.
