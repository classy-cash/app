package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"
)

// recalcLedger rebuilds the class cash ledger from first principles: every paid payment is income
// (dated by when it was paid, valued at the money actually received) and every expense is an outflow.
// Rebuilding makes the result deterministic and self-healing — no duplicate or stale rows survive.
func recalcLedger(ctx context.Context, tx *sql.Tx, classID int64, now time.Time) error {
	type entry struct {
		date    string
		kind    string
		amount  int64
		payment sql.NullInt64
		expense sql.NullInt64
	}
	var entries []entry
	rows, err := tx.QueryContext(ctx, `SELECT p.id, COALESCE(p.paid_at, p.created_at),
		CASE WHEN t.total > 0 THEN t.total ELSE p.amount_minor END
		FROM payments p LEFT JOIN (SELECT payment_id, SUM(amount_minor) AS total FROM transfers WHERE deleted_at IS NULL GROUP BY payment_id) t ON t.payment_id = p.id
		WHERE p.class_id = ? AND p.status = 'paid'`, classID)
	if err != nil {
		return err
	}
	for rows.Next() {
		e := entry{kind: "income"}
		if err := rows.Scan(&e.payment, &e.date, &e.amount); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	rows.Close()
	erows, err := tx.QueryContext(ctx, `SELECT id, spent_at, amount_minor FROM expenses WHERE class_id = ?`, classID)
	if err != nil {
		return err
	}
	for erows.Next() {
		e := entry{kind: "expense"}
		if err := erows.Scan(&e.expense, &e.date, &e.amount); err != nil {
			erows.Close()
			return err
		}
		entries = append(entries, e)
	}
	erows.Close()
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].date != entries[j].date {
			return entries[i].date < entries[j].date
		}
		if entries[i].kind != entries[j].kind {
			return entries[i].kind == "income"
		}
		return entries[i].payment.Int64+entries[i].expense.Int64 < entries[j].payment.Int64+entries[j].expense.Int64
	})
	if _, err := tx.ExecContext(ctx, `DELETE FROM ledger WHERE class_id = ?`, classID); err != nil {
		return err
	}
	var balance int64
	for _, e := range entries {
		if e.kind == "income" {
			balance += e.amount
		} else {
			balance -= e.amount
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ledger (class_id, tx_date, kind, amount_minor, balance_after_minor, payment_id, expense_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
			classID, e.date, e.kind, e.amount, balance, e.payment, e.expense, ts(now)); err != nil {
			return err
		}
	}
	return nil
}

// RecalculateLedger is the maintenance entry point (CLI: classycash ledger-rebuild).
func (s *Store) RecalculateLedger(ctx context.Context) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM classes`)
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
			if err := recalcLedger(ctx, tx, id, s.Now()); err != nil {
				return err
			}
		}
		return nil
	})
}

// CashSummary feeds the dashboard cards.
type CashSummary struct {
	TotalMinor         int64
	MonthCollected     int64
	OutstandingMinor   int64
	OutstandingCount   int
	OutstandingStudent int
}

// Summary computes the dashboard cards for a class. The month starts at local midnight.
func (s *Store) Summary(ctx context.Context, classID int64) (CashSummary, error) {
	var c CashSummary
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT balance_after_minor FROM ledger WHERE class_id = ? ORDER BY tx_date DESC, id DESC LIMIT 1),0)`, classID).Scan(&c.TotalMinor)
	if err != nil {
		return c, err
	}
	local := s.Now().In(s.Loc)
	monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, s.Loc)
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM ledger WHERE class_id = ? AND kind = 'income' AND tx_date >= ?`, classID, ts(monthStart)).Scan(&c.MonthCollected); err != nil {
		return c, err
	}
	err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(sp.amount_minor),0), COUNT(*), COUNT(DISTINCT sp.student_id)
		FROM student_payments sp JOIN students st ON st.id = sp.student_id WHERE sp.class_id = ? AND sp.status <> 'paid' AND st.deleted_at IS NULL`, classID).
		Scan(&c.OutstandingMinor, &c.OutstandingCount, &c.OutstandingStudent)
	return c, err
}

// LedgerRow is one line of "Ostatnio zaksięgowane operacje".
type LedgerRow struct {
	Kind         string // income | expense
	Description  string
	StudentName  string
	Method       string // Auto | Ręcznie | Wydatek
	AmountMinor  int64
	Date         time.Time
	BalanceAfter int64
}

// LedgerPage returns one page (newest first) and whether more rows exist.
func (s *Store) LedgerPage(ctx context.Context, classID int64, page, size int) ([]LedgerRow, bool, error) {
	if page < 1 {
		page = 1
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT l.kind, l.amount_minor, l.tx_date, l.balance_after_minor, l.payment_id, COALESCE(e.label,''),
		EXISTS (SELECT 1 FROM transfers t WHERE t.payment_id = l.payment_id AND t.deleted_at IS NULL)
		FROM ledger l LEFT JOIN expenses e ON e.id = l.expense_id WHERE l.class_id = ? ORDER BY l.tx_date DESC, l.id DESC LIMIT ? OFFSET ?`,
		classID, size+1, (page-1)*size)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	type raw struct {
		row LedgerRow
		pid sql.NullInt64
	}
	var raws []raw
	for rows.Next() {
		var r raw
		var date string
		var auto bool
		var label string
		if err := rows.Scan(&r.row.Kind, &r.row.AmountMinor, &date, &r.row.BalanceAfter, &r.pid, &label, &auto); err != nil {
			return nil, false, err
		}
		r.row.Date = parseTime(date)
		switch {
		case r.row.Kind == "expense":
			r.row.Description, r.row.Method = label, "Wydatek"
		case auto:
			r.row.Method = "Auto"
		default:
			r.row.Method = "Ręcznie"
		}
		raws = append(raws, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(raws) > size
	if more {
		raws = raws[:size]
	}
	out := make([]LedgerRow, 0, len(raws))
	for _, r := range raws {
		if r.pid.Valid {
			sps, err := querySPs(ctx, s.DB, `WHERE sp.payment_id = ? ORDER BY sp.id`, r.pid.Int64)
			if err != nil {
				return nil, false, err
			}
			if len(sps) == 0 {
				r.row.Description = "Wpłata ogólna"
			} else {
				seen := map[string]bool{}
				var labels, students []string
				for _, sp := range sps {
					if !seen["l"+sp.Label] {
						seen["l"+sp.Label] = true
						labels = append(labels, sp.Label)
					}
					if !seen["s"+sp.StudentName] {
						seen["s"+sp.StudentName] = true
						students = append(students, sp.StudentName)
					}
				}
				r.row.Description = strings.Join(labels, ", ")
				r.row.StudentName = strings.Join(students, ", ")
			}
		}
		out = append(out, r.row)
	}
	return out, more, nil
}

// LastLedgerBalance is the current cash balance of the class.
func (s *Store) LastLedgerBalance(ctx context.Context, classID int64) (int64, error) {
	sum, err := s.Summary(ctx, classID)
	return sum.TotalMinor, err
}
