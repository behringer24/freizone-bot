# Changelog

All notable changes to freizone-bot are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Reference codes in parentheses (e.g. `BOT-01`) point at the item in
[`ROADMAP.md`](ROADMAP.md). This file records *what shipped when*; the reasoning
lives there.

Releases are cut as annotated git tags.

## [Unreleased]

### Fixed

* **`FREIZONE_BOT_COMMANDERS` accepts every spelling, and a prefix is refused
  with the reason (`BOT-16`).** The allow-list held whatever was written and
  compared it against the canonical account id the receive path reports, so the
  hyphenated form -- the one the app copies -- authorised nobody. It failed
  closed *and* silently, since a sender who is not on the list gets no reply by
  design, so nothing said why. A prefix is now refused rather than completed:
  unlike a recipient, an allow-list entry is checked against whoever sends
  something, so a prefix would authorise everybody whose id begins with it.

* **A configured server that disagrees with the account's own is refused
  (`BOT-17`).** An account belongs to the server it was registered on -- its
  address is `id*server` and its keys are published there -- so pointing
  `FREIZONE_BOT_SERVER` somewhere else is not a change the bot can carry out. It
  used to be ignored: the stored server was used and the start logged
  `account ready`, at the exact moment somebody was trying to work out why
  nothing arrived. Now it stops, names both servers, and says the two ways out

* **Every spelling of a Freizone address is accepted, for groups as well as
  accounts (`BOT-16`).** The whole id, the hyphenated display form, the short
  prefix from the app's compact rendering, upper case, with `*server`, with
  `*local`, with a bare `*`, with none — in `FREIZONE_BOT_ROUTE_PEERS` and in
  `FREIZONE_BOT_ROUTE_GROUP` alike. A group id may carry a server part, which is
  accepted and discarded: a group is not reached through a server, but the form
  the app shows carries one.

  This bot used to require the whole checksummed id, on the reasoning that a
  truncated one in a configuration file should fail rather than resolve to
  whoever happens to match. That was wrong: every one of those forms is one the
  app *displays*, so refusing any of them means the form most likely to be
  pasted is the one that does not work — and completing a prefix is verified
  rather than guessed, the server completing it and the client then checking the
  returned id against the returned root key. What genuinely needed guarding was
  an **ambiguous** prefix, which is now an error naming every match.

* **`config.Load` used the raw group id rather than the parsed one.** It called
  the parser and discarded the result, so it validated one value and then used
  another — whatever spelling had been pasted, hyphens and server part included

### Added

* **A setup guide (`docs/SETUP.md`).** One path, in order, from nothing to a bot
  that delivers: register, invite, route, verify, and then make it a service —
  with a complete hardened systemd unit and a Docker equivalent, a
  troubleshooting table, and a list of what catches people out. The README had
  grown into a reference and was unusable as a walkthrough; it keeps the
  reference and links to the guide.

  With a **Windows** section: the same steps in PowerShell, the service wrappers
  that work where `sc.exe` does not (it starts the binary and then fails, since
  there is no service control handler), and the three things that behave
  differently there. The first of those matters: the bot has no way to verify
  the permissions on the directory holding its private keys, because Windows
  permissions are ACLs and the mode bits Go synthesises say nothing about them —
  so that check is disabled and the ACL is yours to set. Windows is a
  development and testing platform for this bot, not a hardened deployment
  target, and the guide says so

### Fixed

* **`send`, `status` and `whoami` no longer require `FREIZONE_BOT_SERVER`.** Only
  the daemon talks to a server — `send` and `status` speak to the local socket
  and `whoami` reads a file. The `systemd OnFailure=` unit therefore had to carry
  a setting it has no use for, and forgetting it answered a send with "the bot
  has no default server to register against", which is not what the caller was
  doing

## [0.4.0] — 2026-08-23

Two questions the bot can now answer about itself, and one piece of work
deliberately dropped rather than carried.

### Added

* **`/listrecipients` and `/routes`.** Two read-only answers to the questions an
  operator actually asks about a bot that has been running for a while: who is
  getting these, and what decided that. `/listrecipients` names the group and
  each person, keeping the server of a federated recipient, and says when a
  routing rule can narrow the list. `/routes` lists the rules in configured
  order and spells out the two things somebody would otherwise discover during
  an incident — a message matching no rule goes everywhere, and an explicit
  route beats the rules.

  **There is deliberately no `/addrecipient`**, and the roadmap now has a
  rejected-work section saying why: the recipient list is configuration, a
  command that edited it would route around the review configuration exists to
  have, and adding a recipient means every future message goes there too.

### Changed

* `BOT-03` is complete rather than in progress: pairing a `resolved` notice with
  its `firing` has been **dropped** rather than deferred. The pairing key was to
  come from a monitoring tool's payload, which `BOT-08` decided the bot does not
  read — and firing/resolved is one domain's vocabulary in a bridge meant to
  carry build results, digests and sensor readings too.

## [0.3.0] — 2026-08-23

The bot grows a second way in, and one general rule about how much text belongs
in a chat message. Both were shaped by the same correction: this is a bridge
between Freizone and other systems, and operations alerting is one use of it
rather than its shape.

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
