// Package store holds all persistence and accounting rules. Every class-scoped query filters by
// class_id so classes and families stay isolated; callers pass the class they have authorised.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Domain errors surfaced to the web layer.
var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrInvalid   = errors.New("invalid input")
	ErrConflict  = errors.New("conflict")
)

// Payment statuses.
const (
	PaymentPending  = "pending"
	PaymentPaid     = "paid"
	PaymentFailed   = "failed"
	PaymentRefunded = "refunded"
	PaymentExpired  = "expired"

	SPPending = "pending"
	SPPartial = "partial"
	SPPaid    = "paid"
)

// Roles within a class.
const (
	RoleTreasurer     = "treasurer"
	RolePresident     = "president"
	RoleVicePresident = "vice_president"
	RoleParent        = "parent"
)

// Store is the application's data access and domain service.
type Store struct {
	DB      *sql.DB
	Loc     *time.Location
	BaseURL string // public URL used in e-mail links
	// AttachDir is where expense receipts are kept; empty disables attachments.
	AttachDir string
	// Now is injectable for tests.
	Now func() time.Time
}

// New wraps an open, migrated database.
func New(conn *sql.DB, loc *time.Location) *Store {
	return &Store{DB: conn, Loc: loc, Now: func() time.Time { return time.Now().UTC() }}
}

const timeLayout = time.RFC3339

func ts(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseNullTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// InTx runs fn in an IMMEDIATE transaction.
func (s *Store) InTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func idsToAny(ids []int64) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}

func wrapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func errf(sentinel error, format string, a ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, a...))
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }
