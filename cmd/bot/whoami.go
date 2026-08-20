package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/behringer24/freizone-bot/internal/account"
	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-server/pkg/client"
)

// runWhoami prints this bot's own Freizone address.
//
// Reads the address file rather than opening the account, so it works while the
// daemon is running -- which is when somebody is most likely to want it, and
// when opening the account would be refused. Falls back to opening only when
// the file is not there, which is the case for an account registered before
// that file existed.
func runWhoami(args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if raw, err := os.ReadFile(cfg.AddressFile()); err == nil {
		fmt.Print(string(raw))
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", cfg.AddressFile(), err)
	}

	c, err := account.Open(cfg)
	if err != nil {
		return err
	}
	defer c.Close() //nolint:errcheck // nothing to do about a failed release on the way out

	id, err := c.Identity()
	if errors.Is(err, client.ErrNoIdentity) {
		return fmt.Errorf("this bot has no account yet -- start it once with `freizone-bot run`")
	}
	if err != nil {
		return err
	}
	// Write it now, so the next call takes the cheap path above.
	if err := account.WriteAddress(cfg, id); err != nil {
		return err
	}
	// Canonical rather than hyphenated: this goes into a pipe as often as onto a
	// screen, and a caller splitting on `*` should not also have to strip
	// separators.
	fmt.Printf("%s\n", id.Address())
	return nil
}
