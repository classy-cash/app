package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Personal-data rights (GDPR art. 15 and 17). Export gives a person everything the application holds about
// their account; erasure anonymises the account but keeps the class's accounting records intact, because the
// payments and the ledger belong to the class and must keep adding up.

// ErasedDomain is the reserved domain of the placeholder address an erased account is left with.
const ErasedDomain = "erased.invalid"

var (
	// ErrIsTreasurer blocks erasing a treasurer: the class would be left without one.
	ErrIsTreasurer = errors.New("the account is the treasurer of a class")
	// ErrLastAdmin blocks erasing the only administrator.
	ErrLastAdmin = errors.New("the account is the last administrator")
)

// UserExport is the machine-readable copy of a person's data.
type UserExport struct {
	ExportedAt  time.Time          `json:"exported_at"`
	Profile     ExportProfile      `json:"profile"`
	Memberships []ExportMembership `json:"memberships"`
	Children    []ExportChild      `json:"children"`
	Payments    []ExportPayment    `json:"payments"`
	BankTests   []ExportBankTest   `json:"bank_test_references"`
	Mail        []ExportMail       `json:"mail_sent_to_you"`
}

// ExportProfile is the account itself.
type ExportProfile struct {
	ID            int64      `json:"id"`
	Email         string     `json:"email"`
	Name          string     `json:"name"`
	Phone         string     `json:"phone"`
	Administrator bool       `json:"administrator"`
	CreatedAt     time.Time  `json:"created_at"`
	ConfirmedAt   *time.Time `json:"first_sign_in_at"`
}

// ExportMembership is a class the person belongs to.
type ExportMembership struct {
	Class string `json:"class"`
	Role  string `json:"role"`
}

// ExportChild is a student linked to the person, with their obligations.
type ExportChild struct {
	Class       string             `json:"class"`
	FirstName   string             `json:"first_name"`
	LastName    string             `json:"last_name"`
	Obligations []ExportObligation `json:"obligations"`
}

// ExportObligation is one amount owed for a child.
type ExportObligation struct {
	Label     string     `json:"label"`
	AmountPLN string     `json:"amount_pln"`
	Status    string     `json:"status"`
	DueAt     *time.Time `json:"due_at"`
	PaidAt    *time.Time `json:"paid_at"`
}

// ExportPayment is a payment the person initiated or that is booked under their account.
type ExportPayment struct {
	Class     string     `json:"class"`
	AmountPLN string     `json:"amount_pln"`
	Status    string     `json:"status"`
	Source    string     `json:"source"`
	CreatedAt time.Time  `json:"created_at"`
	PaidAt    *time.Time `json:"paid_at"`
}

// ExportBankTest is a bank-pipeline diagnostic reference the person created (never a payment).
type ExportBankTest struct {
	Class       string     `json:"class"`
	Reference   string     `json:"reference"`
	ExpectedPLN string     `json:"expected_amount_pln"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ObservedAt  *time.Time `json:"notification_received_at"`
	ObservedPLN *string    `json:"notification_amount_pln"`
	Hits        int        `json:"notifications_with_reference"`
}

// ExportMail lists messages by subject only; bodies are not part of the export.
type ExportMail struct {
	Subject   string    `json:"subject"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func plain(minor int64) string { return fmt.Sprintf("%d.%02d", minor/100, minor%100) }

// ExportUser collects everything held about the account.
func (s *Store) ExportUser(ctx context.Context, userID int64) (UserExport, error) {
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return UserExport{}, err
	}
	ex := UserExport{ExportedAt: s.Now(), Profile: ExportProfile{ID: u.ID, Email: u.Email, Name: u.Name, Phone: u.Phone, Administrator: u.IsAdmin, CreatedAt: u.CreatedAt, ConfirmedAt: u.ConfirmedAt},
		Memberships: []ExportMembership{}, Children: []ExportChild{}, Payments: []ExportPayment{}, BankTests: []ExportBankTest{}, Mail: []ExportMail{}}

	rows, err := s.DB.QueryContext(ctx, `SELECT c.name, m.role FROM memberships m JOIN classes c ON c.id = m.class_id WHERE m.user_id = ? ORDER BY c.id`, userID)
	if err != nil {
		return ex, err
	}
	for rows.Next() {
		var m ExportMembership
		if err := rows.Scan(&m.Class, &m.Role); err != nil {
			rows.Close()
			return ex, err
		}
		ex.Memberships = append(ex.Memberships, m)
	}
	rows.Close()

	krows, err := s.DB.QueryContext(ctx, `SELECT st.id, c.name, st.first_name, st.last_name FROM student_parents sp JOIN students st ON st.id = sp.student_id
		JOIN classes c ON c.id = st.class_id WHERE sp.user_id = ? AND st.deleted_at IS NULL ORDER BY st.id`, userID)
	if err != nil {
		return ex, err
	}
	type kid struct {
		id int64
		ExportChild
	}
	var kids []kid
	for krows.Next() {
		var k kid
		if err := krows.Scan(&k.id, &k.Class, &k.FirstName, &k.LastName); err != nil {
			krows.Close()
			return ex, err
		}
		k.Obligations = []ExportObligation{}
		kids = append(kids, k)
	}
	krows.Close()
	for _, k := range kids {
		orows, err := s.DB.QueryContext(ctx, `SELECT label, amount_minor, status, due_at, paid_at FROM student_payments WHERE student_id = ? ORDER BY id`, k.id)
		if err != nil {
			return ex, err
		}
		for orows.Next() {
			var o ExportObligation
			var amount int64
			var due, paid sql.NullString
			if err := orows.Scan(&o.Label, &amount, &o.Status, &due, &paid); err != nil {
				orows.Close()
				return ex, err
			}
			o.AmountPLN, o.DueAt, o.PaidAt = plain(amount), parseNullTime(due), parseNullTime(paid)
			k.Obligations = append(k.Obligations, o)
		}
		orows.Close()
		ex.Children = append(ex.Children, k.ExportChild)
	}

	prows, err := s.DB.QueryContext(ctx, `SELECT c.name, p.amount_minor, p.status, p.source, p.created_at, p.paid_at FROM payments p JOIN classes c ON c.id = p.class_id WHERE p.user_id = ? ORDER BY p.id`, userID)
	if err != nil {
		return ex, err
	}
	for prows.Next() {
		var p ExportPayment
		var amount int64
		var created string
		var paid sql.NullString
		if err := prows.Scan(&p.Class, &amount, &p.Status, &p.Source, &created, &paid); err != nil {
			prows.Close()
			return ex, err
		}
		p.AmountPLN, p.CreatedAt, p.PaidAt = plain(amount), parseTime(created), parseNullTime(paid)
		ex.Payments = append(ex.Payments, p)
	}
	prows.Close()

	brows, err := s.DB.QueryContext(ctx, `SELECT `+bankTestCols+`, c.name FROM bank_tests t JOIN classes c ON c.id = t.class_id LEFT JOIN users u ON u.id = t.created_by WHERE t.created_by = ? ORDER BY t.id`, userID)
	if err != nil {
		return ex, err
	}
	for brows.Next() {
		var bt BankTest
		var created, expires string
		var cancelled, observed sql.NullString
		var obsMinor sql.NullInt64
		var parsed int
		var class string
		if err := brows.Scan(&bt.ID, &bt.ClassID, &bt.Marker, &bt.ExpectedMinor, &bt.Stored, &created, &expires, &cancelled, &bt.CreatedByName, &bt.EventID, &observed, &obsMinor, &parsed, &bt.Hits, &class); err != nil {
			brows.Close()
			return ex, err
		}
		bt.CreatedAt, bt.ExpiresAt = parseTime(created), parseTime(expires)
		e := ExportBankTest{Class: class, Reference: bt.Title(), ExpectedPLN: plain(bt.ExpectedMinor), Status: bt.State(s.Now()),
			CreatedAt: bt.CreatedAt, ExpiresAt: bt.ExpiresAt, ObservedAt: parseNullTime(observed), Hits: bt.Hits}
		if obsMinor.Valid {
			v := plain(obsMinor.Int64)
			e.ObservedPLN = &v
		}
		ex.BankTests = append(ex.BankTests, e)
	}
	brows.Close()

	mrows, err := s.DB.QueryContext(ctx, `SELECT subject, status, created_at FROM outbox WHERE to_email = ? COLLATE NOCASE ORDER BY id`, u.Email)
	if err != nil {
		return ex, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var m ExportMail
		var created string
		if err := mrows.Scan(&m.Subject, &m.Status, &created); err != nil {
			return ex, err
		}
		m.CreatedAt = parseTime(created)
		ex.Mail = append(ex.Mail, m)
	}
	return ex, mrows.Err()
}

// IsErased reports whether the account has already been anonymised.
func IsErased(u User) bool { return strings.HasSuffix(u.Email, "@"+ErasedDomain) }

// EraseUser anonymises an account: name, address, phone and password are removed, sessions, sign-in links,
// memberships and parent links are deleted, and mail addressed to the person is purged. Payments, obligations
// and the ledger stay (they belong to the class) but no longer point at a real person. A treasurer or the last
// administrator must be replaced first. Erasing twice is harmless.
func (s *Store) EraseUser(ctx context.Context, userID int64) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, userID))
		if err != nil {
			return wrapNotFound(err)
		}
		if IsErased(u) {
			return nil
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memberships WHERE user_id = ? AND role = 'treasurer'`, userID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrIsTreasurer
		}
		if u.IsAdmin {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1 AND id <> ?`, userID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrLastAdmin
			}
		}
		for _, q := range []string{
			`DELETE FROM sessions WHERE user_id = ?`,
			`DELETE FROM auth_tokens WHERE user_id = ?`,
			`DELETE FROM memberships WHERE user_id = ?`,
			`DELETE FROM student_parents WHERE user_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, userID); err != nil {
				return err
			}
		}
		// Diagnostic references stay (marker, status, hits) so a notification that still arrives is swallowed; only
		// the link to the person goes.
		if _, err := tx.ExecContext(ctx, `UPDATE bank_tests SET created_by = NULL WHERE created_by = ?`, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE to_email = ? COLLATE NOCASE`, u.Email); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE users SET email = ?, name = 'Usunięty użytkownik', phone = '', password_hash = '', is_admin = 0,
			session_epoch = session_epoch + 1, confirmed_at = NULL, updated_at = ? WHERE id = ?`,
			fmt.Sprintf("erased-%d@%s", userID, ErasedDomain), ts(s.Now()), userID)
		return err
	})
}
