package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/alior"
	"github.com/classy-cash/app/internal/mail"
	"github.com/classy-cash/app/internal/matcher"
	"github.com/classy-cash/app/internal/money"
)

// Transfer is a credit announced by the bank.
type Transfer struct {
	ID            int64
	ClassID       *int64
	EventID       string
	Account       string
	Sender        string
	Title         string
	AmountMinor   int64
	TransferredAt time.Time
	PaymentID     *int64
	ReviewReason  string
	Payment       *Payment // loaded for list views
}

// IsMatched reports whether the transfer is attached to a payment.
func (t Transfer) IsMatched() bool { return t.PaymentID != nil }

// IsPartial reports a matched transfer whose amount differs from the payment amount.
func (t Transfer) IsPartial() bool { return t.Payment != nil && t.Payment.AmountMinor != t.AmountMinor }

// Difference is transfer minus due amount (negative = underpayment).
func (t Transfer) Difference() int64 {
	if t.Payment == nil {
		return 0
	}
	return t.AmountMinor - t.Payment.AmountMinor
}

const transferCols = `id, class_id, COALESCE(event_id,''), account_number, sender, title, amount_minor, transferred_at, payment_id, COALESCE(review_reason,'')`

func scanTransfer(r interface{ Scan(...any) error }) (Transfer, error) {
	var t Transfer
	var cid, pid sql.NullInt64
	var at string
	err := r.Scan(&t.ID, &cid, &t.EventID, &t.Account, &t.Sender, &t.Title, &t.AmountMinor, &at, &pid, &t.ReviewReason)
	if cid.Valid {
		t.ClassID = &cid.Int64
	}
	if pid.Valid {
		t.PaymentID = &pid.Int64
	}
	t.TransferredAt = parseTime(at)
	return t, err
}

func (s *Store) transferIn(ctx context.Context, x querier, classID, id int64) (Transfer, error) {
	t, err := scanTransfer(x.QueryRowContext(ctx, `SELECT `+transferCols+` FROM transfers WHERE id = ? AND deleted_at IS NULL AND (class_id = ? OR class_id IS NULL)`, id, classID))
	return t, wrapNotFound(err)
}

// TransferByID loads a transfer visible to the class.
func (s *Store) TransferByID(ctx context.Context, classID, id int64) (Transfer, error) {
	t, err := s.transferIn(ctx, s.DB, classID, id)
	if err != nil {
		return t, err
	}
	if t.PaymentID != nil {
		p, err := s.PaymentByID(ctx, classID, *t.PaymentID)
		if err == nil {
			t.Payment = &p
		}
	}
	return t, nil
}

// Transfers lists the class's bank operations, newest first.
func (s *Store) Transfers(ctx context.Context, classID int64) ([]Transfer, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+transferCols+` FROM transfers WHERE deleted_at IS NULL AND (class_id = ? OR class_id IS NULL) ORDER BY transferred_at DESC, id DESC LIMIT 300`, classID)
	if err != nil {
		return nil, err
	}
	var out []Transfer
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	cache := map[int64]*Payment{}
	for i := range out {
		if out[i].PaymentID == nil {
			continue
		}
		pid := *out[i].PaymentID
		if p, ok := cache[pid]; ok {
			out[i].Payment = p
			continue
		}
		p, err := s.PaymentByID(ctx, classID, pid)
		if err != nil {
			continue
		}
		cache[pid] = &p
		out[i].Payment = &p
	}
	return out, nil
}

// LastBankEvent is the time of the last authenticated bank delivery (the legacy "last import" date).
func (s *Store) LastBankEvent(ctx context.Context) *time.Time {
	var v sql.NullString
	_ = s.DB.QueryRowContext(ctx, `SELECT MAX(received_at) FROM bank_events`).Scan(&v)
	return parseNullTime(v)
}

// ---- ingestion ----

// BankEventIn is one authenticated webhook delivery.
type BankEventIn struct {
	ID         string
	ReceivedAt time.Time
	Recipient  string
	MailFrom   string
	Subject    string
	Body       string
}

// Ingest outcomes.
const (
	OutcomeMatched      = "matched"
	OutcomeUnmatched    = "unmatched"
	OutcomeReview       = "review"
	OutcomeUnrecognized = "unrecognized"
	OutcomeDuplicate    = "duplicate"
)

// IngestResult describes what a delivery did.
type IngestResult struct {
	Outcome    string
	TransferID int64
}

// IngestBankMail stores the delivery, the parsed transfer and any matching effects atomically.
// Redelivery of the same event id is acknowledged without applying financial effects twice.
func (s *Store) IngestBankMail(ctx context.Context, ev BankEventIn, parsed *alior.Notification) (IngestResult, error) {
	var res IngestResult
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO bank_events (id, received_at, recipient, mail_from, subject, body, status, created_at) VALUES (?,?,?,?,?,?, 'received', ?)`,
			ev.ID, ts(ev.ReceivedAt), ev.Recipient, ev.MailFrom, ev.Subject, ev.Body, ts(s.Now()))
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			res = IngestResult{Outcome: OutcomeDuplicate}
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(transfer_id,0) FROM bank_events WHERE id = ?`, ev.ID).Scan(&res.TransferID)
			return nil
		}
		res, err = s.processEvent(ctx, tx, ev.ID, ev.ReceivedAt, parsed)
		return err
	})
	return res, err
}

func (s *Store) processEvent(ctx context.Context, tx *sql.Tx, eventID string, received time.Time, parsed *alior.Notification) (IngestResult, error) {
	setStatus := func(status, reason string, transferID int64) error {
		var tid any
		if transferID != 0 {
			tid = transferID
		}
		_, err := tx.ExecContext(ctx, `UPDATE bank_events SET status = ?, reason = ?, transfer_id = ? WHERE id = ?`, status, reason, tid, eventID)
		return err
	}
	if parsed == nil {
		return IngestResult{Outcome: OutcomeUnrecognized}, setStatus("unrecognized", "Nie rozpoznano powiadomienia o uznaniu rachunku (nieznany format).", 0)
	}
	amount, err := money.Parse(parsed.Amount)
	if err != nil || amount <= 0 {
		return IngestResult{Outcome: OutcomeUnrecognized}, setStatus("unrecognized", "Nieprawidłowa kwota w powiadomieniu.", 0)
	}
	var classID any
	if v := s.settingTx(ctx, tx, "bank_class_id", ""); v != "" {
		classID = v
	} else {
		var first sql.NullInt64
		_ = tx.QueryRowContext(ctx, `SELECT MIN(id) FROM classes`).Scan(&first)
		if first.Valid {
			classID = first.Int64
		}
	}
	review := ""
	var dup int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM transfers WHERE deleted_at IS NULL AND account_number = ? AND sender = ? AND title = ? AND amount_minor = ? AND transferred_at >= ? LIMIT 1`,
		parsed.AccountNumber, parsed.Sender, parsed.Title, amount, ts(received.Add(-24*time.Hour))).Scan(&dup); err == nil {
		review = fmt.Sprintf("Możliwy duplikat przelewu #%d (ten sam nadawca, tytuł i kwota w ciągu doby). Zatwierdź albo odrzuć.", dup)
	}
	var rv any
	if review != "" {
		rv = review
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO transfers (class_id, event_id, account_number, sender, title, amount_minor, transferred_at, review_reason, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		classID, eventID, parsed.AccountNumber, parsed.Sender, parsed.Title, amount, ts(received), rv, ts(s.Now()))
	if err != nil {
		return IngestResult{}, err
	}
	tid, _ := r.LastInsertId()
	if review != "" {
		if err := s.notifyUnmatched(ctx, tx, tid, review); err != nil {
			return IngestResult{}, err
		}
		return IngestResult{Outcome: OutcomeReview, TransferID: tid}, setStatus("duplicate_review", review, tid)
	}
	matched, err := s.matchTransfer(ctx, tx, tid)
	if err != nil {
		return IngestResult{}, err
	}
	if !matched {
		if err := s.notifyUnmatched(ctx, tx, tid, ""); err != nil {
			return IngestResult{}, err
		}
		return IngestResult{Outcome: OutcomeUnmatched, TransferID: tid}, setStatus("parsed", "", tid)
	}
	return IngestResult{Outcome: OutcomeMatched, TransferID: tid}, setStatus("parsed", "", tid)
}

func (s *Store) settingTx(ctx context.Context, x querier, key, def string) string {
	var v string
	if err := x.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

// ReplayEvent re-parses an unrecognized event (after parser improvements). It is idempotent: an
// event that already produced a transfer is left untouched.
func (s *Store) ReplayEvent(ctx context.Context, eventID string, parse func(subject, body string) (alior.Notification, bool)) (IngestResult, error) {
	var res IngestResult
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		var ev BankEventIn
		var received, status string
		var tid sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT id, received_at, recipient, mail_from, subject, body, status, transfer_id FROM bank_events WHERE id = ?`, eventID).
			Scan(&ev.ID, &received, &ev.Recipient, &ev.MailFrom, &ev.Subject, &ev.Body, &status, &tid); err != nil {
			return wrapNotFound(err)
		}
		if tid.Valid || status != "unrecognized" {
			res = IngestResult{Outcome: OutcomeDuplicate, TransferID: tid.Int64}
			return nil
		}
		n, ok := parse(ev.Subject, ev.Body)
		var p *alior.Notification
		if ok {
			p = &n
		}
		var err error
		res, err = s.processEvent(ctx, tx, eventID, parseTime(received), p)
		return err
	})
	return res, err
}

// BankEvent is an entry of the quarantine/inbox view.
type BankEvent struct {
	ID         string
	ReceivedAt time.Time
	MailFrom   string
	Subject    string
	Body       string
	Status     string
	Reason     string
	TransferID int64
}

// BankEvents lists deliveries by status ("" = all).
func (s *Store) BankEvents(ctx context.Context, status string, limit int) ([]BankEvent, error) {
	q := `SELECT id, received_at, mail_from, subject, body, status, reason, COALESCE(transfer_id,0) FROM bank_events`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY received_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BankEvent
	for rows.Next() {
		var e BankEvent
		var at string
		if err := rows.Scan(&e.ID, &at, &e.MailFrom, &e.Subject, &e.Body, &e.Status, &e.Reason, &e.TransferID); err != nil {
			return nil, err
		}
		e.ReceivedAt = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PurgeBankEventBodies blanks stored notification bodies after the retention window.
func (s *Store) PurgeBankEventBodies(ctx context.Context, keep time.Duration) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE bank_events SET body = '' WHERE body <> '' AND received_at < ?`, ts(s.Now().Add(-keep)))
	return err
}

func (s *Store) notifyUnmatched(ctx context.Context, tx *sql.Tx, transferID int64, reason string) error {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payments WHERE status = 'pending'`).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 && reason == "" {
		return nil
	}
	t, err := scanTransfer(tx.QueryRowContext(ctx, `SELECT `+transferCols+` FROM transfers WHERE id = ?`, transferID))
	if err != nil {
		return err
	}
	admins, err := queryUsers(ctx, tx, `SELECT `+userCols+` FROM users WHERE is_admin = 1`)
	if err != nil {
		return err
	}
	b := s.brandingTx(ctx, tx)
	r := mail.TransferNotMatched(b, mail.TransferNotMatchedData{AmountMinor: t.AmountMinor, Sender: t.Sender, Title: t.Title, Date: t.TransferredAt, Reason: reason, ReviewURL: s.BaseURL + "/treasurer/payments"}, s.Loc)
	for _, a := range admins {
		if err := enqueue(ctx, tx, s.Now(), mail.Message{Key: fmt.Sprintf("unmatched-%d-%d", transferID, a.ID), ToEmail: a.Email, ToName: a.Name, Subject: r.Subject, HTML: r.HTML, Text: r.Text}); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE transfers SET notified_at = ? WHERE id = ?`, ts(s.Now()), transferID)
	return err
}

// ---- matching ----

// matchTransfer tries the payment code first, then unambiguous history. It reports whether the
// transfer was attached to a payment.
func (s *Store) matchTransfer(ctx context.Context, tx *sql.Tx, transferID int64) (bool, error) {
	t, err := scanTransfer(tx.QueryRowContext(ctx, `SELECT `+transferCols+` FROM transfers WHERE id = ?`, transferID))
	if err != nil {
		return false, err
	}
	if t.PaymentID != nil || t.ReviewReason != "" {
		return t.PaymentID != nil, nil
	}
	cands := matcher.Candidates(t.Title)
	if len(cands) > 0 {
		args := make([]any, len(cands))
		for i, c := range cands {
			args[i] = c
		}
		rows, err := tx.QueryContext(ctx, `SELECT code, id FROM payments WHERE status IN ('pending','expired') AND code IN (`+placeholders(len(cands))+`)`, args...)
		if err != nil {
			return false, err
		}
		byCode := map[string]int64{}
		for rows.Next() {
			var c string
			var id int64
			if err := rows.Scan(&c, &id); err != nil {
				rows.Close()
				return false, err
			}
			byCode[c] = id
		}
		rows.Close()
		for _, c := range cands {
			if id, ok := byCode[c]; ok {
				return true, s.attachTransfer(ctx, tx, transferID, id, false)
			}
		}
	}
	id, err := s.historicalMatch(ctx, tx, t)
	if err != nil || id == 0 {
		return false, err
	}
	return true, s.attachTransfer(ctx, tx, transferID, id, false)
}

// candidate is a payment considered for a transfer.
type candidate struct {
	ID        int64
	ClassID   int64
	UserID    int64
	Amount    int64
	CreatedAt string
	Students  map[int64]bool
}

func loadCandidates(ctx context.Context, x querier, classID int64, search string) ([]candidate, error) {
	q := `SELECT p.id, p.class_id, p.user_id, p.amount_minor, p.created_at FROM payments p JOIN users u ON u.id = p.user_id WHERE p.status IN ('pending','expired')`
	var args []any
	if classID != 0 {
		q += ` AND p.class_id = ?`
		args = append(args, classID)
	}
	if search = strings.TrimSpace(search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		q += ` AND (LOWER(u.name) LIKE ? OR LOWER(u.email) LIKE ? OR LOWER(COALESCE(p.code,'')) LIKE ? OR printf('%.2f', p.amount_minor / 100.0) LIKE ?
			OR EXISTS (SELECT 1 FROM student_payments sp JOIN students st ON st.id = sp.student_id WHERE sp.payment_id = p.id AND (LOWER(st.first_name || ' ' || st.last_name) LIKE ? OR LOWER(sp.label) LIKE ?)))`
		args = append(args, like, like, like, like, like, like)
	}
	q += ` ORDER BY p.created_at DESC, p.id DESC LIMIT 500`
	rows, err := x.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var out []candidate
	idx := map[int64]int{}
	for rows.Next() {
		c := candidate{Students: map[int64]bool{}}
		if err := rows.Scan(&c.ID, &c.ClassID, &c.UserID, &c.Amount, &c.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		idx[c.ID] = len(out)
		out = append(out, c)
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]any, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	srows, err := x.QueryContext(ctx, `SELECT payment_id, student_id FROM student_payments WHERE payment_id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var pid, sid int64
		if err := srows.Scan(&pid, &sid); err != nil {
			return nil, err
		}
		out[idx[pid]].Students[sid] = true
	}
	return out, srows.Err()
}

type history struct {
	studentNames map[int64]string
	userIDs      map[int64]bool
	sameAccount  bool
	sameSender   bool
	count        int
}

func loadHistory(ctx context.Context, x querier, t Transfer) (history, error) {
	h := history{studentNames: map[int64]string{}, userIDs: map[int64]bool{}}
	rows, err := x.QueryContext(ctx, `SELECT t.account_number, t.sender, p.user_id, st.id, st.first_name || ' ' || st.last_name
		FROM transfers t JOIN payments p ON p.id = t.payment_id
		LEFT JOIN student_payments sp ON sp.payment_id = p.id LEFT JOIN students st ON st.id = sp.student_id
		WHERE t.deleted_at IS NULL AND t.id <> ? AND (t.account_number = ? OR t.sender = ?) ORDER BY t.transferred_at DESC`, t.ID, t.Account, t.Sender)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	counted := map[string]bool{}
	for rows.Next() {
		var acc, sender string
		var uid int64
		var sid sql.NullInt64
		var name sql.NullString
		if err := rows.Scan(&acc, &sender, &uid, &sid, &name); err != nil {
			return h, err
		}
		if acc == t.Account {
			h.sameAccount = true
		}
		if sender == t.Sender {
			h.sameSender = true
		}
		counted[acc+"\x00"+sender+"\x00"+fmt.Sprint(uid)] = true
		h.userIDs[uid] = true
		if sid.Valid {
			h.studentNames[sid.Int64] = strings.TrimSpace(name.String)
		}
	}
	h.count = len(counted)
	return h, rows.Err()
}

func buildHints(t Transfer, cands []candidate, h history) map[int64]matcher.Hint {
	hints := map[int64]matcher.Hint{}
	for _, c := range cands {
		var names []string
		score := 0
		for sid := range c.Students {
			if n, ok := h.studentNames[sid]; ok {
				names = append(names, n)
				score += matcher.ScoreStudentMatch
			}
		}
		sort.Strings(names)
		if h.userIDs[c.UserID] {
			score += matcher.ScoreUserMatch
		}
		if c.Amount == t.AmountMinor {
			score += matcher.ScoreAmountMatch
		}
		if score == 0 {
			continue
		}
		hints[c.ID] = matcher.Hint{StudentNames: dedupe(names), SameAccountNumber: h.sameAccount, SameSender: h.sameSender, HistoricalMatchCount: h.count, Score: score}
	}
	return hints
}

func dedupe(in []string) []string {
	var out []string
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// historicalMatch auto-assigns only when exactly one payment fits: first by a recurring student
// plus equal amount, else by a recurring payer plus equal amount.
func (s *Store) historicalMatch(ctx context.Context, tx *sql.Tx, t Transfer) (int64, error) {
	cands, err := loadCandidates(ctx, tx, 0, "")
	if err != nil || len(cands) == 0 {
		return 0, err
	}
	h, err := loadHistory(ctx, tx, t)
	if err != nil {
		return 0, err
	}
	hints := buildHints(t, cands, h)
	var byStudent, byUser []int64
	for _, c := range cands {
		hint, ok := hints[c.ID]
		if !ok || c.Amount != t.AmountMinor {
			continue
		}
		switch {
		case hint.HasStudentMatch():
			byStudent = append(byStudent, c.ID)
		case hint.Score >= matcher.ScoreUserMatch:
			byUser = append(byUser, c.ID)
		}
	}
	if len(byStudent) == 1 {
		return byStudent[0], nil
	}
	if len(byUser) == 1 {
		return byUser[0], nil
	}
	return 0, nil
}

// PaymentSuggestion is an assignable payment with an optional history hint.
type PaymentSuggestion struct {
	Payment     Payment
	Hint        *matcher.Hint
	AmountMatch bool
}

// Suggestions lists payments a transfer could be assigned to, best hint first.
func (s *Store) Suggestions(ctx context.Context, classID, transferID int64, search string) ([]PaymentSuggestion, *matcher.Hint, error) {
	t, err := s.transferIn(ctx, s.DB, classID, transferID)
	if err != nil {
		return nil, nil, err
	}
	cands, err := loadCandidates(ctx, s.DB, classID, search)
	if err != nil {
		return nil, nil, err
	}
	h, err := loadHistory(ctx, s.DB, t)
	if err != nil {
		return nil, nil, err
	}
	hints := buildHints(t, cands, h)
	// The top hint is computed over all payments, not just the searched subset.
	allCands, _ := loadCandidates(ctx, s.DB, classID, "")
	var top *matcher.Hint
	for _, hint := range buildHints(t, allCands, h) {
		hint := hint
		if top == nil || hint.Score > top.Score {
			top = &hint
		}
	}
	out := make([]PaymentSuggestion, 0, len(cands))
	for _, c := range cands {
		p, err := s.PaymentByID(ctx, classID, c.ID)
		if err != nil {
			return nil, nil, err
		}
		sg := PaymentSuggestion{Payment: p, AmountMatch: c.Amount == t.AmountMinor}
		if hint, ok := hints[c.ID]; ok {
			hint := hint
			sg.Hint = &hint
		}
		out = append(out, sg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := 0, 0
		if out[i].Hint != nil {
			si = out[i].Hint.Score
		}
		if out[j].Hint != nil {
			sj = out[j].Hint.Score
		}
		if si != sj {
			return si > sj
		}
		return out[i].Payment.CreatedAt.After(out[j].Payment.CreatedAt)
	})
	return out, top, nil
}

// attachTransfer links a transfer to a payment and settles it when fully covered (or forced).
func (s *Store) attachTransfer(ctx context.Context, tx *sql.Tx, transferID, paymentID int64, force bool) error {
	var classID, amount int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT class_id, amount_minor, status FROM payments WHERE id = ?`, paymentID).Scan(&classID, &amount, &status); err != nil {
		return wrapNotFound(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE transfers SET payment_id = ?, class_id = ?, review_reason = NULL WHERE id = ?`, paymentID, classID, transferID); err != nil {
		return err
	}
	var received int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM transfers WHERE payment_id = ? AND deleted_at IS NULL`, paymentID).Scan(&received); err != nil {
		return err
	}
	switch status {
	case PaymentPending, PaymentExpired:
		if force || received >= amount {
			return s.settlePayment(ctx, tx, paymentID)
		}
		_, err := tx.ExecContext(ctx, `UPDATE student_payments SET status = 'partial' WHERE payment_id = ? AND status = 'pending'`, paymentID)
		return err
	case PaymentPaid:
		return recalcLedger(ctx, tx, classID, s.Now())
	}
	return nil
}

// AssignTransfer manually links a transfer to a payment of the class (and moves it if already linked).
func (s *Store) AssignTransfer(ctx context.Context, classID, transferID, paymentID int64) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		t, err := s.transferIn(ctx, tx, classID, transferID)
		if err != nil {
			return err
		}
		var pc int64
		var pst string
		if err := tx.QueryRowContext(ctx, `SELECT class_id, status FROM payments WHERE id = ?`, paymentID).Scan(&pc, &pst); err != nil || pc != classID {
			return ErrNotFound
		}
		if pst != PaymentPending && pst != PaymentExpired && (t.PaymentID == nil || *t.PaymentID != paymentID) {
			return errf(ErrConflict, "payment is %s", pst)
		}
		if t.PaymentID != nil {
			if *t.PaymentID == paymentID {
				return nil
			}
			if err := s.detachTransfer(ctx, tx, t); err != nil {
				return err
			}
		}
		return s.attachTransfer(ctx, tx, transferID, paymentID, true)
	})
}

// AssignTransferToObligation books a transfer against an obligation that has no payment yet.
func (s *Store) AssignTransferToObligation(ctx context.Context, classID, transferID, spID int64, actor User) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		t, err := s.transferIn(ctx, tx, classID, transferID)
		if err != nil {
			return err
		}
		sp, err := s.studentPayment(ctx, tx, classID, spID)
		if err != nil {
			return err
		}
		if sp.Status == SPPaid {
			return errf(ErrConflict, "obligation is already paid")
		}
		if t.PaymentID != nil {
			if err := s.detachTransfer(ctx, tx, t); err != nil {
				return err
			}
		}
		pid := int64(0)
		if sp.PaymentID != nil && (sp.PaymentStatus == PaymentPending || sp.PaymentStatus == PaymentExpired) {
			pid = *sp.PaymentID
		} else {
			owner, err := payerFor(ctx, tx, sp.StudentID, actor.ID)
			if err != nil {
				return err
			}
			pid, err = insertPayment(ctx, tx, s.Now(), classID, owner, t.AmountMinor, PaymentPending, false)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET payment_id = ? WHERE id = ?`, pid, spID); err != nil {
				return err
			}
		}
		return s.attachTransfer(ctx, tx, transferID, pid, true)
	})
}

// UnassignTransfer detaches a transfer; a payment it had settled returns to pending.
func (s *Store) UnassignTransfer(ctx context.Context, classID, transferID int64) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		t, err := s.transferIn(ctx, tx, classID, transferID)
		if err != nil {
			return err
		}
		if t.PaymentID == nil {
			return nil
		}
		return s.detachTransfer(ctx, tx, t)
	})
}

func (s *Store) detachTransfer(ctx context.Context, tx *sql.Tx, t Transfer) error {
	pid := *t.PaymentID
	if _, err := tx.ExecContext(ctx, `UPDATE transfers SET payment_id = NULL WHERE id = ?`, t.ID); err != nil {
		return err
	}
	var classID, amount int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT class_id, amount_minor, status FROM payments WHERE id = ?`, pid).Scan(&classID, &amount, &status); err != nil {
		return err
	}
	var received int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM transfers WHERE payment_id = ? AND deleted_at IS NULL`, pid).Scan(&received); err != nil {
		return err
	}
	if status == PaymentPaid && received < amount {
		code, err := generateCode(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = 'pending', paid_at = NULL, code = ? WHERE id = ?`, code, pid); err != nil {
			return err
		}
		newStatus := SPPending
		if received > 0 {
			newStatus = SPPartial
		}
		if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET status = ?, paid_at = NULL WHERE payment_id = ?`, newStatus, pid); err != nil {
			return err
		}
		return recalcLedger(ctx, tx, classID, s.Now())
	}
	if status == PaymentPending || status == PaymentExpired {
		if received == 0 {
			_, err := tx.ExecContext(ctx, `UPDATE student_payments SET status = 'pending' WHERE payment_id = ? AND status = 'partial'`, pid)
			return err
		}
		return nil
	}
	return recalcLedger(ctx, tx, classID, s.Now())
}

// ReviewTransfer resolves a held transfer: approve it as a genuine new payment (and match it), or dismiss it.
func (s *Store) ReviewTransfer(ctx context.Context, classID, transferID int64, approve bool) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		t, err := s.transferIn(ctx, tx, classID, transferID)
		if err != nil {
			return err
		}
		if t.ReviewReason == "" {
			return errf(ErrConflict, "transfer is not awaiting review")
		}
		if !approve {
			_, err := tx.ExecContext(ctx, `UPDATE transfers SET deleted_at = ? WHERE id = ?`, ts(s.Now()), transferID)
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE transfers SET review_reason = NULL WHERE id = ?`, transferID); err != nil {
			return err
		}
		matched, err := s.matchTransfer(ctx, tx, transferID)
		if err != nil {
			return err
		}
		if !matched {
			return s.notifyUnmatched(ctx, tx, transferID, "")
		}
		return nil
	})
}

// RematchRecent retries unmatched transfers from the last 12 hours (scheduled every minute).
func (s *Store) RematchRecent(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM transfers WHERE payment_id IS NULL AND deleted_at IS NULL AND review_reason IS NULL AND transferred_at > ?`, ts(s.Now().Add(-12*time.Hour)))
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		var matched bool
		err := s.InTx(ctx, func(tx *sql.Tx) error {
			var err error
			matched, err = s.matchTransfer(ctx, tx, id)
			return err
		})
		if err != nil {
			return n, err
		}
		if matched {
			n++
		}
	}
	return n, nil
}

// NotifyParentAboutDifference mails the payer of a transfer whose amount differs from the due amount.
func (s *Store) NotifyParentAboutDifference(ctx context.Context, classID, transferID int64) error {
	t, err := s.TransferByID(ctx, classID, transferID)
	if err != nil {
		return err
	}
	if t.Payment == nil {
		return errf(ErrConflict, "transfer is not matched")
	}
	r := mail.PartialPayment(s.Branding(ctx), t.Payment.UserName, t.Payment.Label(), t.Payment.AmountMinor, t.AmountMinor)
	return s.Enqueue(ctx, mail.Message{Key: fmt.Sprintf("diff-%d-%d", transferID, s.Now().Unix()), ToEmail: t.Payment.UserEmail, ToName: t.Payment.UserName, Subject: r.Subject, HTML: r.HTML, Text: r.Text})
}
