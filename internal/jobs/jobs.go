// Package jobs runs the background work: mail delivery from the durable outbox, payment expiry,
// periodic re-matching and retention. Every job is idempotent and reads its work from the
// database, so nothing essential lives only in a goroutine and a restart loses nothing.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/classy-cash/app/internal/mail"
	"github.com/classy-cash/app/internal/store"
)

// Runner owns the periodic tasks.
type Runner struct {
	Store  *store.Store
	Sender mail.Sender
	Log    *slog.Logger
	// Batch bounds mail work per tick (memory and relay rate).
	Batch int
}

// DispatchMail sends due outbox messages. With a capturing sender they are marked captured and
// never leave the host.
func (r *Runner) DispatchMail(ctx context.Context) (int, error) {
	batch := r.Batch
	if batch <= 0 {
		batch = 20
	}
	due, err := r.Store.DueMail(ctx, batch)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range due {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		if r.Sender == nil || r.Sender.Captures() {
			if err := r.Store.MarkMailDone(ctx, m.ID, store.MailCaptured); err != nil {
				return n, err
			}
			n++
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := r.Sender.Send(sendCtx, m.Message())
		cancel()
		if err != nil {
			var perm *mail.PermanentError
			permanent := errors.As(err, &perm)
			r.Log.Warn("mail delivery failed", "id", m.ID, "attempt", m.Attempts+1, "permanent", permanent, "err", err)
			if e := r.Store.MarkMailError(ctx, m.ID, err, permanent); e != nil {
				return n, e
			}
			continue
		}
		if err := r.Store.MarkMailDone(ctx, m.ID, store.MailSent); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Housekeeping performs the slower maintenance tasks once.
func (r *Runner) Housekeeping(ctx context.Context) {
	if n, err := r.Store.ExpirePending(ctx, 24*time.Hour); err != nil {
		r.Log.Error("expire payments", "err", err)
	} else if n > 0 {
		r.Log.Info("payments expired", "count", n)
	}
	if err := r.Store.PurgeExpired(ctx); err != nil {
		r.Log.Error("purge sessions", "err", err)
	}
	if err := r.Store.PurgeOldMail(ctx, 30*24*time.Hour); err != nil {
		r.Log.Error("purge mail", "err", err)
	}
	if err := r.Store.PurgeBankEventBodies(ctx, 90*24*time.Hour); err != nil {
		r.Log.Error("purge bank events", "err", err)
	}
}

// Run blocks until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	mailTick := time.NewTicker(10 * time.Second)
	matchTick := time.NewTicker(time.Minute)
	houseTick := time.NewTicker(5 * time.Minute)
	defer mailTick.Stop()
	defer matchTick.Stop()
	defer houseTick.Stop()
	r.Housekeeping(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-mailTick.C:
			if _, err := r.DispatchMail(ctx); err != nil && ctx.Err() == nil {
				r.Log.Error("dispatch mail", "err", err)
			}
		case <-matchTick.C:
			if n, err := r.Store.RematchRecent(ctx); err != nil && ctx.Err() == nil {
				r.Log.Error("rematch", "err", err)
			} else if n > 0 {
				r.Log.Info("transfers matched on retry", "count", n)
			}
		case <-houseTick.C:
			r.Housekeeping(ctx)
		}
	}
}
