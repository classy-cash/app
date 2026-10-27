package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Corrections: late students, editing/closing/deleting collections, reversing manual cash entries.
// Nothing here rewrites history silently — a reversed income becomes a "refunded" payment and a voided
// expense keeps its row — and all of it is class-scoped like the rest of the store.

// ---- enrolment ----

// enrolStudent gives a student the obligation for a collection. It reports false when the student already
// has one, so the call is idempotent.
func enrolStudent(ctx context.Context, tx *sql.Tx, now time.Time, c Contribution, studentID int64) (bool, error) {
	var have int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM student_payments WHERE contribution_id = ? AND student_id = ?`, c.ID, studentID).Scan(&have); err != nil {
		return false, err
	}
	if have > 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO contribution_students (contribution_id, student_id) VALUES (?,?)`, c.ID, studentID); err != nil {
		return false, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO student_payments (class_id, student_id, contribution_id, label, amount_minor, status, created_at, due_at) VALUES (?,?,?,?,?,'pending',?,?)`,
		c.ClassID, studentID, c.ID, c.Title, c.AmountMinor, ts(now), nullTime(c.DueAt))
	return err == nil, err
}

func loadContribution(ctx context.Context, x querier, classID, id int64) (Contribution, error) {
	var c Contribution
	var due, closed sql.NullString
	var created string
	err := x.QueryRowContext(ctx, `SELECT id, class_id, title, amount_minor, due_at, created_at, closed_at FROM contributions WHERE id = ? AND class_id = ?`, id, classID).
		Scan(&c.ID, &c.ClassID, &c.Title, &c.AmountMinor, &due, &created, &closed)
	if err != nil {
		return c, wrapNotFound(err)
	}
	c.DueAt, c.CreatedAt, c.ClosedAt = parseNullTime(due), parseTime(created), parseNullTime(closed)
	return c, nil
}

func studentInClass(ctx context.Context, x querier, classID, studentID int64) error {
	var ok int
	if err := x.QueryRowContext(ctx, `SELECT 1 FROM students WHERE id = ? AND class_id = ? AND deleted_at IS NULL`, studentID, classID).Scan(&ok); err != nil {
		return wrapNotFound(err)
	}
	return nil
}

// AddStudentToContribution enrols one student (for example one who joined the class later) in a collection.
func (s *Store) AddStudentToContribution(ctx context.Context, classID, contributionID, studentID int64) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		c, err := loadContribution(ctx, tx, classID, contributionID)
		if err != nil {
			return err
		}
		if c.ClosedAt != nil {
			return errf(ErrConflict, "the collection is closed")
		}
		if err := studentInClass(ctx, tx, classID, studentID); err != nil {
			return err
		}
		_, err = enrolStudent(ctx, tx, s.Now(), c, studentID)
		return err
	})
}

// EnrolInOpenContributions puts a new student on every collection that is still running: not closed and
// not past its due date. Collections that already ended are left alone (add the student by hand if needed).
// It returns the titles of the collections joined.
func (s *Store) EnrolInOpenContributions(ctx context.Context, classID, studentID int64) ([]string, error) {
	var titles []string
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		if err := studentInClass(ctx, tx, classID, studentID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM contributions WHERE class_id = ? AND closed_at IS NULL AND (due_at IS NULL OR due_at >= ?) ORDER BY id`, classID, ts(s.Now()))
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			c, err := loadContribution(ctx, tx, classID, id)
			if err != nil {
				return err
			}
			added, err := enrolStudent(ctx, tx, s.Now(), c, studentID)
			if err != nil {
				return err
			}
			if added {
				titles = append(titles, c.Title)
			}
		}
		return nil
	})
	return titles, err
}

// ---- collections ----

// UpdateContribution changes the title, amount and due date. Unpaid obligations follow the new amount
// (and the payments that bundle them are re-totalled); paid ones keep what was actually owed and paid, and
// obligations with an individual amount keep it.
func (s *Store) UpdateContribution(ctx context.Context, classID, id int64, title string, amountMinor int64, dueAt *time.Time) error {
	title = strings.TrimSpace(title)
	if title == "" || amountMinor <= 0 {
		return errf(ErrInvalid, "title and a positive amount are required")
	}
	return s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := loadContribution(ctx, tx, classID, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE contributions SET title = ?, amount_minor = ?, due_at = ? WHERE id = ?`, title, amountMinor, nullTime(dueAt), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET label = ?, due_at = ? WHERE contribution_id = ?`, title, nullTime(dueAt), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET amount_minor = ? WHERE contribution_id = ? AND status <> 'paid' AND amount_override = 0`, amountMinor, id); err != nil {
			return err
		}
		return reconcileContributionPayments(ctx, tx, id)
	})
}

// reconcileContributionPayments re-totals the open payments that bundle obligations of a collection.
func reconcileContributionPayments(ctx context.Context, tx *sql.Tx, contributionID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT payment_id FROM student_payments WHERE contribution_id = ? AND payment_id IS NOT NULL`, contributionID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := reconcilePayment(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// SetContributionClosed archives a collection (no more enrolments, shown under "Zakończone") or reopens it.
// When closing, cancelUnpaid also withdraws the obligations nobody has started paying; part-paid and paid
// ones stay. It returns how many obligations were withdrawn.
func (s *Store) SetContributionClosed(ctx context.Context, classID, id int64, closed, cancelUnpaid bool) (int, error) {
	cancelled := 0
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := loadContribution(ctx, tx, classID, id); err != nil {
			return err
		}
		if !closed {
			_, err := tx.ExecContext(ctx, `UPDATE contributions SET closed_at = NULL WHERE id = ?`, id)
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE contributions SET closed_at = COALESCE(closed_at, ?) WHERE id = ?`, ts(s.Now()), id); err != nil {
			return err
		}
		if !cancelUnpaid {
			return nil
		}
		sps, err := querySPs(ctx, tx, `WHERE sp.contribution_id = ? AND sp.class_id = ? AND sp.status = 'pending'`, id, classID)
		if err != nil {
			return err
		}
		for _, sp := range sps {
			if err := cancelObligation(ctx, tx, sp); err != nil {
				return err
			}
			cancelled++
		}
		return nil
	})
	return cancelled, err
}

// DeleteContribution removes a collection that nobody has paid into (a creation mistake). Anything with a
// paid or part-paid obligation must be closed instead, so the money keeps its explanation.
func (s *Store) DeleteContribution(ctx context.Context, classID, id int64) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := loadContribution(ctx, tx, classID, id); err != nil {
			return err
		}
		sps, err := querySPs(ctx, tx, `WHERE sp.contribution_id = ? AND sp.class_id = ?`, id, classID)
		if err != nil {
			return err
		}
		for _, sp := range sps {
			if sp.Status != SPPending {
				return errf(ErrConflict, "the collection already has payments; close it instead")
			}
		}
		for _, sp := range sps {
			if err := cancelObligation(ctx, tx, sp); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM contributions WHERE id = ? AND class_id = ?`, id, classID)
		return err
	})
}

// ---- manual cash entries ----

// markManual flags a payment as booked by hand and keeps a description of what it was for.
func markManual(ctx context.Context, tx *sql.Tx, paymentID int64) error {
	sps, err := querySPs(ctx, tx, `WHERE sp.payment_id = ? ORDER BY sp.id`, paymentID)
	if err != nil {
		return err
	}
	note := "Wpłata ogólna"
	if len(sps) > 0 {
		parts := make([]string, 0, len(sps))
		for _, sp := range sps {
			parts = append(parts, sp.StudentName+" — "+sp.Label)
		}
		note = strings.Join(parts, "; ")
	}
	_, err = tx.ExecContext(ctx, `UPDATE payments SET source = 'manual', note = ? WHERE id = ?`, note, paymentID)
	return err
}

// ManualEntry is a line of the treasurer's hand-booked cash history, including reversed ones.
type ManualEntry struct {
	Kind        string // income | expense
	ID          int64
	Description string
	AmountMinor int64
	At          time.Time
	Reversed    bool
}

// ManualEntries lists hand-booked incomes and expenses, newest first, reversed ones included.
func (s *Store) ManualEntries(ctx context.Context, classID int64, limit int) ([]ManualEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT kind, id, descr, amount_minor, at, reversed FROM (
		SELECT 'income' AS kind, id, COALESCE(note,'Wpłata ogólna') AS descr, amount_minor, COALESCE(paid_at, created_at) AS at, status = 'refunded' AS reversed
			FROM payments WHERE class_id = ? AND source = 'manual' AND status IN ('paid','refunded')
		UNION ALL
		SELECT 'expense', id, label, amount_minor, spent_at, voided_at IS NOT NULL FROM expenses WHERE class_id = ?
	) ORDER BY at DESC, id DESC LIMIT ?`, classID, classID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ManualEntry
	for rows.Next() {
		var e ManualEntry
		var at string
		if err := rows.Scan(&e.Kind, &e.ID, &e.Description, &e.AmountMinor, &at, &e.Reversed); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReverseManualIncome cancels a hand-booked cash income that was entered by mistake: the payment becomes
// "refunded" (kept for the history), the obligations it settled reopen and the ledger is rebuilt without it.
func (s *Store) ReverseManualIncome(ctx context.Context, classID, paymentID int64) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		var source, status string
		if err := tx.QueryRowContext(ctx, `SELECT source, status FROM payments WHERE id = ? AND class_id = ?`, paymentID, classID).Scan(&source, &status); err != nil {
			return wrapNotFound(err)
		}
		if source != "manual" {
			return errf(ErrConflict, "only hand-booked income can be reversed; refund bank payments instead")
		}
		if status != PaymentPaid {
			return errf(ErrConflict, "payment is %s", status)
		}
		var transfers int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transfers WHERE payment_id = ? AND deleted_at IS NULL`, paymentID).Scan(&transfers); err != nil {
			return err
		}
		if transfers > 0 {
			return errf(ErrConflict, "payment has bank transfers attached")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = 'refunded' WHERE id = ?`, paymentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET status = 'pending', paid_at = NULL, payment_id = NULL WHERE payment_id = ?`, paymentID); err != nil {
			return err
		}
		return recalcLedger(ctx, tx, classID, s.Now())
	})
}

// VoidExpense cancels a hand-booked expense entered by mistake; the row stays for the history.
func (s *Store) VoidExpense(ctx context.Context, classID, id int64) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE expenses SET voided_at = ? WHERE id = ? AND class_id = ? AND voided_at IS NULL`, ts(s.Now()), id, classID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM expenses WHERE id = ? AND class_id = ?`, id, classID).Scan(&one); err != nil {
				return wrapNotFound(err)
			}
			return nil // already voided
		}
		return recalcLedger(ctx, tx, classID, s.Now())
	})
}

// ---- individual amounts ----

// SetObligationAmount gives one student's unpaid obligation an individual amount (sibling discount, partial
// exemption). Later edits of the collection's amount leave it alone. Only an obligation nobody has started
// paying can change; to waive it completely, cancel the obligation instead.
func (s *Store) SetObligationAmount(ctx context.Context, classID, spID, amountMinor int64) error {
	if amountMinor <= 0 {
		return errf(ErrInvalid, "amount must be positive; cancel the obligation to waive it")
	}
	return s.InTx(ctx, func(tx *sql.Tx) error {
		sp, err := s.studentPayment(ctx, tx, classID, spID)
		if err != nil {
			return err
		}
		if sp.Status != SPPending {
			return errf(ErrConflict, "only an obligation without payments can change amount")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET amount_minor = ?, amount_override = 1 WHERE id = ?`, amountMinor, spID); err != nil {
			return err
		}
		if sp.PaymentID != nil {
			return reconcilePayment(ctx, tx, *sp.PaymentID)
		}
		return nil
	})
}

// ResetObligationAmount puts an obligation back on the collection's amount.
func (s *Store) ResetObligationAmount(ctx context.Context, classID, spID int64) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		sp, err := s.studentPayment(ctx, tx, classID, spID)
		if err != nil {
			return err
		}
		if sp.ContributionID == nil {
			return errf(ErrConflict, "the obligation does not belong to a collection")
		}
		if sp.Status != SPPending {
			return errf(ErrConflict, "only an obligation without payments can change amount")
		}
		c, err := loadContribution(ctx, tx, classID, *sp.ContributionID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET amount_minor = ?, amount_override = 0 WHERE id = ?`, c.AmountMinor, spID); err != nil {
			return err
		}
		if sp.PaymentID != nil {
			return reconcilePayment(ctx, tx, *sp.PaymentID)
		}
		return nil
	})
}
