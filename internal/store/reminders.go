package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/mail"
)

// ReminderCooldown is how long a parent is spared a second reminder from the same class.
const ReminderCooldown = 72 * time.Hour

// ReminderResult reports what RemindParents did.
type ReminderResult struct {
	Sent    int // parents a reminder was queued for
	Skipped int // parents reminded within the cooldown
}

// RemindParents queues one reminder per parent listing the unpaid obligations of their children. It is
// always a deliberate act of the treasurer (nothing runs on a schedule). contributionID narrows it to one
// collection (0 = all); overdueOnly keeps only obligations past their due date. A parent reminded in the
// last ReminderCooldown is skipped, so a double click or a daily habit cannot spam anyone.
func (s *Store) RemindParents(ctx context.Context, classID, contributionID int64, overdueOnly bool, actor User) (ReminderResult, error) {
	var res ReminderResult
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		class, err := scanClass(tx.QueryRowContext(ctx, `SELECT `+classCols+` FROM classes WHERE id = ?`, classID))
		if err != nil {
			return wrapNotFound(err)
		}
		where := `WHERE sp.class_id = ? AND sp.status IN ('pending','partial') AND st.deleted_at IS NULL`
		args := []any{classID}
		if contributionID != 0 {
			where += ` AND sp.contribution_id = ?`
			args = append(args, contributionID)
		}
		if overdueOnly {
			where += ` AND sp.due_at IS NOT NULL AND sp.due_at < ?`
			args = append(args, ts(s.Now()))
		}
		sps, err := querySPs(ctx, tx, where+` ORDER BY st.last_name, st.first_name, sp.id`, args...)
		if err != nil {
			return err
		}
		type parent struct {
			u     User
			lines []mail.ReminderLine
		}
		parents := map[int64]*parent{}
		for _, sp := range sps {
			urows, err := queryUsers(ctx, tx, `SELECT `+userCols+` FROM users WHERE id IN (SELECT user_id FROM student_parents WHERE student_id = ?) ORDER BY id`, sp.StudentID)
			if err != nil {
				return err
			}
			for _, u := range urows {
				if IsErased(u) {
					continue
				}
				p := parents[u.ID]
				if p == nil {
					p = &parent{u: u}
					parents[u.ID] = p
				}
				p.lines = append(p.lines, mail.ReminderLine{Student: sp.StudentName, Label: sp.Label, AmountMinor: sp.AmountMinor, DueAt: sp.DueAt})
			}
		}
		ids := make([]int64, 0, len(parents))
		for id := range parents {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		branding := s.brandingTx(ctx, tx)
		for _, id := range ids {
			p := parents[id]
			var last sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT MAX(created_at) FROM outbox WHERE idempotency_key LIKE ?`, fmt.Sprintf("remind-%d-%d-%%", classID, id)).Scan(&last); err != nil {
				return err
			}
			if t := parseNullTime(last); t != nil && s.Now().Sub(*t) < ReminderCooldown {
				res.Skipped++
				continue
			}
			r := mail.Reminder(branding, mail.ReminderData{ParentName: p.u.Name, ClassName: class.Name, Treasurer: strings.TrimSpace(actor.Name),
				Lines: p.lines, BlikPhone: class.BlikPhone, Account: class.TransferAccount, PayURL: s.BaseURL + "/parent"}, s.Loc)
			if err := enqueue(ctx, tx, s.Now(), mail.Message{Key: fmt.Sprintf("remind-%d-%d-%d", classID, id, s.Now().Unix()), ToEmail: p.u.Email, ToName: p.u.Name, Subject: r.Subject, HTML: r.HTML, Text: r.Text}); err != nil {
				return err
			}
			res.Sent++
		}
		return nil
	})
	return res, err
}
