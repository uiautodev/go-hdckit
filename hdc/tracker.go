package hdc

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Tracker watches the set of connected targets, emitting additions and
// removals as they happen. It mirrors the polling behaviour of the original
// implementation (one `list targets` per second).
//
// A Tracker must be closed with Close to release its connection and goroutine.
type Tracker struct {
	conn   *Connection
	ctx    context.Context
	cancel context.CancelFunc

	added   chan string
	removed chan string
	errs    chan error

	closeOnce sync.Once
}

// TrackTargets starts tracking targets and returns a Tracker.
func (c *Client) TrackTargets(ctx context.Context) (*Tracker, error) {
	conn, err := c.connection(ctx, "")
	if err != nil {
		return nil, err
	}
	if err := conn.send(ctx, []byte("alive")); err != nil {
		conn.Close()
		return nil, err
	}

	tctx, cancel := context.WithCancel(ctx)
	tr := &Tracker{
		conn:    conn,
		ctx:     tctx,
		cancel:  cancel,
		added:   make(chan string),
		removed: make(chan string),
		errs:    make(chan error, 1),
	}
	go tr.loop()
	return tr, nil
}

// Added returns a channel that yields newly connected targets.
func (t *Tracker) Added() <-chan string { return t.added }

// Removed returns a channel that yields disconnected targets.
func (t *Tracker) Removed() <-chan string { return t.removed }

// Errors returns a channel that yields a non-nil error when tracking stops
// unexpectedly.
func (t *Tracker) Errors() <-chan error { return t.errs }

// Close stops tracking and releases the underlying connection.
func (t *Tracker) Close() {
	t.closeOnce.Do(t.cancel)
}

func (t *Tracker) loop() {
	defer close(t.added)
	defer close(t.removed)
	defer close(t.errs)
	defer t.conn.Close()

	current := []string{}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		if err := t.conn.send(t.ctx, []byte("list targets")); err != nil {
			t.fail(err)
			return
		}
		data, err := t.conn.readValue(t.ctx)
		if err != nil {
			t.fail(err)
			return
		}

		current = t.update(current, parseTargets(string(data)))

		select {
		case <-t.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (t *Tracker) update(current, next []string) []string {
	for _, target := range next {
		if !slices.Contains(current, target) {
			select {
			case t.added <- target:
			case <-t.ctx.Done():
				return current
			}
		}
	}
	for _, target := range current {
		if !slices.Contains(next, target) {
			select {
			case t.removed <- target:
			case <-t.ctx.Done():
				return current
			}
		}
	}
	return next
}

func (t *Tracker) fail(err error) {
	if t.ctx.Err() != nil {
		return
	}
	select {
	case t.errs <- err:
	default:
	}
}
