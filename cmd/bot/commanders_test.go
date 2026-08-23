package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/behringer24/freizone-server/pkg/address"
	"github.com/behringer24/freizone-server/pkg/client"
)

func daemonForCommanders(t *testing.T, configured string) *daemon {
	t.Helper()
	d := daemonWith(t, map[string]string{"FREIZONE_BOT_COMMANDERS": configured})
	d.logger = slog.New(slog.DiscardHandler)
	return d
}

// A whole id needs no lookup, which is what keeps a bot configured with whole
// ids able to start while its server is unreachable.
func TestAWholeCommanderIDNeedsNoLookup(t *testing.T) {
	d := daemonForCommanders(t, address.FormatForDisplay(somePeer))
	d.resolvePeer = func(context.Context, string, string) (client.PeerEndpoint, error) {
		t.Error("a whole id must not be looked up")
		return client.PeerEndpoint{}, nil
	}

	got, err := d.resolveCommanders(context.Background())
	if err != nil {
		t.Fatalf("resolveCommanders: %v", err)
	}
	if len(got) != 1 || got[0] != somePeer {
		t.Errorf("got %v, want the canonical id", got)
	}
}

// A prefix is resolved once, into the one account it names, so the check that
// uses it still compares exact ids -- never a prefix against a sender.
func TestACommanderPrefixIsResolvedToAnExactID(t *testing.T) {
	d := daemonForCommanders(t, somePeer[:address.PrefixLength]+"*chat.example.org")

	var askedID, askedServer string
	d.resolvePeer = func(_ context.Context, id, server string) (client.PeerEndpoint, error) {
		askedID, askedServer = id, server
		return client.PeerEndpoint{AccountID: somePeer}, nil
	}

	got, err := d.resolveCommanders(context.Background())
	if err != nil {
		t.Fatalf("resolveCommanders: %v", err)
	}
	if len(got) != 1 || got[0] != somePeer {
		t.Errorf("got %v, want the exact id", got)
	}
	if askedID != somePeer[:address.PrefixLength] {
		t.Errorf("asked about %q", askedID)
	}
	// Against the server the entry named, because prefix uniqueness is per
	// server: asking the wrong one could authorise a different account that
	// happens to start the same way.
	if askedServer != "https://chat.example.org" {
		t.Errorf("asked server %q", askedServer)
	}
}

// Fatal, not skipped. An allow-list that quietly lost an entry is a bot that
// silently answers nobody -- and since an unauthorised sender gets no reply by
// design, that would be invisible from outside.
func TestAnUnresolvableCommanderStopsTheDaemon(t *testing.T) {
	d := daemonForCommanders(t, somePeer[:address.PrefixLength])
	d.resolvePeer = func(context.Context, string, string) (client.PeerEndpoint, error) {
		return client.PeerEndpoint{}, errors.New("404 not found")
	}

	_, err := d.resolveCommanders(context.Background())
	if err == nil {
		t.Fatal("an allow-list entry that cannot be resolved must stop the start")
	}
	// The message has to separate the two causes and offer the way round it,
	// since one of them is a server being down and the other is a typo.
	for _, want := range []string{"names no account", "could not be reached", "whole id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q: %q", want, err)
		}
	}
}

func TestNoCommandersIsNothingToResolve(t *testing.T) {
	d := daemonForCommanders(t, "")
	d.resolvePeer = func(context.Context, string, string) (client.PeerEndpoint, error) {
		t.Error("nothing should be looked up")
		return client.PeerEndpoint{}, nil
	}
	got, err := d.resolveCommanders(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}
