package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/alior"
	"github.com/classy-cash/app/internal/auth"
	"github.com/classy-cash/app/internal/money"
)

// Bank-pipeline tests. The treasurer creates a reference, makes one small REAL transfer whose title carries
// it, and watches the status flip from pending to success when the bank's notification arrives through the
// Worker. A reference is a diagnostic record, not a payment: processEvent hands every notification that carries
// a known marker to interceptBankTest before any transfer is stored or matched, so it can never settle an
// obligation, appear in the transfer list, notify the administrators or touch a ledger — whether it is the first
// delivery, a retry under a new id, a late arrival after expiry or cancellation, or a replay.

const (
	// BankTestPrefix starts every marker. Payment codes are 4-6 characters, so no code can contain it.
	BankTestPrefix = "CCTEST"
	// BankTestTTL is how long a reference stays open. Interbank transfers can take a banking session or two.
	BankTestTTL = 48 * time.Hour
	// MaxBankTestMinor caps the expected amount: this is a diagnostic, not a payment.
	MaxBankTestMinor = 1000
	// MaxOpenBankTests caps simultaneously open references per class.
	MaxOpenBankTests = 3

	bankTestAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // no 0/O, 1/I/L: unambiguous when typed or read aloud
	bankTestRandom   = 8

	// Bank event statuses of intercepted deliveries (never "parsed", so they do not count as imports).
	EventTest         = "test"
	EventTestUnparsed = "test_unparsed"
)

// OutcomeTest is the ingest outcome of a delivery that carried a diagnostic marker.
const OutcomeTest = "test"

// Bank test states as shown to the user.
const (
	BankTestPending   = "pending"
	BankTestSuccess   = "success"
	BankTestExpired   = "expired"
	BankTestCancelled = "cancelled"
)

// BankTest is one diagnostic reference.
type BankTest struct {
	ID            int64
	ClassID       int64
	Marker        string // compact form, CCTEST + 8 characters
	ExpectedMinor int64
	Stored        string // pending | success | cancelled
	CreatedAt     time.Time
	ExpiresAt     time.Time
	CancelledAt   *time.Time
	CreatedByName string
	EventID       string
	ObservedAt    *time.Time
	ObservedMinor *int64
	Parsed        bool // the observation was a recognised credit notification
	Hits          int
}

// Title is the marker as the user types it into the transfer title.
func (t BankTest) Title() string {
	m := t.Marker
	if len(m) != len(BankTestPrefix)+bankTestRandom {
		return m
	}
	r := m[len(BankTestPrefix):]
	return BankTestPrefix + "-" + r[:4] + "-" + r[4:]
}

// State is the status at the given moment. Expiry is derived from the clock, never from a background job, so
// there is no window in which a stale "pending" can be settled by a race.
func (t BankTest) State(now time.Time) string {
	switch {
	case t.Stored == BankTestSuccess:
		return BankTestSuccess
	case t.Stored == BankTestCancelled:
		return BankTestCancelled
	case !now.Before(t.ExpiresAt):
		return BankTestExpired
	}
	return BankTestPending
}

// Open reports whether the reference can still turn into a success.
func (t BankTest) Open(now time.Time) bool { return t.State(now) == BankTestPending }

// Seen reports that some notification with the marker has arrived.
func (t BankTest) Seen() bool { return t.Hits > 0 }

// AmountDiffers reports a successful observation whose amount is not the expected one.
func (t BankTest) AmountDiffers() bool {
	return t.Parsed && t.ObservedMinor != nil && *t.ObservedMinor != t.ExpectedMinor
}

const bankTestCols = `t.id, t.class_id, t.marker, t.expected_minor, t.status, t.created_at, t.expires_at, t.cancelled_at, COALESCE(u.name,''),
	COALESCE(t.event_id,''), t.observed_at, t.observed_minor, t.parsed, t.hits`

func scanBankTest(r interface{ Scan(...any) error }) (BankTest, error) {
	var t BankTest
	var created, expires string
	var cancelled, observed sql.NullString
	var obsMinor sql.NullInt64
	var parsed int
	err := r.Scan(&t.ID, &t.ClassID, &t.Marker, &t.ExpectedMinor, &t.Stored, &created, &expires, &cancelled, &t.CreatedByName, &t.EventID, &observed, &obsMinor, &parsed, &t.Hits)
	t.CreatedAt, t.ExpiresAt = parseTime(created), parseTime(expires)
	t.CancelledAt, t.ObservedAt = parseNullTime(cancelled), parseNullTime(observed)
	if obsMinor.Valid {
		t.ObservedMinor = &obsMinor.Int64
	}
	t.Parsed = parsed == 1
	return t, err
}

// CreateBankTest opens a diagnostic reference for the class.
func (s *Store) CreateBankTest(ctx context.Context, classID int64, actor User, expectedMinor int64) (BankTest, error) {
	if expectedMinor <= 0 || expectedMinor > MaxBankTestMinor {
		return BankTest{}, errf(ErrInvalid, "the test amount must be between 0.01 and %d.00 PLN", MaxBankTestMinor/100)
	}
	var id int64
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bank_tests WHERE class_id = ? AND status = 'pending' AND expires_at > ?`, classID, ts(s.Now())).Scan(&open); err != nil {
			return err
		}
		if open >= MaxOpenBankTests {
			return errf(ErrConflict, "too many open test references")
		}
		for try := 0; try < 10; try++ {
			rnd, err := auth.RandomCode(bankTestAlphabet, bankTestRandom)
			if err != nil {
				return err
			}
			marker := BankTestPrefix + rnd
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bank_tests WHERE marker = ?`, marker).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			r, err := tx.ExecContext(ctx, `INSERT INTO bank_tests (class_id, marker, expected_minor, created_by, created_at, expires_at) VALUES (?,?,?,?,?,?)`,
				classID, marker, expectedMinor, actor.ID, ts(s.Now()), ts(s.Now().Add(BankTestTTL)))
			if err != nil {
				return err
			}
			id, _ = r.LastInsertId()
			return nil
		}
		return errf(ErrConflict, "could not allocate a test marker")
	})
	if err != nil {
		return BankTest{}, err
	}
	return s.BankTestByID(ctx, classID, id)
}

// BankTestByID loads one reference of the class.
func (s *Store) BankTestByID(ctx context.Context, classID, id int64) (BankTest, error) {
	t, err := scanBankTest(s.DB.QueryRowContext(ctx, `SELECT `+bankTestCols+` FROM bank_tests t LEFT JOIN users u ON u.id = t.created_by WHERE t.id = ? AND t.class_id = ?`, id, classID))
	return t, wrapNotFound(err)
}

// BankTests lists the class's references, newest first.
func (s *Store) BankTests(ctx context.Context, classID int64, limit int) ([]BankTest, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+bankTestCols+` FROM bank_tests t LEFT JOIN users u ON u.id = t.created_by WHERE t.class_id = ? ORDER BY t.id DESC LIMIT ?`, classID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BankTest
	for rows.Next() {
		t, err := scanBankTest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CancelBankTest closes a pending reference. A success is a fact and cannot be taken back; a cancelled or
// expired reference keeps swallowing a notification that still arrives with its marker.
func (s *Store) CancelBankTest(ctx context.Context, classID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE bank_tests SET status = 'cancelled', cancelled_at = ? WHERE id = ? AND class_id = ? AND status = 'pending'`, ts(s.Now()), id, classID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := s.BankTestByID(ctx, classID, id); err != nil {
		return err
	}
	return errf(ErrConflict, "the test reference is already closed")
}

// normalizeMarkerText keeps only upper-case letters and digits, so that a bank that re-cases the title or
// drops the hyphens does not hide the marker.
func normalizeMarkerText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// markersIn returns every distinct marker-shaped token of the text.
func markersIn(text string) []string {
	n := normalizeMarkerText(text)
	width := len(BankTestPrefix) + bankTestRandom
	var out []string
	seen := map[string]bool{}
	for i := 0; ; {
		j := strings.Index(n[i:], BankTestPrefix)
		if j < 0 {
			break
		}
		i += j
		if i+width <= len(n) {
			if m := n[i : i+width]; !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
		i++
	}
	return out
}

// interceptBankTest recognises a diagnostic notification. It must run before anything else in processEvent. The
// event row has just been inserted in this transaction, so a redelivery of the same id never reaches here, and
// SQLite's immediate write lock serialises concurrent deliveries with cancel and expiry.
func (s *Store) interceptBankTest(ctx context.Context, tx *sql.Tx, eventID string, received time.Time, parsed *alior.Notification) (IngestResult, bool, error) {
	var subject, body string
	if err := tx.QueryRowContext(ctx, `SELECT subject, body FROM bank_events WHERE id = ?`, eventID).Scan(&subject, &body); err != nil {
		return IngestResult{}, false, err
	}
	text := subject + "\n" + body
	var amount int64
	if parsed != nil {
		text += "\n" + parsed.Title
		if a, err := money.Parse(parsed.Amount); err == nil && a > 0 {
			amount = a
		}
	}
	isParsed := parsed != nil && amount > 0

	type known struct {
		id       int64
		status   string
		expires  time.Time
		hasEvent bool
		parsed   bool
	}
	var found []known
	for _, m := range markersIn(text) {
		var k known
		var exp string
		var ev sql.NullString
		var p int
		err := tx.QueryRowContext(ctx, `SELECT id, status, expires_at, event_id, parsed FROM bank_tests WHERE marker = ?`, m).Scan(&k.id, &k.status, &exp, &ev, &p)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return IngestResult{}, false, err
		}
		k.expires, k.hasEvent, k.parsed = parseTime(exp), ev.Valid, p == 1
		found = append(found, k)
	}
	if len(found) == 0 {
		return IngestResult{}, false, nil
	}

	at := received
	if now := s.Now(); at.After(now) {
		at = now
	}
	for _, k := range found {
		var obs any
		if isParsed {
			obs = amount
		}
		switch {
		case isParsed && k.status == "pending" && !at.After(k.expires):
			// The pipeline worked and the reference was still open.
			_, err := tx.ExecContext(ctx, `UPDATE bank_tests SET status = 'success', event_id = ?, observed_at = ?, observed_minor = ?, parsed = 1, hits = hits + 1 WHERE id = ?`, eventID, ts(at), obs, k.id)
			if err != nil {
				return IngestResult{}, false, err
			}
		case isParsed && !k.parsed:
			// Late, cancelled, or the first usable observation after unreadable ones: remember it, change no state.
			_, err := tx.ExecContext(ctx, `UPDATE bank_tests SET event_id = ?, observed_at = ?, observed_minor = ?, parsed = 1, hits = hits + 1 WHERE id = ?`, eventID, ts(at), obs, k.id)
			if err != nil {
				return IngestResult{}, false, err
			}
		case !k.hasEvent:
			_, err := tx.ExecContext(ctx, `UPDATE bank_tests SET event_id = ?, observed_at = ?, hits = hits + 1 WHERE id = ?`, eventID, ts(at), k.id)
			if err != nil {
				return IngestResult{}, false, err
			}
		default:
			if _, err := tx.ExecContext(ctx, `UPDATE bank_tests SET hits = hits + 1 WHERE id = ?`, k.id); err != nil {
				return IngestResult{}, false, err
			}
		}
	}

	status, reason := EventTestUnparsed, "Przelew diagnostyczny (test połączenia z bankiem) — treść powiadomienia nie została rozpoznana jako uznanie rachunku. Nie księgowano."
	if isParsed {
		status, reason = EventTest, "Przelew diagnostyczny (test połączenia z bankiem) — pominięty w księgowaniu."
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bank_events SET status = ?, reason = ?, transfer_id = NULL WHERE id = ?`, status, reason, eventID); err != nil {
		return IngestResult{}, false, err
	}
	return IngestResult{Outcome: OutcomeTest}, true, nil
}
