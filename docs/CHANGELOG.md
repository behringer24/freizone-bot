# Changelog

All notable changes to freizone-bot are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Reference codes in parentheses (e.g. `BOT-01`) point at the item in
[`ROADMAP.md`](ROADMAP.md). This file records *what shipped when*; the reasoning
lives there.

Releases are cut as annotated git tags.

## [Unreleased]

### Added

* **An HTTP ingress, off by default (`BOT-08`).** For anything that can only
  POST. The body is the message text and the query string carries the title,
  the labels, an optional route and an optional deduplication key — the same
  message model as `freizone-bot send`, over HTTP. **One request is one
  message.** The body is never parsed and no sender's payload format is known
  to the bot, which is what keeps it a general bridge rather than one
  monitoring tool's companion. Requires a file naming who may POST, and
  refuses to start without one
* **General length limits, in one place.** A request body caps at 1 MiB
  (`413` beyond it); a message reaching a chat caps at 4000 characters and 60
  lines and says `(cut short)` when it was shortened; labels cap at 20 per
  request. The chat limit is shared by every producer — the CLI, the ingress
  and a command's reply — because how much text belongs in a chat message does
  not depend on how it arrived

### Fixed

* **`FREIZONE_BOT_CONTROL_GROUP` does something now (`BOT-14`).** It was read
  from the environment and used nowhere, so an operator who named a group
  believed they had let specific people send, and nothing had happened. It
  failed closed, which is the only reason it was not worse. The group is applied
  to the socket and its directory, accepts a numeric gid as well as a name (a
  container has no `/etc/group` entry for a host group, so a name would not
  resolve there), and one that cannot be resolved or handed out stops the daemon
  instead of being skipped

### Changed

* **The README says when to use the container and when not to**, which it had
  never said. The binary is static and the image is distroless, so the container
  buys no isolation that was not already there — it is a distribution and
  lifecycle mechanism, and so is a systemd unit. Also what a farm of many
  machines actually wants, since one bot per host puts every one of those hosts
  inside the alert group's confidentiality boundary

## [0.2.0] — 2026-08-21

The first release that builds from a clean checkout — 0.1.0's image build never
completed, so this is the first published image as well.

### Added

* **Actions an operator can declare in a file (`BOT-12`).**
  `FREIZONE_BOT_ACTIONS_FILE` takes two kinds: a fixed reply, and an HTTP
  request whose answer becomes the reply. Adding a command no longer means
  editing Go and rebuilding. Deliberately absent is the third kind everybody
  asks for — a shell command in a configuration file — because that is remote
  code execution for anybody who gets a message past the allow-list, dressed as
  a convenience feature. Anything that must run on the host belongs behind an
  endpoint, which the request kind already reaches
* **A build-and-test workflow**, which this repo did not have. Until now the
  only workflow published an image on a version tag, so nothing ever compiled
  the repo except a developer machine — and a developer machine resolved
  `pkg/client` from a working tree next door rather than from the module proxy

### Fixed

* **The release image could not be built (`go.sum` was missing entirely).** Every
  build here went through a `go.work`, which keeps its hashes in `go.work.sum`,
  so `go.sum` had never been generated — and the first thing to ever need it was
  the Dockerfile, in public, on a tag. One step behind it, `go.mod` still named
  freizone-server `v0.22.0`, which predates half the API this bot calls. Both are
  the same root cause: a local build and a CI build that were not the same build,
  with nothing running the second one until a release. `go.mod` now names
  `v0.23.0`, `go.sum` is committed, the workspace file is gone, and CI runs the
  standalone build on every push

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

* **This tag does not build from a clean checkout, and its image never got
  built.** `go.mod` requires freizone-server `v0.22.0`, which predates `SRV-30`
  and `SRV-31` — the account registration, the directory lock, the queue drain
  and the address parser this bot is built on — and there was no `go.sum` at
  all. Fixed in 0.2.0; use that instead. Nothing was published from this tag,
  since the build failed before the registry push.
* Pairing a `resolved` notice with its `firing` is not implemented (`BOT-03`).
* No webhook receiver (`BOT-08`), no server-assistant role (`BOT-09`), no
  interpretation by a model (`BOT-10`).
