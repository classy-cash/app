package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/classy-cash/app/internal/db"
	"github.com/classy-cash/app/internal/mail"
	"github.com/classy-cash/app/internal/store"
	"github.com/classy-cash/app/migrations"
)

type fakeSender struct {
	captures bool
	err      error
	sent     []mail.Message
}

func (f *fakeSender) Send(_ context.Context, m mail.Message) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}
func (f *fakeSender) Captures() bool { return f.captures }

func setup(t *testing.T) (*store.Store, *time.Time) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, migrations.FS); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Warsaw")
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	s := store.New(conn, loc)
	s.Now = func() time.Time { return now }
	return s, &now
}

func TestCapturedMailNeverReachesTheSender(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	_ = s.Enqueue(ctx, mail.Message{Key: "k1", ToEmail: "a@example.com", Subject: "s", HTML: "h", Text: "t"})
	fs := &fakeSender{captures: true}
	r := &Runner{Store: s, Sender: fs, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n, err := r.DispatchMail(ctx); err != nil || n != 1 || len(fs.sent) != 0 {
		t.Fatalf("n=%d err=%v sent=%d", n, err, len(fs.sent))
	}
	list, _ := s.RecentMail(ctx, 5)
	if list[0].Status != store.MailCaptured {
		t.Fatalf("status %s", list[0].Status)
	}
}

func TestRelayFailureRetriesThenSucceeds(t *testing.T) {
	s, now := setup(t)
	ctx := context.Background()
	_ = s.Enqueue(ctx, mail.Message{Key: "k2", ToEmail: "a@example.com", Subject: "s", HTML: "h", Text: "t"})
	fs := &fakeSender{err: errors.New("relay down")}
	r := &Runner{Store: s, Sender: fs, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n, _ := r.DispatchMail(ctx); n != 0 {
		t.Fatal("nothing should be delivered")
	}
	if n, _ := r.DispatchMail(ctx); n != 0 {
		t.Fatal("must back off before retrying")
	}
	*now = now.Add(time.Minute)
	fs.err = nil
	if n, _ := r.DispatchMail(ctx); n != 1 || len(fs.sent) != 1 || fs.sent[0].Key != "k2" {
		t.Fatalf("retry failed: %+v", fs.sent)
	}
}

func TestPermanentRejectionParksMessage(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	_ = s.Enqueue(ctx, mail.Message{Key: "k3", ToEmail: "a@example.com", Subject: "s", HTML: "h", Text: "t"})
	r := &Runner{Store: s, Sender: &fakeSender{err: &mail.PermanentError{Msg: "bad recipient"}}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, _ = r.DispatchMail(ctx)
	if st, _ := s.OutboxStats(ctx); st.Failed != 1 {
		t.Fatalf("stats %+v", st)
	}
}
