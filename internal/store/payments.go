package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/auth"
)

// ErrNothingToPay is returned when a parent has no open obligations.
var ErrNothingToPay = errors.New("nothing to pay")

// CodeAlphabet is the payment-code alphabet (I and O omitted for readability).
const CodeAlphabet = "0123456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// StudentPayment is one student's obligation.
type StudentPayment struct {
	ID             int64
	ClassID        int64
	StudentID      int64
	PaymentID      *int64
	ContributionID *int64
	Label          string
	AmountMinor    int64
	Status         string
	CreatedAt      time.Time
	DueAt          *time.Time
	PaidAt         *time.Time
	StudentName    string
	Code           string // payment code of the linked payment, when any
	PaymentStatus  string
}

// Payment is what a parent sends the treasurer.
type Payment struct {
	ID            int64
	ClassID       int64
	UserID        int64
	AmountMinor   int64
	Status        string
	Code          string
	CreatedAt     time.Time
	PaidAt        *time.Time
	UserName      string
	UserEmail     string
	ReceivedMinor int64
	Items         []StudentPayment
}

// Label summarises what the payment is for.
func (p Payment) Label() string {
	if len(p.Items) == 0 {
		return "Wpłata ogólna"
	}
	seen := map[string]bool{}
	var out []string
	for _, it := range p.Items {
		if !seen[it.Label] {
			seen[it.Label] = true
			out = append(out, it.Label)
		}
	}
	return strings.Join(out, ", ")
}

const spCols = `sp.id, sp.class_id, sp.student_id, sp.payment_id, sp.contribution_id, sp.label, sp.amount_minor, sp.status, sp.created_at, sp.due_at, sp.paid_at,
	st.first_name || ' ' || st.last_name, COALESCE(p.code,''), COALESCE(p.status,'')`
const spFrom = ` FROM student_payments sp JOIN students st ON st.id = sp.student_id LEFT JOIN payments p ON p.id = sp.payment_id `

func scanSP(r interface{ Scan(...any) error }) (StudentPayment, error) {
	var sp StudentPayment
	var pid, cid sql.NullInt64
	var created string
	var due, paid sql.NullString
	err := r.Scan(&sp.ID, &sp.ClassID, &sp.StudentID, &pid, &cid, &sp.Label, &sp.AmountMinor, &sp.Status, &created, &due, &paid, &sp.StudentName, &sp.Code, &sp.PaymentStatus)
	if pid.Valid {
		sp.PaymentID = &pid.Int64
	}
	if cid.Valid {
		sp.ContributionID = &cid.Int64
	}
	sp.CreatedAt, sp.DueAt, sp.PaidAt = parseTime(created), parseNullTime(due), parseNullTime(paid)
	return sp, err
}

func querySPs(ctx context.Context, x querier, where string, args ...any) ([]StudentPayment, error) {
	rows, err := x.QueryContext(ctx, `SELECT `+spCols+spFrom+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StudentPayment
	for rows.Next() {
		sp, err := scanSP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// StudentPaymentsByClass lists every obligation of the class, newest first.
func (s *Store) StudentPaymentsByClass(ctx context.Context, classID int64) ([]StudentPayment, error) {
	return querySPs(ctx, s.DB, `WHERE sp.class_id = ? AND st.deleted_at IS NULL ORDER BY sp.created_at DESC, sp.id DESC`, classID)
}

// OpenStudentPayments lists unpaid obligations of the class.
func (s *Store) OpenStudentPayments(ctx context.Context, classID int64) ([]StudentPayment, error) {
	return querySPs(ctx, s.DB, `WHERE sp.class_id = ? AND sp.status IN ('pending','partial') AND st.deleted_at IS NULL ORDER BY st.last_name, st.first_name, sp.created_at`, classID)
}

// StudentPaymentsForParent lists obligations of the parent's children, open ones first.
func (s *Store) StudentPaymentsForParent(ctx context.Context, userID, classID int64) ([]StudentPayment, error) {
	return querySPs(ctx, s.DB, `WHERE sp.class_id = ? AND sp.student_id IN (SELECT student_id FROM student_parents WHERE user_id = ?) ORDER BY (sp.status = 'paid'), sp.created_at DESC, sp.id DESC`, classID, userID)
}

func (s *Store) studentPayment(ctx context.Context, x querier, classID, id int64) (StudentPayment, error) {
	list, err := querySPs(ctx, x, `WHERE sp.id = ? AND sp.class_id = ?`, id, classID)
	if err != nil {
		return StudentPayment{}, err
	}
	if len(list) == 0 {
		return StudentPayment{}, ErrNotFound
	}
	return list[0], nil
}

// ---- contributions ----

// Contribution is a collection ("składka") assigned to students.
type Contribution struct {
	ID          int64
	ClassID     int64
	Title       string
	AmountMinor int64
	DueAt       *time.Time
	CreatedAt   time.Time
}

// ContribItem is one student's slot in a contribution.
type ContribItem struct {
	Student Student
	SP      *StudentPayment
}

// ContributionView is a contribution with per-student progress.
type ContributionView struct {
	Contribution
	Items         []ContribItem
	PaidCount     int
	TotalPaid     int64
	Overdue       bool
	ProgressPct   int
	ExpectedTotal int64
}

// CreateContribution creates a collection and one obligation per selected student of the class.
func (s *Store) CreateContribution(ctx context.Context, classID int64, title string, amountMinor int64, dueAt *time.Time, studentIDs []int64) (int64, error) {
	title = strings.TrimSpace(title)
	if title == "" || amountMinor <= 0 {
		return 0, errf(ErrInvalid, "title and a positive amount are required")
	}
	var cid int64
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		now := s.Now()
		res, err := tx.ExecContext(ctx, `INSERT INTO contributions (class_id, title, amount_minor, due_at, created_at) VALUES (?,?,?,?,?)`, classID, title, amountMinor, nullTime(dueAt), ts(now))
		if err != nil {
			return err
		}
		cid, _ = res.LastInsertId()
		seen := map[int64]bool{}
		for _, sid := range studentIDs {
			if seen[sid] {
				continue
			}
			seen[sid] = true
			var ok int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM students WHERE id = ? AND class_id = ? AND deleted_at IS NULL`, sid, classID).Scan(&ok); err != nil {
				continue // students of other classes are silently ignored (class isolation)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO contribution_students (contribution_id, student_id) VALUES (?,?)`, cid, sid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO student_payments (class_id, student_id, contribution_id, label, amount_minor, status, created_at, due_at) VALUES (?,?,?,?,?,'pending',?,?)`,
				classID, sid, cid, title, amountMinor, ts(now), nullTime(dueAt)); err != nil {
				return err
			}
		}
		return nil
	})
	return cid, err
}

// ContributionViews lists the class collections with student progress.
func (s *Store) ContributionViews(ctx context.Context, classID int64) ([]ContributionView, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, class_id, title, amount_minor, due_at, created_at FROM contributions WHERE class_id = ?
		ORDER BY due_at IS NULL, due_at ASC, created_at DESC, id DESC`, classID)
	if err != nil {
		return nil, err
	}
	var views []ContributionView
	idx := map[int64]int{}
	for rows.Next() {
		var c Contribution
		var due sql.NullString
		var created string
		if err := rows.Scan(&c.ID, &c.ClassID, &c.Title, &c.AmountMinor, &due, &created); err != nil {
			rows.Close()
			return nil, err
		}
		c.DueAt, c.CreatedAt = parseNullTime(due), parseTime(created)
		idx[c.ID] = len(views)
		views = append(views, ContributionView{Contribution: c, Overdue: c.DueAt != nil && c.DueAt.Before(s.Now())})
	}
	rows.Close()
	if len(views) == 0 {
		return views, nil
	}
	irows, err := s.DB.QueryContext(ctx, `SELECT cs.contribution_id, st.id, st.class_id, st.first_name, st.last_name, sp.id
		FROM contribution_students cs JOIN students st ON st.id = cs.student_id JOIN contributions c ON c.id = cs.contribution_id
		LEFT JOIN student_payments sp ON sp.contribution_id = cs.contribution_id AND sp.student_id = cs.student_id
		WHERE c.class_id = ? AND st.deleted_at IS NULL ORDER BY st.last_name COLLATE NOCASE, st.first_name COLLATE NOCASE`, classID)
	if err != nil {
		return nil, err
	}
	type link struct {
		c, spID int64
		st      Student
	}
	var links []link
	for irows.Next() {
		var l link
		var spid sql.NullInt64
		if err := irows.Scan(&l.c, &l.st.ID, &l.st.ClassID, &l.st.FirstName, &l.st.LastName, &spid); err != nil {
			irows.Close()
			return nil, err
		}
		l.spID = spid.Int64
		links = append(links, l)
	}
	irows.Close()
	sps, err := querySPs(ctx, s.DB, `WHERE sp.class_id = ? AND sp.contribution_id IS NOT NULL`, classID)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*StudentPayment{}
	for i := range sps {
		byID[sps[i].ID] = &sps[i]
	}
	for _, l := range links {
		v := &views[idx[l.c]]
		it := ContribItem{Student: l.st}
		if sp := byID[l.spID]; sp != nil {
			it.SP = sp
			if sp.Status == SPPaid {
				v.PaidCount++
				v.TotalPaid += sp.AmountMinor
			}
		}
		v.Items = append(v.Items, it)
	}
	for i := range views {
		v := &views[i]
		v.ExpectedTotal = v.AmountMinor * int64(len(v.Items))
		if v.ExpectedTotal > 0 {
			v.ProgressPct = int(v.TotalPaid * 100 / v.ExpectedTotal)
		}
	}
	return views, nil
}

// ---- obligations ----

// GenerateCode makes a unique payment code (4 chars, 5 as the namespace fills).
func generateCode(ctx context.Context, x querier) (string, error) {
	for length := 4; length <= 6; length++ {
		for try := 0; try < 25; try++ {
			c, err := auth.RandomCode(CodeAlphabet, length)
			if err != nil {
				return "", err
			}
			var n int
			if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM payments WHERE code = ?`, c).Scan(&n); err != nil {
				return "", err
			}
			if n == 0 {
				return c, nil
			}
		}
	}
	return "", errors.New("could not allocate a payment code")
}

func insertPayment(ctx context.Context, x querier, now time.Time, classID, userID, amount int64, status string, withCode bool) (int64, error) {
	var code any
	if withCode {
		c, err := generateCode(ctx, x)
		if err != nil {
			return 0, err
		}
		code = c
	}
	var paid any
	if status == PaymentPaid {
		paid = ts(now)
	}
	res, err := x.ExecContext(ctx, `INSERT INTO payments (class_id, user_id, amount_minor, status, code, created_at, paid_at) VALUES (?,?,?,?,?,?,?)`, classID, userID, amount, status, code, ts(now), paid)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// reconcilePayment re-derives an open payment's amount from its obligations and removes it when empty.
func reconcilePayment(ctx context.Context, tx *sql.Tx, paymentID int64) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM payments WHERE id = ?`, paymentID).Scan(&status); err != nil {
		return nil
	}
	if status != PaymentPending && status != PaymentExpired {
		return nil
	}
	var n int
	var sum sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), SUM(amount_minor) FROM student_payments WHERE payment_id = ?`, paymentID).Scan(&n, &sum); err != nil {
		return err
	}
	if n == 0 {
		return dropOrphanPayment(ctx, tx, paymentID)
	}
	_, err := tx.ExecContext(ctx, `UPDATE payments SET amount_minor = ? WHERE id = ?`, sum.Int64, paymentID)
	return err
}

// CreateFastPayment is the parent's "pay now": it bundles the (selected) open obligations of their
// children into one payment with a code. Reopening the dialog returns the same payment.
func (s *Store) CreateFastPayment(ctx context.Context, u User, classID int64, spIDs []int64) (Payment, error) {
	var pid int64
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		list, err := querySPs(ctx, tx, `WHERE sp.class_id = ? AND sp.status IN ('pending','partial') AND st.deleted_at IS NULL
			AND sp.student_id IN (SELECT student_id FROM student_parents WHERE user_id = ?) ORDER BY sp.id`, classID, u.ID)
		if err != nil {
			return err
		}
		if len(spIDs) > 0 {
			want := map[int64]bool{}
			for _, id := range spIDs {
				want[id] = true
			}
			filtered := list[:0:0]
			for _, sp := range list {
				if want[sp.ID] {
					filtered = append(filtered, sp)
				}
			}
			list = filtered
		}
		if len(list) == 0 {
			return ErrNothingToPay
		}
		var sum int64
		for _, sp := range list {
			sum += sp.AmountMinor
		}
		// Reuse an identical open payment instead of minting a new code on every click.
		if list[0].PaymentID != nil && (list[0].PaymentStatus == PaymentPending || list[0].PaymentStatus == PaymentExpired) {
			same := true
			for _, sp := range list {
				if sp.PaymentID == nil || *sp.PaymentID != *list[0].PaymentID {
					same = false
				}
			}
			var owner, amount, total int64
			var code sql.NullString
			if same && tx.QueryRowContext(ctx, `SELECT user_id, amount_minor, code, (SELECT COUNT(*) FROM student_payments WHERE payment_id = ?) FROM payments WHERE id = ?`, *list[0].PaymentID, *list[0].PaymentID).
				Scan(&owner, &amount, &code, &total) == nil && owner == u.ID && amount == sum && int(total) == len(list) && code.Valid {
				pid = *list[0].PaymentID
				return nil
			}
		}
		now := s.Now()
		pid, err = insertPayment(ctx, tx, now, classID, u.ID, sum, PaymentPending, true)
		if err != nil {
			return err
		}
		old := map[int64]bool{}
		for _, sp := range list {
			if sp.PaymentID != nil {
				old[*sp.PaymentID] = true
			}
			if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET payment_id = ? WHERE id = ?`, pid, sp.ID); err != nil {
				return err
			}
		}
		for id := range old {
			if err := reconcilePayment(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Payment{}, err
	}
	return s.PaymentByID(ctx, classID, pid)
}

// EnsureStudentPayment creates a code-bearing payment for one obligation (treasurer's "Utwórz płatność").
func (s *Store) EnsureStudentPayment(ctx context.Context, classID, spID int64, actor User) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		sp, err := s.studentPayment(ctx, tx, classID, spID)
		if err != nil {
			return err
		}
		if sp.PaymentID != nil || sp.Status == SPPaid {
			return nil
		}
		owner, err := payerFor(ctx, tx, sp.StudentID, actor.ID)
		if err != nil {
			return err
		}
		pid, err := insertPayment(ctx, tx, s.Now(), classID, owner, sp.AmountMinor, PaymentPending, true)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE student_payments SET payment_id = ? WHERE id = ?`, pid, spID)
		return err
	})
}

// payerFor picks the account that owns a payment for a student: the first parent, else the actor.
func payerFor(ctx context.Context, x querier, studentID, fallback int64) (int64, error) {
	var uid int64
	err := x.QueryRowContext(ctx, `SELECT user_id FROM student_parents WHERE student_id = ? ORDER BY user_id LIMIT 1`, studentID).Scan(&uid)
	if err == sql.ErrNoRows {
		return fallback, nil
	}
	return uid, err
}

// MarkStudentPaymentPaid records a cash settlement of one obligation.
func (s *Store) MarkStudentPaymentPaid(ctx context.Context, classID, spID int64, actor User) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		sp, err := s.studentPayment(ctx, tx, classID, spID)
		if err != nil {
			return err
		}
		if sp.Status == SPPaid {
			return nil
		}
		owner, err := payerFor(ctx, tx, sp.StudentID, actor.ID)
		if err != nil {
			return err
		}
		if sp.PaymentID != nil {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM student_payments WHERE payment_id = ?`, *sp.PaymentID).Scan(&n); err != nil {
				return err
			}
			if n == 1 {
				return s.settlePayment(ctx, tx, *sp.PaymentID)
			}
		}
		// Split this obligation off a shared payment (or create one) and settle only it.
		old := sp.PaymentID
		pid, err := insertPayment(ctx, tx, s.Now(), classID, owner, sp.AmountMinor, PaymentPending, false)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET payment_id = ? WHERE id = ?`, pid, spID); err != nil {
			return err
		}
		if old != nil {
			if err := reconcilePayment(ctx, tx, *old); err != nil {
				return err
			}
		}
		return s.settlePayment(ctx, tx, pid)
	})
}

// CancelStudentPayment withdraws an unpaid obligation and removes the student from the contribution.
func (s *Store) CancelStudentPayment(ctx context.Context, classID, spID int64) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		sp, err := s.studentPayment(ctx, tx, classID, spID)
		if err != nil {
			return err
		}
		if sp.Status == SPPaid {
			return errf(ErrConflict, "a paid obligation cannot be cancelled")
		}
		if sp.ContributionID != nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM contribution_students WHERE contribution_id = ? AND student_id = ?`, *sp.ContributionID, sp.StudentID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM student_payments WHERE id = ?`, spID); err != nil {
			return err
		}
		if sp.PaymentID != nil {
			return reconcilePayment(ctx, tx, *sp.PaymentID)
		}
		return nil
	})
}

// ---- payments ----

// PaymentByID loads a payment of the class with its obligations and received total.
func (s *Store) PaymentByID(ctx context.Context, classID, id int64) (Payment, error) {
	return s.loadPayment(ctx, s.DB, classID, id)
}

func (s *Store) loadPayment(ctx context.Context, x querier, classID, id int64) (Payment, error) {
	var p Payment
	var created string
	var paid, code sql.NullString
	err := x.QueryRowContext(ctx, `SELECT p.id, p.class_id, p.user_id, p.amount_minor, p.status, p.code, p.created_at, p.paid_at, u.name, u.email,
		COALESCE((SELECT SUM(amount_minor) FROM transfers WHERE payment_id = p.id AND deleted_at IS NULL),0)
		FROM payments p JOIN users u ON u.id = p.user_id WHERE p.id = ? AND p.class_id = ?`, id, classID).
		Scan(&p.ID, &p.ClassID, &p.UserID, &p.AmountMinor, &p.Status, &code, &created, &paid, &p.UserName, &p.UserEmail, &p.ReceivedMinor)
	if err != nil {
		return p, wrapNotFound(err)
	}
	p.Code, p.CreatedAt, p.PaidAt = code.String, parseTime(created), parseNullTime(paid)
	p.Items, err = querySPs(ctx, x, `WHERE sp.payment_id = ? ORDER BY sp.id`, id)
	return p, err
}

// AssignablePayments lists pending/expired payments, optionally filtered by code, payer or amount.
func (s *Store) AssignablePayments(ctx context.Context, classID int64, search string) ([]Payment, error) {
	q := `SELECT p.id FROM payments p JOIN users u ON u.id = p.user_id WHERE p.class_id = ? AND p.status IN ('pending','expired')`
	args := []any{classID}
	if search = strings.TrimSpace(search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		q += ` AND (LOWER(u.name) LIKE ? OR LOWER(u.email) LIKE ? OR LOWER(COALESCE(p.code,'')) LIKE ? OR printf('%.2f', p.amount_minor / 100.0) LIKE ?
			OR EXISTS (SELECT 1 FROM student_payments sp JOIN students st ON st.id = sp.student_id WHERE sp.payment_id = p.id AND (LOWER(st.first_name || ' ' || st.last_name) LIKE ? OR LOWER(sp.label) LIKE ?)))`
		args = append(args, like, like, like, like, like, like)
	}
	q += ` ORDER BY p.created_at DESC, p.id DESC LIMIT 200`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]Payment, 0, len(ids))
	for _, id := range ids {
		p, err := s.PaymentByID(ctx, classID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ExpirePending marks payments older than maxAge as expired (they stay assignable).
func (s *Store) ExpirePending(ctx context.Context, maxAge time.Duration) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE payments SET status = 'expired' WHERE status = 'pending' AND created_at < ?`, ts(s.Now().Add(-maxAge)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Transition applies an explicit state change: pay, fail, refund or expire.
func (s *Store) Transition(ctx context.Context, classID, paymentID int64, action string) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		p, err := s.loadPayment(ctx, tx, classID, paymentID)
		if err != nil {
			return err
		}
		switch action {
		case "pay":
			if p.Status != PaymentPending && p.Status != PaymentExpired {
				return errf(ErrConflict, "payment is %s", p.Status)
			}
			return s.settlePayment(ctx, tx, paymentID)
		case "fail":
			if p.Status != PaymentPending {
				return errf(ErrConflict, "payment is %s", p.Status)
			}
			_, err = tx.ExecContext(ctx, `UPDATE payments SET status = 'failed', code = NULL WHERE id = ?`, paymentID)
			return err
		case "expire":
			if p.Status != PaymentPending {
				return errf(ErrConflict, "payment is %s", p.Status)
			}
			_, err = tx.ExecContext(ctx, `UPDATE payments SET status = 'expired' WHERE id = ?`, paymentID)
			return err
		case "refund":
			if p.Status != PaymentPaid {
				return errf(ErrConflict, "payment is %s", p.Status)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = 'refunded' WHERE id = ?`, paymentID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET status = 'pending', paid_at = NULL WHERE payment_id = ?`, paymentID); err != nil {
				return err
			}
			return recalcLedger(ctx, tx, classID, s.Now())
		}
		return errf(ErrInvalid, "unknown transition %q", action)
	})
}

// settlePayment moves a payment to paid, settles its obligations, books the income and queues mail.
func (s *Store) settlePayment(ctx context.Context, tx *sql.Tx, paymentID int64) error {
	now := s.Now()
	var classID int64
	if err := tx.QueryRowContext(ctx, `SELECT class_id FROM payments WHERE id = ?`, paymentID).Scan(&classID); err != nil {
		return wrapNotFound(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = 'paid', paid_at = COALESCE(paid_at, ?), code = NULL WHERE id = ?`, ts(now), paymentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET status = 'paid', paid_at = COALESCE(paid_at, ?) WHERE payment_id = ? AND status <> 'paid'`, ts(now), paymentID); err != nil {
		return err
	}
	if err := recalcLedger(ctx, tx, classID, now); err != nil {
		return err
	}
	return s.queueSettlementMail(ctx, tx, classID, paymentID)
}

// ManualIncome books cash handed to the treasurer, optionally against one obligation.
func (s *Store) ManualIncome(ctx context.Context, classID int64, actor User, amountMinor int64, spID int64) error {
	if amountMinor <= 0 {
		return errf(ErrInvalid, "amount must be positive")
	}
	return s.InTx(ctx, func(tx *sql.Tx) error {
		now := s.Now()
		if spID == 0 {
			pid, err := insertPayment(ctx, tx, now, classID, actor.ID, amountMinor, PaymentPaid, false)
			if err != nil {
				return err
			}
			_ = pid
			return recalcLedger(ctx, tx, classID, now)
		}
		sp, err := s.studentPayment(ctx, tx, classID, spID)
		if err != nil {
			return err
		}
		if sp.Status == SPPaid {
			return errf(ErrConflict, "obligation is already paid")
		}
		owner, err := payerFor(ctx, tx, sp.StudentID, actor.ID)
		if err != nil {
			return err
		}
		pid, err := insertPayment(ctx, tx, now, classID, owner, amountMinor, PaymentPending, false)
		if err != nil {
			return err
		}
		old := sp.PaymentID
		if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET payment_id = ? WHERE id = ?`, pid, spID); err != nil {
			return err
		}
		if old != nil {
			if err := reconcilePayment(ctx, tx, *old); err != nil {
				return err
			}
		}
		if amountMinor < sp.AmountMinor {
			// Part payment: cash is booked, the obligation stays open as "partial".
			if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = 'paid', paid_at = ?, code = NULL WHERE id = ?`, ts(now), pid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET status = 'partial' WHERE id = ?`, spID); err != nil {
				return err
			}
			return recalcLedger(ctx, tx, classID, now)
		}
		return s.settlePayment(ctx, tx, pid)
	})
}

// ManualExpense books money spent from the class cash.
func (s *Store) ManualExpense(ctx context.Context, classID int64, label string, amountMinor int64, spentAt time.Time) (int64, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return 0, errf(ErrInvalid, "expense description is required")
	}
	if amountMinor <= 0 {
		return 0, errf(ErrInvalid, "amount must be positive")
	}
	var id int64
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO expenses (class_id, label, amount_minor, spent_at, created_at) VALUES (?,?,?,?,?)`, classID, label, amountMinor, ts(spentAt), ts(s.Now()))
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return recalcLedger(ctx, tx, classID, s.Now())
	})
	return id, err
}
