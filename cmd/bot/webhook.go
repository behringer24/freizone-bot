package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/behringer24/freizone-bot/internal/outbound"
	"github.com/behringer24/freizone-bot/internal/outbox"
	"github.com/behringer24/freizone-bot/internal/webhook"
)

// Accept adapts the daemon to the webhook's view of it: hand over a message,
// hear whether it was queued.
//
// The daemon's own accept does the work -- routes, deduplication, the rate cap,
// the outbox -- so the ingress owns none of that and cannot drift from what the
// control socket does. It only translates the two failures the caller of an HTTP
// request can act on.
func (d *daemon) Accept(title, text, route, dedupKey string, labels map[string]string) (int, string, error) {
	result, err := d.accept(outbound.Message{
		Title:  title,
		Text:   text,
		Labels: labels,
	}, route, dedupKey)
	switch {
	case errors.Is(err, outbound.ErrNoRoute):
		return 0, "", fmt.Errorf("%w: %s", webhook.ErrNoRoute, err)
	case errors.Is(err, outbox.ErrFull):
		return 0, "", fmt.Errorf("%w: %s", webhook.ErrFull, err)
	case err != nil:
		return 0, "", err
	}
	return len(result.IDs), result.SuppressedBy, nil
}

// serveWebhook starts the HTTP ingress, or does nothing if none is configured.
//
// Returns a stop function rather than relying on the context, because the
// shutdown *order* matters and the caller owns it: the ingress closes before
// the outbox is drained, so nothing new is accepted after the decision to stop
// has been taken. Accepting a message and then discarding it is the one thing a
// bridge must never do quietly.
func serveWebhook(d *daemon) (stop func(), err error) {
	if d.cfg.WebhookAddr == "" {
		return func() {}, nil
	}

	tokens, err := webhook.LoadTokens(d.cfg.WebhookTokens)
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", d.cfg.WebhookAddr)
	if err != nil {
		return nil, fmt.Errorf("opening the webhook listener: %w", err)
	}

	srv := webhook.Server(webhook.New(d, tokens, d.logger), d.cfg.WebhookAddr)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.logger.Error("webhook listener stopped", "error", err)
		}
	}()

	// Named at info level with the number of senders, because this is the one
	// property of the bot that changes character by being configured: from here
	// on, this process accepts input from the network.
	d.logger.Info("webhook ingress listening",
		"addr", ln.Addr().String(), "path", webhook.Path, "senders", len(tokens))

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			d.logger.Warn("webhook listener did not close cleanly", "error", err)
		}
	}, nil
}
