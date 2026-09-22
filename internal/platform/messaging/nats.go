// Package messaging wraps the broker client. Nothing above this package knows
// which broker is in use, which is what keeps the choice reversible.
package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

// Client is a connection to the broker.
type Client struct {
	conn *nats.Conn
	cfg  config.NATSConfig
}

// Connect opens a connection and proves it works before returning.
//
// Reconnection is delegated to the client library with an unlimited retry
// budget: the outbox means a broker outage costs latency rather than data, so
// giving up on reconnection would be the only way to turn it into data loss.
func Connect(ctx context.Context, cfg config.NATSConfig) (*Client, error) {
	options := []nats.Option{
		nats.Name("fulcrum"),
		nats.Timeout(cfg.ConnectTimeout),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.RetryOnFailedConnect(true),
	}

	conn, err := nats.Connect(cfg.URL, options...)
	if err != nil {
		return nil, errs.Unavailable("the broker is not reachable", err)
	}

	client := &Client{conn: conn, cfg: cfg}
	if pingErr := client.Health(ctx); pingErr != nil {
		client.Close()
		return nil, pingErr
	}
	return client, nil
}

// Conn exposes the underlying connection to the publisher and the consumer,
// which need the JetStream context.
func (c *Client) Conn() *nats.Conn { return c.conn }

// Health reports whether the broker is currently reachable.
func (c *Client) Health(ctx context.Context) error {
	if c.conn == nil || !c.conn.IsConnected() {
		return errs.Unavailable("the broker is not connected", errors.New("connection is not established"))
	}

	deadline := c.cfg.ConnectTimeout
	if fromCtx, ok := ctx.Deadline(); ok {
		if remaining := time.Until(fromCtx); remaining > 0 && remaining < deadline {
			deadline = remaining
		}
	}
	if err := c.conn.FlushTimeout(deadline); err != nil {
		return errs.Unavailable("the broker did not answer", fmt.Errorf("flush: %w", err))
	}
	return nil
}

// Close drains and closes the connection. Draining lets in-flight publishes
// finish, which is the difference between a clean shutdown and a lost message.
func (c *Client) Close() {
	if c.conn == nil {
		return
	}
	if err := c.conn.Drain(); err != nil {
		c.conn.Close()
		return
	}
}
