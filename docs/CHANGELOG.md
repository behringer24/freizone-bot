# Changelog

All notable changes to freizone-bot are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Reference codes in parentheses (e.g. `BOT-01`) point at the item in
[`ROADMAP.md`](ROADMAP.md). This file records *what shipped when*; the reasoning
lives there.

Releases are cut as annotated git tags.

## [0.1.0] — 2026-08-21

The first release: a daemon that holds a Freizone account, stays connected, and
carries messages in both directions.

### Added

* **A daemon with its own account (`BOT-01`).** It registers itself against a
  server on first start and prints the address to invite. Registration survives
  a crash halfway through it: the keys and a marker go to disk before the
  request, so a restart asks the server whether the account is already there
  instead of registering a second one under a different address.
* **One process owns the account.** The daemon takes an exclusive lock on its
  state directory and the CLI never opens it — `freizone-bot send` hands the
  message to the running daemon over a unix socket instead. Two writers on one
  account's ratchet state corrupt it, so this is refused rather than survived.
* **Sending, with a durable queue.** `send` returns once the message is on disk,
  not once it is delivered: an exit code of 0 must never mean "accepted into a
  buffer a restart discards". One queue entry per (message × destination), so a
  retry after a partial success does not deliver again to whoever already has it.
* **Group and one-to-one routes (`BOT-02`), independent rather than
  alternatives.** With both configured a message goes to both, which is how
  escalation is expressed. A peer may live on another server —
  `id*chat.example.org` — and every recipient is checked when the configuration
  is read rather than at the first message.
* **It joins the group it was configured for**, accepting that invitation by
  itself, in either order: invite first and configure afterwards and it finishes
  the invitation on its next start. An invitation to any other group is left
  unanswered unless explicitly allowed.
* **Messages are a title, a body and labels (`BOT-03`).** Not a severity and a
  source as fields — labels describe an alert, a build result, a digest and a
  one-liner equally, and they drive routing and deduplication. `severity` and
  `source` are conventions the renderer gives prominence to, not a schema.
* **Deduplication and a rate ceiling.** Repeats of the same message collapse
  into one that carries a count; a hard per-minute ceiling turns a flapping
  source into one "N further messages suppressed" line rather than a denial of
  service on your own phone.
* **Commands, off by default (`BOT-05`).** With an allow-list configured the bot
  answers `/help`, `/status`, `/ping`, `/joke` in a one-to-one chat. The
  authorization check runs *before* any interpretation of the text, and the set
  of actions is closed — which is what will keep a model-driven interpreter from
  being promptable by strangers.
* **No network listener at all.** The only ingress is a unix socket inside the
  state directory. There is no port to expose and no `EXPOSE` line in the
  Dockerfile.

### Known limitations

* **This tag does not build from a clean checkout yet.** `go.mod` requires
  freizone-server `v0.22.0`, which predates `SRV-30` and `SRV-31` — the account
  registration, the directory lock, the queue drain and the address parser this
  bot is built on. Until a freizone-server release contains them and `go.mod`
  names it, building requires a `go.work` pointing at a freizone-server checkout
  (see the README's development section).
* Pairing a `resolved` notice with its `firing` is not implemented (`BOT-03`).
* No webhook receiver (`BOT-08`), no server-assistant role (`BOT-09`), no
  interpretation by a model (`BOT-10`).
