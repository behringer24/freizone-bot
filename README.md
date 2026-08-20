# Freizone Bot

An automation daemon for [Freizone](https://github.com/behringer24/freizone-server). It holds a Freizone account of its own and connects Freizone to other systems in **both directions**: something happens elsewhere and becomes a message, or a message causes something to happen.

**Why this exists:** operations alerting is the first thing it does. A monitoring system that pages you through Slack or Telegram hands every alert — hostnames, internal addresses, stack traces, sometimes a credential in a log line — to a third party. Freizone is end-to-end encrypted and self-hosted, so the same alert reaches the same phone without anyone in between being able to read it. Alerting is the first capability rather than the shape of the whole thing: the same daemon is where a server-assistant, a command bot and later integrations live.

**Status:** early. The daemon registers its own account, holds a live connection, drains whatever is queued for it and keeps itself healthy. What it cannot do yet is **send** — the control socket, the routes and `freizone-bot send` are the rest of `BOT-01`, see [`docs/ROADMAP.md`](docs/ROADMAP.md). So it is worth starting to get an address, and not yet worth relying on.

## What the bot never does

- **It opens no network listener.** Its only ingress is a unix socket inside its own state directory, reachable by whoever the operator puts in one group and nobody else. There is no port to expose and no `EXPOSE` line in the Dockerfile — when that changes, it will be a deliberate release with its own authentication design ([`BOT-08`](docs/ROADMAP.md)), not a configuration flag somebody flips.
- **It implements none of the Freizone protocol.** Encryption, sessions, delivery semantics and group convergence all live in freizone-server's [`pkg/client`](https://github.com/behringer24/freizone-server/tree/master/pkg/client), which the app uses too. The bot's own code begins after a message has been decrypted.
- **It never logs message bodies.** They land permanently in every recipient's transcript and routinely carry things that should not also sit in a log file. Title and severity are enough.

## Security model

**The bot is a full group member, and that is the honest way to think about its blast radius.** Whoever takes over the host it runs on can not only impersonate it, but read that group's *future* traffic. So the standing recommendation is a group that exists only for alerts, containing only people who accept that an operations box sits inside that group's trust boundary. An alerting bot dropped into the team's general chat silently extends that chat's confidentiality to a machine every CI job on the network can write to.

**One process owns the account directory.** The daemon takes an exclusive lock on it and the CLI never opens it — it talks to the daemon over the control socket instead. This is not tidiness: two processes writing one account's ratchet state corrupt it, permanently and silently.

**The control socket is an authentication boundary.** Whoever can write to it can make the bot say *"false alarm, all clear"* into the alert group during a real incident — suppression is as damaging here as spam. The socket's parent directory is the gate: `0750`, owned by the daemon's user and an operator group you choose.

**Incoming messages are untrusted input from anyone** who knows the bot's address, and every group member does. Commands are therefore off unless an allow-list is configured, a sender who is not on it gets no reply at all, and the authorization check runs *before* any interpretation of the text — which is what will keep a future model-driven interpreter from being promptable by strangers.

## Getting it running

You need a Freizone server to register against — any instance, including one you run locally for the purpose. The bot needs its address, and an invite code if that server's registration policy requires one.

```sh
FREIZONE_BOT_SERVER=https://chat.example.org \
FREIZONE_BOT_STATE_DIR=./data \
freizone-bot run
```

The first start registers an account and prints the address it got, then stops — because there is nowhere to send yet:

```
  This bot registered as:

      qkh74xlzec2an4vth086f*https://chat.example.org

  Invite that address to the group it should post in.
```

That address is the one thing you have to act on: invite it to the group it should post in, or add it as a contact. It is also written to `<state>/address`, and `freizone-bot whoami` prints it later — including while the daemon is running, since it reads that file rather than opening the account.

Then give it a route and start it for real:

```sh
FREIZONE_BOT_SERVER=https://chat.example.org \
FREIZONE_BOT_STATE_DIR=./data \
FREIZONE_BOT_ROUTE_GROUP=qgroupid… \
freizone-bot run
```

A daemon with no route refuses to start rather than warning: one that accepts messages with nowhere to put them is worse than one that is plainly not configured.

## Local development

```sh
go build ./...
go vet ./...
go test ./...
```

## Configuration reference

All configuration is via environment variables (there is no config file):

| Variable | Default | Description |
|---|---|---|
| `FREIZONE_BOT_SERVER` | – | The Freizone server this bot's account lives on. **Required** — there is no default, because guessing one would mean registering an identity somewhere you did not choose. |
| `FREIZONE_BOT_STATE_DIR` | `./data` | Everything the bot keeps: the account (private keys, sessions, transcripts), media, the outbox, the control socket and the ownership lock. Back it up; losing it means losing the bot's identity. Must not be readable by anyone but the bot's own user. |
| `FREIZONE_BOT_INVITE_CODE` | – | An invite code, if the server's registration policy needs one. Prefer the file form below: an environment variable is readable through `/proc/<pid>/environ` and `docker inspect`, and an invite code is a credential. |
| `FREIZONE_BOT_INVITE_CODE_FILE` | – | Path to a file holding the invite code. Mutually exclusive with the variable above — setting both is refused rather than resolved by precedence, so there is never a question which one was used. |
| `FREIZONE_BOT_LOG_LEVEL` | `info` | `debug` · `info` · `warn` (or `warning`) · `error`. |
| `FREIZONE_BOT_CONTROL_SOCKET` | `<STATE_DIR>/control.sock` | Where the daemon listens for the CLI. Defaulting inside the state directory means a CLI invocation that inherits the daemon's environment needs no further configuration, and a container's data volume carries it. `/run/freizone-bot/control.sock` is the more conventional place under systemd (`RuntimeDirectory=freizone-bot`) and is not the default only because that path does not exist in a container. |
| `FREIZONE_BOT_CONTROL_GROUP` | – | A group that owns the socket's parent directory, so members of it may talk to the daemon. Unset leaves the directory to the daemon's own user alone. |
| `FREIZONE_BOT_ROUTE_GROUP` | – | A group id messages are sent to. |
| `FREIZONE_BOT_ROUTE_PEERS` | – | Comma-separated account ids or addresses messages are sent to individually. **Independent of the group route, not an alternative to it** — with both set, a message goes to both, which is how escalation is expressed: the team channel *and* whoever is carrying the pager. |
| `FREIZONE_BOT_MAX_AGE_MINUTES` | `60` | How long an undelivered message keeps being retried before it is dropped. An alert delivered six hours late is noise. The drop is logged at error level naming the message and its destination — a silent one would be a lie about a channel somebody is relying on. |
| `FREIZONE_BOT_RATE_PER_MINUTE` | `20` | A hard ceiling on messages leaving per minute, with the excess collapsed into one "N further messages suppressed" line. Without it, one flapping service turns the bot into a denial of service on your own phone — and on your own server, whose per-device queue is bounded and starts refusing at 1000. |
| `FREIZONE_BOT_OUTBOX_MAX` | `1000` | How many accepted-but-undelivered messages the outbox holds. Beyond it, `send` is refused with a non-zero exit rather than silently dropping: a loud rejection leaves the caller able to do something about it. |
| `FREIZONE_BOT_MAINTENANCE_INTERVAL_MINUTES` | `360` | How often the periodic upkeep runs (topping up one-time prekeys, settling group facts, recovering sessions, re-sending confirmations) in addition to running on every reconnect. The timer matters more here than in a phone app: a phone reconnects constantly, while a server bot can hold one connection for weeks and would otherwise never run any of it again. |

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

There is no CI beyond the release image build, so these are run by hand.

While `pkg/client` is changing alongside this repo, builds resolve it through a **gitignored `go.work`** pointing at a `freizone-server` checkout next door, rather than a `replace` directive in `go.mod`. That is deliberate: a `replace` travels with the repository, so CI and the Docker build would silently compile against whichever branch happened to be checked out. A workspace file stays on the machine that created it.

## License

AGPL-3.0-or-later — `SPDX-License-Identifier: AGPL-3.0-or-later`. See [LICENSE](LICENSE).

This is not a free choice: the bot links freizone-server's `pkg/client`, which is AGPL, into a single binary. Worth knowing what that means in practice, since the AGPL's network clause is easy to misread — running an **unmodified** bot for yourself obliges you to nothing. Running a **modified** one that other people send commands to over Freizone does engage § 13, and its source has to be offered to them.
