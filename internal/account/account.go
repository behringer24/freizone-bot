// Package account owns this bot's Freizone identity: opening the account
// directory, and getting an account to exist in the first place.
//
// Everything here is a thin layer over freizone-server's pkg/client. What it
// adds is the part that is the bot's business rather than the protocol's: which
// directory, what to do when it is already taken, and what an operator has to
// be told once an identity exists.
package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-server/pkg/address"
	"github.com/behringer24/freizone-server/pkg/client"
)

// Open takes ownership of the bot's account directory.
//
// The ownership is the point. pkg/client refuses a second process outright and
// hands a second opener *in this process* the same client, because two clients
// over one directory corrupt each other's ratchet state -- see that package's
// lock.go. A bot is where that would otherwise happen most easily: a one-shot
// CLI invocation from a cron job, next to a daemon that has been running for
// weeks.
//
// Released by [client.Client.Close], which the caller owes it.
func Open(cfg *config.Config) (*client.Client, error) {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the state directory: %w", err)
	}
	if err := checkPrivate(cfg.AccountDir()); err != nil {
		return nil, err
	}

	c, err := client.OpenWith(cfg.AccountDir(), client.Options{MediaPath: cfg.MediaDir()})
	if err != nil {
		var inUse *client.ErrAccountInUse
		if errors.As(err, &inUse) {
			// The core's own message already states the fact and names the
			// directory, so this adds only what it cannot know: that the holder
			// is almost certainly the operator's own daemon, and that the way
			// in is the control socket. Wrapped rather than replaced so a
			// caller can still recognise the condition -- BOT-04's standalone
			// mode has to tell it apart from a real failure.
			return nil, fmt.Errorf(
				"%w -- if that is your own daemon, talk to it over the control socket "+
					"rather than opening the account directly", err)
		}
		return nil, err
	}
	return c, nil
}

// checkPrivate refuses to start when an existing account directory is readable
// by anyone but its owner.
//
// The identity is on disk in the clear, and no design changes that: there is
// nobody to type a passphrase at three in the morning. So the perimeter is the
// filesystem, and a directory that has been loosened -- by a careless chmod, by
// a restore from an archive that did not keep modes, by a volume mounted with
// the wrong options -- is worth stopping for rather than starting quietly on
// top of.
//
// pkg/client creates the directory 0700; this is about the ones it did not
// create.
func checkPrivate(dir string) error {
	if runtime.GOOS == "windows" {
		// Windows permissions are ACLs, and the mode bits os.Stat synthesises
		// there say nothing about them -- a check against those would refuse to
		// start on every development machine while proving nothing.
		return nil
	}
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing there yet; pkg/client will create it 0700
	}
	if err != nil {
		return fmt.Errorf("checking the account directory: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf(
			"the account directory %s is mode %#o and holds this bot's private keys: "+
				"only its own user may read it (chmod 700)", dir, perm)
	}
	return nil
}

// EnsureRegistered makes sure this bot has an account, registering one on the
// first run. The bool reports whether an account was created just now.
//
// Safe to call on every start, and safe after a crash mid-registration:
// pkg/client resumes an interrupted attempt rather than creating a second
// account under a different address (see its register.go). That matters more
// here than anywhere else -- an operator has already been told the bot's
// address and may have invited it to a group, and a bot that comes back under
// a different one looks like it is working while reaching nobody.
func EnsureRegistered(ctx context.Context, c *client.Client, cfg *config.Config, logger *slog.Logger) (client.Identity, bool, error) {
	switch id, err := c.Identity(); {
	case err == nil:
		// An account belongs to the server it was registered on: its address is
		// `id*server`, its keys are published there, and nothing can move it.
		// So a configured server that disagrees with the stored one is not a
		// change this can carry out -- and carrying on with the stored value
		// would be worse than either answer, because the operator would see
		// "account ready" and keep talking to the server they just stopped
		// naming.
		//
		// SameServer rather than a string comparison, so adding or dropping the
		// default scheme is not mistaken for a move.
		if !address.SameServer(id.Server, cfg.Server) {
			return client.Identity{}, false, fmt.Errorf(
				"this account lives on %s, but %s says %s. An account cannot move between servers: "+
					"either put the first one back, or point %s at a fresh directory to register a "+
					"second account there -- which gets a new address, so it has to be invited again",
				id.Server, envServerName, cfg.Server, envStateDirName)
		}
		return id, false, nil
	case !errors.Is(err, client.ErrNoIdentity):
		return client.Identity{}, false, fmt.Errorf("reading the identity: %w", err)
	}

	logger.Info("no account yet, registering", "server", cfg.Server)
	id, err := c.Register(ctx, cfg.Server, client.RegisterOptions{InviteCode: cfg.InviteCode})
	if err != nil {
		return client.Identity{}, false, fmt.Errorf("registering on %s: %w", cfg.Server, err)
	}

	// Publishing prekeys is deliberately not part of registering (see
	// pkg/client), but a bot that never does it cannot be written to first --
	// which for an alerting bot in a group is exactly what happens.
	if err := c.RotatePrekeys(ctx); err != nil {
		return id, true, fmt.Errorf("publishing prekeys: %w", err)
	}
	if err := c.TopUpOneTimePrekeys(ctx); err != nil {
		return id, true, fmt.Errorf("filling the prekey pool: %w", err)
	}
	return id, true, nil
}

// WriteAddress records the bot's address as a plain file next to its state.
//
// The one fact an operator has to act on: they cannot invite the bot anywhere
// without it. Logged too, but a log line scrolls away and a container's first
// start is often watched by nobody -- so it is also somewhere they can read it
// later without knowing any commands.
func WriteAddress(cfg *config.Config, id client.Identity) error {
	path := cfg.AddressFile()
	line := id.Address().String() + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// The two variable names this package puts into messages. Named here rather
// than imported from config, which would be a dependency in the wrong
// direction -- config knows nothing about accounts and should not have to.
const (
	envServerName   = "FREIZONE_BOT_SERVER"
	envStateDirName = "FREIZONE_BOT_STATE_DIR"
)
