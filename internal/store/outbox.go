package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/classy-cash/app/internal/mail"
)

// Outbox statuses.
const (
	MailPending  = "pending"
	MailSent     = "sent"
	MailFailed   = "failed"
	MailCaptured = "captured"
)

// maxMailAttempts bounds retries before a message is parked as failed.
const maxMailAttempts = 12

// OutboxMail is a stored outgoing message.
type OutboxMail struct {
	ID        int64
	Key       string
	ToEmail   string
	ToName    string
	ReplyTo   string
	Subject   string
	HTML      string
	Text      string
	Status    string
	Attempts  int
	LastError string
	CreatedAt time.Time
	SentAt    *time.Time
}

// Message converts to the sender's type.
func (m OutboxMail) Message() mail.Message {
	return mail.Message{Key: m.Key, ToEmail: m.ToEmail, ToName: m.ToName, ReplyTo: m.ReplyTo, Subject: m.Subject, HTML: m.HTML, Text: m.Text}
}

func enqueue(ctx context.Context, x querier, now time.Time, m mail.Message) error {
	if m.ToEmail == "" || m.Key == "" {
		return nil
	}
	_, err := x.ExecContext(ctx, `INSERT OR IGNORE INTO outbox (idempotency_key, to_email, to_name, reply_to, subject, html_body, text_body, status, next_attempt_at, created_at)
		VALUES (?,?,?,?,?,?,?, 'pending', ?, ?)`, m.Key, m.ToEmail, m.ToName, m.ReplyTo, m.Subject, m.HTML, m.Text, ts(now), ts(now))
	return err
}

// Enqueue stores a message for delivery; a repeated Key is ignored (idempotent).
func (s *Store) Enqueue(ctx context.Context, m mail.Message) error {
	return enqueue(ctx, s.DB, s.Now(), m)
}

// Branding returns template branding for the installation.
func (s *Store) Branding(ctx context.Context) mail.Branding {
	return mail.Branding{AppName: s.AppName(ctx), BaseURL: s.BaseURL}
}

func (s *Store) brandingTx(ctx context.Context, x querier) mail.Branding {
	name := "Classy Cash"
	_ = x.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'app_name'`).Scan(&name)
	return mail.Branding{AppName: name, BaseURL: s.BaseURL}
}

// DueMail returns pending messages whose retry time has come.
func (s *Store) DueMail(ctx context.Context, limit int) ([]OutboxMail, error) {
	return s.queryOutbox(ctx, `WHERE status = 'pending' AND next_attempt_at <= ? ORDER BY id LIMIT ?`, ts(s.Now()), limit)
}

// RecentMail lists the newest messages (admin preview of captured mail).
func (s *Store) RecentMail(ctx context.Context, limit int) ([]OutboxMail, error) {
	return s.queryOutbox(ctx, `ORDER BY id DESC LIMIT ?`, limit)
}

// MailByID loads one stored message.
func (s *Store) MailByID(ctx context.Context, id int64) (OutboxMail, error) {
	list, err := s.queryOutbox(ctx, `WHERE id = ?`, id)
	if err != nil {
		return OutboxMail{}, err
	}
	if len(list) == 0 {
		return OutboxMail{}, ErrNotFound
	}
	return list[0], nil
}

func (s *Store) queryOutbox(ctx context.Context, tail string, args ...any) ([]OutboxMail, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, idempotency_key, to_email, to_name, reply_to, subject, html_body, text_body, status, attempts, last_error, created_at, sent_at FROM outbox `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxMail
	for rows.Next() {
		var m OutboxMail
		var created string
		var sent sql.NullString
		if err := rows.Scan(&m.ID, &m.Key, &m.ToEmail, &m.ToName, &m.ReplyTo, &m.Subject, &m.HTML, &m.Text, &m.Status, &m.Attempts, &m.LastError, &created, &sent); err != nil {
			return nil, err
		}
		m.CreatedAt, m.SentAt = parseTime(created), parseNullTime(sent)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkMailDone records a final state: sent or captured.
func (s *Store) MarkMailDone(ctx context.Context, id int64, status string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE outbox SET status = ?, sent_at = ?, last_error = '', attempts = attempts + 1 WHERE id = ?`, status, ts(s.Now()), id)
	return err
}

// MarkMailError schedules a retry with exponential backoff (30 s … 1 h), or parks the message.
func (s *Store) MarkMailError(ctx context.Context, id int64, cause error, permanent bool) error {
	var attempts int
	if err := s.DB.QueryRowContext(ctx, `SELECT attempts FROM outbox WHERE id = ?`, id).Scan(&attempts); err != nil {
		return err
	}
	attempts++
	if permanent || attempts >= maxMailAttempts {
		_, err := s.DB.ExecContext(ctx, `UPDATE outbox SET status = 'failed', attempts = ?, last_error = ? WHERE id = ?`, attempts, truncate(cause.Error(), 300), id)
		return err
	}
	delay := 30 * time.Second << min(attempts-1, 7)
	if delay > time.Hour {
		delay = time.Hour
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE outbox SET attempts = ?, last_error = ?, next_attempt_at = ? WHERE id = ?`, attempts, truncate(cause.Error(), 300), ts(s.Now().Add(delay)), id)
	return err
}

// OutboxStats feeds /ready and monitoring.
type OutboxStats struct {
	Pending, Failed int
	OldestPending   time.Duration
}

func (s *Store) OutboxStats(ctx context.Context) (OutboxStats, error) {
	var st OutboxStats
	var oldest sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='pending'),0), COALESCE(SUM(status='failed'),0), MIN(CASE WHEN status='pending' THEN created_at END) FROM outbox`).Scan(&st.Pending, &st.Failed, &oldest)
	if t := parseNullTime(oldest); t != nil {
		st.OldestPending = s.Now().Sub(*t)
	}
	return st, err
}

// PurgeOldMail drops delivered/captured mail bodies after the retention window.
func (s *Store) PurgeOldMail(ctx context.Context, keep time.Duration) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM outbox WHERE status IN ('sent','captured') AND created_at < ?`, ts(s.Now().Add(-keep)))
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// queueSettlementMail composes the payer confirmation and the treasurer notice for a settled payment.
func (s *Store) queueSettlementMail(ctx context.Context, tx *sql.Tx, classID, paymentID int64) error {
	p, err := s.loadPayment(ctx, tx, classID, paymentID)
	if err != nil || len(p.Items) == 0 {
		return err
	}
	b := s.brandingTx(ctx, tx)
	var className string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM classes WHERE id = ?`, classID).Scan(&className); err != nil {
		return err
	}
	var treasurer User
	treasurerFound := false
	if rows, err := queryUsers(ctx, tx, `SELECT u.id, u.email, u.name, u.phone, u.password_hash, u.is_admin, u.session_epoch, u.created_at, u.confirmed_at
		FROM users u JOIN memberships m ON m.user_id = u.id WHERE m.class_id = ? AND m.role = 'treasurer'`, classID); err == nil && len(rows) > 0 {
		treasurer, treasurerFound = rows[0], true
	}
	items := make([]mail.SettlementItem, 0, len(p.Items))
	for _, sp := range p.Items {
		var total, paid int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(status='paid'),0) FROM student_payments WHERE class_id = ? AND label = ?`, classID, sp.Label).Scan(&total, &paid); err != nil {
			return err
		}
		items = append(items, mail.SettlementItem{Student: sp.StudentName, Label: sp.Label, AmountMinor: sp.AmountMinor, Paid: paid, Total: total, PaidAt: sp.PaidAt, DueAt: sp.DueAt})
	}
	// Payer confirmation (skipped when the treasurer paid on the parent's behalf).
	if !treasurerFound || treasurer.ID != p.UserID {
		var prev []mail.PreviousPayment
		var sumPrev int64
		rows, err := tx.QueryContext(ctx, `SELECT COALESCE(paid_at, created_at), amount_minor FROM payments WHERE user_id = ? AND status = 'paid' AND id <> ? ORDER BY COALESCE(paid_at, created_at) DESC LIMIT 10`, p.UserID, paymentID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var d string
			var a int64
			if err := rows.Scan(&d, &a); err != nil {
				rows.Close()
				return err
			}
			prev = append(prev, mail.PreviousPayment{Date: parseTime(d), AmountMinor: a})
			sumPrev += a
		}
		rows.Close()
		r := mail.SettlementConfirmation(b, mail.SettlementData{Items: items, Previous: prev, SumPrevious: sumPrev, SumIncludingThis: sumPrev + p.AmountMinor}, s.Loc)
		if err := enqueue(ctx, tx, s.Now(), mail.Message{Key: fmt.Sprintf("settled-%d-payer", paymentID), ToEmail: p.UserEmail, ToName: p.UserName, Subject: r.Subject, HTML: r.HTML, Text: r.Text}); err != nil {
			return err
		}
	}
	if treasurerFound {
		var missing int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM student_payments WHERE class_id = ? AND status IN ('pending','partial')`, classID).Scan(&missing); err != nil {
			return err
		}
		r := mail.TreasurerPayment(b, mail.TreasurerPaymentData{ClassName: className, ParentName: p.UserName, ParentEmail: p.UserEmail, Items: items, MissingMinor: missing}, s.Loc)
		return enqueue(ctx, tx, s.Now(), mail.Message{Key: fmt.Sprintf("settled-%d-treasurer", paymentID), ToEmail: treasurer.Email, ToName: treasurer.Name, Subject: r.Subject, HTML: r.HTML, Text: r.Text})
	}
	return nil
}
