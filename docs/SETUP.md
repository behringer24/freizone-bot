# Setting up freizone-bot

One path, in order, from nothing to a bot that delivers. Roughly twenty minutes,
most of it waiting for you to open the app.

The [README](../README.md) is the reference — every setting, every rule, and the
reasoning behind them. This is the walkthrough. Where the two disagree, the
README is right and this file has a bug.

The commands below are for a shell on Linux or macOS. **On Windows the steps are
the same, the commands differ, and three things behave differently enough to
matter** — see [On Windows](#on-windows).

**What you need before starting:**

- a Freizone server to register against, and its address
- an invite code, if that server's registration policy asks for one
- the Freizone app, signed in, so you can create a group and invite the bot
- Go 1.26 or later to build, or Docker to run the image

---

## 1. Get a binary

```sh
git clone https://github.com/behringer24/freizone-bot
cd freizone-bot
go build -o freizone-bot ./cmd/bot
```

Or take the image, which needs no toolchain:

```sh
docker pull ghcr.io/behringer24/freizone-bot:latest
```

**Which of the two you want is a real decision, and the image is often the
wrong answer** — see [A container, or just the binary?](../README.md#a-container-or-just-the-binary)
in the README. Short version: if this bot is going to page you about *this
machine*, take the binary, because the things that will page you (`systemd`,
cron, a CI step) run on the host and the daemon's ingress is a unix socket. If
it is one service among containers, take the image.

The walkthrough below uses the binary. Step 6 has the container equivalent.

## 2. Register its account — by hand, once

Do this interactively rather than under a service manager. The first run
**registers an account and then exits**, because there is nowhere to send yet —
and a service manager would read that exit as a crash and restart it in a loop.

```sh
FREIZONE_BOT_SERVER=https://chat.example.org \
FREIZONE_BOT_STATE_DIR=./data \
./freizone-bot run
```

If the server requires an invite code, add it — from a **file**, not an
environment variable, because `docker inspect` and `/proc/<pid>/environ` hand
environment variables to more things than a one-time credential deserves:

```sh
FREIZONE_BOT_INVITE_CODE_FILE=/etc/freizone-bot/invite ./freizone-bot run
```

What you should see:

```
  This bot registered as:

      qkh74-xlzec-2an4v-th086-f*chat.example.org

  Invite that address to the group it should post in.
  It is also in data/address.
```

**That address is the one thing you have to act on.** It is also in
`<state>/address` and `freizone-bot whoami` prints it later, so you cannot lose
it — but write it down now anyway, because the next step needs it.

The hyphens are only there to help you read it off a screen. Both forms mean the
same address.

> **If it exits with `no route configured` — that is this step succeeding.**
> Registration worked; there is simply nowhere to send yet. Step 4 fixes that.

## 3. Make a group and invite it

In the Freizone app:

1. Create a group. **A group only for this bot's messages**, not your team chat —
   see why in the [security model](../README.md#security-model). The short reason:
   whoever takes over the machine running this bot can read that group's *future*
   traffic.
2. Invite the address from step 2.
3. Note the group's id. **Whatever form you can get hold of will do** — the whole
   id, the hyphenated form, or just the short prefix the app shows in its compact
   `shortid*domain` rendering, with or without the server. The bot completes a
   prefix against the groups it is in, and tells you if one is ambiguous. See
   [Addressing recipients](../README.md#addressing-recipients).

You do not have to accept anything on the bot's behalf. **It accepts that
invitation itself**, because naming the group in its configuration is what asks
for it — and it works in either order, so inviting first and configuring
afterwards is fine.

## 4. Configure the route and start it for real

```sh
FREIZONE_BOT_SERVER=https://chat.example.org \
FREIZONE_BOT_STATE_DIR=./data \
FREIZONE_BOT_ROUTE_GROUP=pczu4-wslmx-3kcen-tudj9-s \
./freizone-bot run
```

It should log `joined a group` on this start, then `bot started`.

You can send to individual people instead of, or as well as, a group —
`FREIZONE_BOT_ROUTE_PEERS`, and the two are independent rather than
alternatives. See [Addressing recipients](../README.md#addressing-recipients).

## 5. Check that it works

Leave the daemon running and open a second terminal.

```sh
export FREIZONE_BOT_STATE_DIR=./data
./freizone-bot status
./freizone-bot send -title "Hello from the bot" "It works."
```

`status` prints the bot's address, `connected: true`, and `outbox: 0 waiting`.
The message should appear in the group on your phone within a second or two,
and `send` answers `queued for 1 destination(s)`.

Note that `status` and `send` need **no server address**: they talk to the
running daemon over its local socket, and the daemon owns the account. The CLI
never opens it — two processes writing one account's encryption state corrupt it.

If nothing arrives, in this order:

| Symptom | Look at |
| --- | --- |
| `no daemon at …` (exit 4) | the daemon is not running, or `FREIZONE_BOT_STATE_DIR` differs between the two terminals |
| `status` says `connected: false` | the daemon cannot reach the server — check the address and TLS |
| `send` returns but nothing arrives | `outbox:` in `status`; if it stays above 0, the daemon logs `delivery failed, will retry` with the reason |
| the group shows the bot as invited, not joined | the group id in `FREIZONE_BOT_ROUTE_GROUP` is not the group you invited it to |

## 6. Make it a service

### With systemd

Create a user and a configuration file first:

```sh
sudo useradd --system --home /var/lib/freizone-bot --shell /usr/sbin/nologin freizone-bot
sudo install -d -m 0750 -o root -g freizone-bot /etc/freizone-bot
sudo install -m 0640 -o root -g freizone-bot /dev/null /etc/freizone-bot/env
```

The state directory itself is not created here: `StateDirectory=` in the unit
below makes systemd create and own it, which is the difference between a unit
that works and one that starts and then cannot write — `ProtectSystem=strict`
makes the filesystem read-only apart from what the unit explicitly asks for.

Move the account you registered in step 2 into place — systemd adopts an
existing directory rather than replacing it:

```sh
sudo mv ./data /var/lib/freizone-bot
sudo chown -R freizone-bot:freizone-bot /var/lib/freizone-bot
```

**Do not register again.** A second registration produces a *different*
address, and the invitation you sent in step 3 points at the old one.

`/etc/freizone-bot/env`:

```ini
FREIZONE_BOT_SERVER=https://chat.example.org
FREIZONE_BOT_STATE_DIR=/var/lib/freizone-bot
FREIZONE_BOT_CONTROL_SOCKET=/run/freizone-bot/control.sock
FREIZONE_BOT_ROUTE_GROUP=pczu4wslmx3kcentudj9s
```

`/etc/systemd/system/freizone-bot.service`:

```ini
[Unit]
Description=Freizone automation daemon
After=network-online.target
Wants=network-online.target
# In [Unit], not [Service] -- they moved there in systemd 229 and are silently
# ignored in the wrong section. A configuration error exits non-zero, and
# without a limit that becomes a restart loop that fills the journal instead of
# stopping to be noticed.
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
User=freizone-bot
Group=freizone-bot
EnvironmentFile=/etc/freizone-bot/env
ExecStart=/usr/local/bin/freizone-bot run

# systemd creates and owns both, which is what makes them writable at all under
# ProtectSystem=strict. 0700 on the state directory because it holds this
# account's private keys; the socket directory is the one place a group may be
# let in, and even that is opt-in.
StateDirectory=freizone-bot
StateDirectoryMode=0700
RuntimeDirectory=freizone-bot
RuntimeDirectoryMode=0750

Restart=on-failure
RestartSec=5

# This process holds long-lived private keys and takes input from a socket.
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
RestrictRealtime=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
CapabilityBoundingSet=
# Outbound HTTPS to its server, plus its own unix socket. Nothing else.
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now freizone-bot
journalctl -u freizone-bot -f
```

**Letting other users send.** The socket is reachable only by the daemon's own
user until you say otherwise. To let, say, a `freizone-ops` group send:

```ini
SupplementaryGroups=freizone-ops
Environment=FREIZONE_BOT_CONTROL_GROUP=freizone-ops
```

Both lines, and that is not redundant: the daemon has to *be* a member of the
group to hand it out, or the change fails with a permission error — loudly, on
purpose, because a group that was configured and silently not applied is worse
than one that was never configured.

### With Docker

```sh
docker run -d --name freizone-bot \
  --restart unless-stopped \
  -v freizone-bot-data:/data \
  -e FREIZONE_BOT_SERVER=https://chat.example.org \
  -e FREIZONE_BOT_ROUTE_GROUP=pczu4wslmx3kcentudj9s \
  ghcr.io/behringer24/freizone-bot:latest run
```

`FREIZONE_BOT_STATE_DIR` is already `/data` in the image, so the named volume is
all the state configuration needed. Register the same way as step 2, with
`docker run --rm -it` and the same volume, before starting it detached.

Sending from inside:

```sh
docker exec freizone-bot /freizone-bot send -title "Hello" "It works."
```

There is no shell in the image, which is why that invocation names the binary
directly. And if you want to send from the *host* instead — which you probably
do, for `systemd OnFailure=` — read
[If you containerise and still want host senders](../README.md#if-you-containerise-and-still-want-host-senders)
first: the obvious `docker exec` route means granting root-equivalent access to
whatever does the sending.

## 7. Now make it useful

Everything below is optional and independent. Each links to the reference.

**Page yourself when a service fails** — three lines, and every failing unit on
the machine reports itself:
[With systemd](../README.md#with-systemd).

**Accept messages over HTTP**, for anything that can only POST:
[The HTTP ingress](../README.md#the-http-ingress). Off by default, and it
refuses to start without a file naming who may use it.

**Let it answer you in chat** — `/status`, `/listrecipients`, `/routes`:
[Talking to it](../README.md#talking-to-it). Off until
`FREIZONE_BOT_COMMANDERS` names somebody, and that is fail-closed on purpose.

**Teach it new commands** without recompiling, from a file:
[Teaching it new commands](../README.md#teaching-it-new-commands).
[`actions.example.json`](actions.example.json) next to this file is a working
one to start from -- its `/weather` action answers from a keyless public
endpoint, so it does something the moment you point the setting at it. Copy it
somewhere of your own before editing, so your configuration is not a file in the
checkout waiting to be committed.

**Stop a storm from becoming a hundred notifications** —
`FREIZONE_BOT_DEDUP_WINDOW_MINUTES` and `FREIZONE_BOT_RATE_PER_MINUTE` in the
[configuration reference](../README.md#configuration-reference).

## On Windows

Same seven steps. What changes is how you set the environment, how it becomes a
service, and three things the bot cannot check for you.

### The commands

PowerShell has no `VAR=value command` prefix, so variables are set and then the
command runs:

```powershell
git clone https://github.com/behringer24/freizone-bot
cd freizone-bot
go build -o freizone-bot.exe ./cmd/bot

$env:FREIZONE_BOT_SERVER = "https://chat.example.org"
$env:FREIZONE_BOT_STATE_DIR = ".\data"
.\freizone-bot.exe run
```

Registration prints the same banner and exits the same way. Then the group, then
the route:

```powershell
$env:FREIZONE_BOT_ROUTE_GROUP = "pczu4-wslmx-3kcen-tudj9-s"
.\freizone-bot.exe run
```

**Checking it works has one Windows-specific trap.** `$env:` lives in the window
that set it, so a second PowerShell window has none of it — and the CLI then
looks for the socket somewhere else entirely and reports `no daemon at …`. Set it
again there:

```powershell
$env:FREIZONE_BOT_STATE_DIR = ".\data"
.\freizone-bot.exe status
.\freizone-bot.exe send -title "Hello from the bot" "It works."
```

Pipelines work as expected:

```powershell
Get-Content .\build.log -Tail 20 | .\freizone-bot.exe send -title "Build failed"
```

### Making it a service

**The bot is not a Windows service.** It implements no service control handler,
so `sc.exe create` will start it and then fail with *"did not respond to the
start request in a timely fashion"*. Three approaches that do work:

- **Task Scheduler** — the simplest. Trigger *At startup*, *Run whether user is
  logged on or not*, and under Settings, *restart the task if it fails*.
  Environment variables cannot be given to a task directly, so point it at a
  small `start.ps1` that sets them and then calls `run`.
- **NSSM or WinSW** — a service wrapper. This is the closest equivalent to the
  systemd unit above: a real service, restart on failure, log redirection.
- **Docker Desktop** and the image, in which case the container instructions
  above apply unchanged.

Use a state directory outside the repository — `C:\ProgramData\freizone-bot` is
the conventional place — and give the account the service runs as write access
to it.

### Three things that behave differently

- **The bot cannot check the permissions on its own state directory.** On Unix it
  refuses to start when the directory holding its private keys is readable by
  group or world. Windows permissions are ACLs, and the mode bits Go synthesises
  there say nothing about them, so that check is disabled rather than guessing —
  it would otherwise refuse to start on every machine while proving nothing.
  **Set the ACL yourself.** The same applies to the HTTP ingress's token file.
- **`FREIZONE_BOT_CONTROL_GROUP` is refused, not ignored.** Granting a group
  would be a promise the mode bits there cannot keep, and a setting that appears
  to grant access without granting it is worse than one that is plainly
  unavailable. Access to the socket is whatever its directory's ACL inherits.
  The refusal comes when the socket opens, which on a first run is *after*
  registration.
- **The control socket itself works normally.** Windows 10 and later have
  `AF_UNIX` and Go uses it, so nothing needs a second mechanism. A hardened
  equivalent would be a named pipe with an explicit security descriptor, which
  is `BOT-07` and not built.

**So: Windows is a development and testing platform for this bot, not a hardened
deployment target.** Fine for trying it out and for working on it. For something
running unattended with long-lived private keys, use Linux.

## Things that catch people out

- **Do not run two daemons on one state directory.** The second is refused, by
  design: two processes writing one account's encryption state corrupt it
  permanently. If you see `account directory … is open in another process`, that
  is the check working.
- **Do not copy the state directory to a second machine and run both.** The lock
  is a file lock on that directory, so it protects two processes on *one*
  machine and has nothing to say about a copy elsewhere.
- **Moving the bot means moving the state directory**, not registering again. A
  fresh registration gets a different address, and the group invitation points at
  the old one.
- **`send` exits with a number**, so a script can branch on it: `0` queued,
  `1` usage, `2` refused (no route, queue full), `3` daemon error, `4` no daemon,
  `5` timeout.
- **`send` reads standard input only when given no text argument.** Never both —
  reading stdin whenever it is not a terminal hangs forever under a service
  manager, where it is routinely an open pipe nobody closes.
- **An invitation to a group you did not configure is ignored**, not declined.
  Set `FREIZONE_BOT_ACCEPT_GROUP_INVITES=true` only if you want anybody who
  knows the address to decide what your bot is a member of.
