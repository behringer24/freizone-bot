# Contributing to Freizone Bot

Bug reports and pull requests are welcome. This file covers the practical side,
plus one thing worth stating up front: contributions require a contributor
licence agreement, and the reason for it is spelled out
[below](#contributor-licence-agreement) rather than left to be discovered.

## What this repository is for

The bot is a **product an operator runs**, not a library to program against, and
not a place where protocol logic lives. It holds a Freizone account and connects
Freizone to other systems in both directions: something happens elsewhere and
becomes a message, or a message causes something to happen.

That shapes what belongs here. The Freizone protocol itself — decryption,
session handling, deduplication, retry semantics, group convergence — lives in
freizone-server's [`pkg/client`](https://github.com/behringer24/freizone-server/tree/master/pkg/client),
and a pull request that reimplements any of it here will be closed with a
pointer there. That separation is not bureaucracy: having the protocol
implemented twice is precisely the problem `pkg/client` was extracted to end,
and re-creating it one layer up would be the same mistake under a different
name. If the bot needs something the core does not offer, the fix belongs
upstream.

## Before you open a pull request

- **Open an issue first for anything non-trivial**, and especially for anything
  touching the control channel, the account-ownership lock, or the
  authorization path. Those three are where a mistake is quiet and expensive.
- **Any change that lets attacker-controlled text reach an action must be
  argued before it is written.** Incoming messages can come from anyone who
  knows the bot's address. The rule that authorization runs *before*
  interpretation is not a style preference; it is what keeps a future
  model-driven interpreter from being promptable by strangers.
- **Keep the build clean:**

  ```sh
  go build ./...
  go vet ./...
  go test ./...
  ```

  All three should pass before you push. There is no CI beyond the release
  image build, so these are run by hand.

- **Match the surrounding code.** Comments here explain *why*, not what.
- **Security.** Please do not open a public issue for a vulnerability in the
  control channel, the authorization path or the account lock. Report it
  privately to <info@behringer24.de> first.

## Contributor licence agreement

Every non-trivial contribution to this repository requires that you accept the
agreement below. It is short, and it does one thing the AGPL alone does not: it
lets the copyright in the codebase stay undivided, so the licence can still be
changed later without having to track down and ask every person who ever
contributed.

### Why this project asks for it

Mostly consistency. The Freizone repositories — server, gateway, the Android app
and this bot — are held under the same terms so that code can move between them
without a licensing question each time, and so that a decision about one is not
quietly foreclosed by an oversight in another. The substantive reasons live in
the [server](https://github.com/behringer24/freizone-server/blob/master/CONTRIBUTING.md)
and [app](https://github.com/behringer24/freizone-app/blob/master/CONTRIBUTING.md)
repositories; there is no licence change planned for the bot itself.

Worth knowing either way: running your own bot never requires anyone's
permission. It registers its own account against whichever Freizone server you
point it at, and there is no registration step with anybody else. That stays
true whatever happens to the licence.

### What you keep

You keep the copyright in your own work, and an unrestricted right to use it
however you like, including in other projects and under other licences. This
agreement takes nothing away from you; it adds a permission for the maintainer.

### The agreement

By submitting a contribution to this repository, you agree to the following,
for that contribution and for any future contribution you make here:

1. **Grant of rights.** You grant Andreas Behringer an exclusive, worldwide,
   perpetual, irrevocable, transferable and sublicensable right to use,
   reproduce, modify, adapt, translate, publish, distribute, and otherwise
   exploit your contribution, in whole or in part, alone or combined with other
   work, in any form and by any means whether known today or developed later,
   and to license it to third parties under any terms — including terms
   differing from the licence this project uses at the time you contribute.
   Where the applicable law does not permit copyright itself to be transferred
   (as under German law, § 29 UrhG), this is a grant of exclusive rights of use
   to the fullest extent that law permits.
2. **Licence back to you.** You retain a non-exclusive, worldwide, perpetual,
   irrevocable right to use, publish and license your own contribution for any
   purpose, under any terms, without restriction and without needing anyone's
   permission.
3. **You are entitled to grant this.** You confirm that the contribution is
   your own work, that you have the right to grant the rights above, and that
   it does not knowingly infringe anyone else's rights. If you wrote it in the
   course of employment or under a contract that might assign rights to someone
   else, you confirm that your employer or client has agreed to this, or that
   the contribution falls unambiguously outside that scope. (Under German law,
   rights in software written by an employee in the course of their duties pass
   to the employer automatically — § 69b UrhG — so this matters more often than
   people expect.)
4. **Patents.** To the extent your contribution is covered by a patent you own
   or control, you grant a perpetual, worldwide, non-exclusive, royalty-free
   licence to that patent, as far as is necessary to use, distribute and
   sublicense your contribution as part of this project.
5. **No warranty.** Your contribution is provided as-is. Except where the law
   provides otherwise, you give no warranty and accept no liability for it.

### Small changes

Fixing a typo, rewording a comment, correcting a broken link or reformatting
existing code does not need this. There is no original creative content in such
a change, so there is nothing to license.

### How to accept it

Include this line in the description of your pull request:

> I have read CONTRIBUTING.md and I accept the Contributor Licence Agreement in
> it, for this and all my future contributions to this repository.

Your GitHub account and the pull request itself are the record. If a signing
bot is set up later, it will replace this step; anything accepted this way
stays valid.

If you are contributing on behalf of a company, or anything above does not fit
your situation, get in touch before you start — open an issue, or write to
<info@behringer24.de>. It is far easier to sort out beforehand.
