# Freizone Bot

An automation daemon for [Freizone](https://github.com/behringer24/freizone-server). It holds a Freizone account of its own and connects Freizone to other systems in **both directions**: something happens elsewhere and becomes a message, or a message causes something to happen.

**Why this exists:** operations alerting is the first thing it does. A monitoring system that pages you through Slack or Telegram hands every alert — hostnames, internal addresses, stack traces, sometimes a credential in a log line — to a third party. Freizone is end-to-end encrypted and self-hosted, so the same alert reaches the same phone without anyone in between being able to read it. Alerting is the first capability rather than the shape of the whole thing: the same daemon is where a server-assistant, a command bot and later integrations live.

**Status:** `BOT-01`, `BOT-02`, `BOT-05` and most of `BOT-03` work, driven end to end against a real server rather than only in tests: the daemon registers its own account, joins the group it was configured for, delivers messages handed to it over a local socket with a durable queue behind them, and answers commands from an allow-listed sender. Not there yet: pairing a `resolved` notice with its `firing` (`BOT-03`), a webhook receiver (`BOT-08`), a server-assistant role (`BOT-09`), interpretation by a model (`BOT-10`). See [`docs/ROADMAP.md`](docs/ROADMAP.md).

## What the bot never does

- **It opens no network listener.** Its only ingress is a unix socket inside its own state directory, reachable by whoever the operator puts in one group and nobody else. There is no port to expose and no `EXPOSE` line in the Dockerfile — when that changes, it will be a deliberate release with its own authentication design ([`BOT-08`](docs/ROADMAP.md)), not a configuration flag somebody flips.
- **It implements none of the Freizone protocol.** Encryption, sessions, delivery semantics and group convergence all live in freizone-server's [`pkg/client`](https://github.com/behringer24/freizone-server/tree/master/pkg/client), which the app uses too. The bot's own code begins after a message has been decrypted.
- **It never logs message bodies.** They land permanently in every recipient's transcript and routinely carry things that should not also sit in a log file. Title and labels are enough.

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

### Putting it in a group

Create the group in the app, put its id in `FREIZONE_BOT_ROUTE_GROUP`, and invite the bot to it. **It accepts that invitation by itself** — naming the group in the configuration is what asks for it.

The order does not matter. Invite first and configure afterwards, and the bot finishes the invitation on its next start: an invitation is only announced once, so it reads the facts it already holds rather than waiting to be told again.

Invitations to any *other* group are left unanswered unless you set `FREIZONE_BOT_ACCEPT_GROUP_INVITES=true`. That is deliberate: an invitation you did not ask for is somebody else deciding what your bot is a member of, and from then on it holds that group's facts and receives its traffic. The bot never declines either — declining is a signed fact that says something, and the honest state is "nobody asked this bot to be there".

## Sending a message

The daemon owns the account, so the CLI does not open it — it hands the message to the running daemon over a local socket. Same binary, which is also why `docker exec <container> /freizone-bot send …` works with no shell in the image.

A message is a title, a body and **labels**. Nothing more is built in — an operations alert, a build result, a scheduled digest and a one-liner are the same shape with different labels:

```sh
freizone-bot send "Why do programmers prefer dark mode? Light attracts bugs."

freizone-bot send -title "build failed" \
  -label repo=freizone-app -label branch=master -label kind=ci "3 tests red"

freizone-bot send -severity critical -source web01 -title "disk full" "/ at 98%"

journalctl -u nginx -n 20 | freizone-bot send -title "nginx is down on web01"
```

Those three arrive as:

```
Why do programmers prefer dark mode? Light attracts bugs.
```
```
build failed

3 tests red

branch=master kind=ci repo=freizone-app
```
```
[CRITICAL] disk full (web01)

/ at 98%
```

`severity` and `source` are **conventions, not a schema**: the renderer gives them prominence because that is what people put there, `-severity` and `-source` are shorthands for `-label`, and everything works with neither present. Labels also drive routing (`FREIZONE_BOT_ROUTE_RULES`) and deduplication, so `kind=digest` can go somewhere different from `severity=critical` without either being special to the code.

Text comes from an argument, or from standard input when no argument is given — **never both**. That is not a style choice: reading standard input whenever it is not a terminal hangs forever under a service manager or a CI runner, where it is routinely an open pipe nobody ever closes. An alerting tool that blocks at the moment it is needed is the worst failure available to it.

The call returns once the message is **durably queued**, not once it is delivered — so `exit 0` means "this will be delivered or loudly reported", never "accepted into a buffer a restart discards". Delivery is retried with a backoff, and given up on after `FREIZONE_BOT_MAX_AGE_MINUTES` with an error in the log naming the message and where it was going. Pass `-wait` to block for delivery instead.

Exit codes, since this ends up inside shell scripts:

| code | meaning |
|---|---|
| 0 | queued (or delivered, with `-wait`) |
| 1 | usage or configuration problem |
| 2 | the daemon refused it — unknown route, outbox full |
| 3 | the daemon failed |
| 4 | no daemon is running |
| 5 | timed out waiting for the daemon |

`freizone-bot status` asks the running daemon for its address, whether it is connected, and how much is waiting.

## Talking to it

The bot can also answer. Set `FREIZONE_BOT_COMMANDERS` to the account ids allowed to command it, message it from one of them, and it replies in that chat:

```
/help    list what it can do
/ping    check that it is listening
/status  connected, queued, uptime
/joke    the joke of the day
```

Everything shipped today only reads, and nothing runs a command on the host. There is deliberately no configuration that would let you add one: `ACTION_restart=systemctl restart nginx` is remote code execution for whoever gets a message past the allow-list.

**With no commanders configured there is no command surface at all** — the bot will not answer anybody. That is fail-closed on purpose: not having decided who may drive your bot is not a decision that everyone may. A sender who is not on the list gets **silence**, not a refusal, because a refusal confirms to whoever asked that something is here and listening.

Commands in groups are off unless you switch them on. In a one-to-one chat the leading `/` is optional; in a group it is required, so ordinary conversation is never read as an instruction.

The interpretation of a message is deliberately a **replaceable layer**, and the authorization check runs *before* it. That ordering is the most important rule in this repository: today the interpreter is a parser and it looks academic, but the moment a model sits there (`BOT-10`), an interpreter that sees everything is one that anyone knowing this bot's address can write prompts for. The layer below it only ever executes actions that already exist, with parameters its own specification validated — a model can *name* an action, never invent one.

### With systemd

Three lines, and every failing service on the host pages you:

```ini
[Unit]
OnFailure=freizone-alert@%n.service
```

```ini
# /etc/systemd/system/freizone-alert@.service
[Service]
Type=oneshot
EnvironmentFile=/etc/freizone-bot.env
ExecStart=/usr/local/bin/freizone-bot send -severity critical -title "%i failed" -source %H
```

Nagios, Icinga, Zabbix, Sensu, cron and CI steps all take the same shape — anything that can run a command. Alertmanager cannot: it has no exec receiver and only posts webhooks, so it needs `BOT-08`.

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
| `FREIZONE_BOT_ROUTE_GROUP` | – | A group id messages are sent to. The bot **accepts an invitation to this group automatically** — naming it is asking for it. Create the group, invite the bot, and it joins on its own. |
| `FREIZONE_BOT_ACCEPT_GROUP_INVITES` | `false` | Whether the bot accepts invitations to *other* groups too. Off by default: an invitation you did not ask for is a stranger deciding what your bot is a member of, and from then on it holds that group’s facts and receives its traffic. |
| `FREIZONE_BOT_ROUTE_PEERS` | – | Comma-separated account ids or addresses messages are sent to individually. **Independent of the group route, not an alternative to it** — with both set, a message goes to both, which is how escalation is expressed: the team channel *and* whoever is carrying the pager. |
| `FREIZONE_BOT_ROUTE_RULES` | – | Narrows where a message goes based on its **labels**, in order — the first matching rule decides. `severity:critical=group+peers,kind:digest=group` means a critical thing reaches the channel and the pager while a daily digest only goes to the channel. A message matching no rule goes everywhere configured, so a partial set never silently drops anything. An explicit `-route` wins over this. |
| `FREIZONE_BOT_COMMANDERS` | – (off) | Comma-separated account ids that may command the bot. **Empty disables the command surface entirely** — the bot will not answer anybody. Deliberately not "whoever is in the group": group membership changes without you being told, a configured list changes when you change it. |
| `FREIZONE_BOT_ALLOW_GROUP_COMMANDS` | `false` | Whether commands may be given in a group. Off by default: a command in a group is visible to everyone in it, its answer is too, and the membership drifts. With it off the bot takes instructions only in a one-to-one chat. |
| `FREIZONE_BOT_JOKES_FILE` | – | One joke per line (`#` comments and blank lines ignored) for the `/joke` action, replacing the small built-in set. |
| `FREIZONE_BOT_DEDUP_WINDOW_MINUTES` | `0` (off) | Collapses repeats of the same message within this many minutes: something that flaps every thirty seconds arrives once and then carries a count. Two messages are "the same" by title and labels — deliberately **not** the body, which routinely carries a timestamp or a measurement that differs every time while the thing being reported plainly does not. Pass `-dedup-key` to decide it yourself. Off by default because deciding two messages are one event is a judgement whatever produced them is usually better placed to make. |
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
