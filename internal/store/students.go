package store

import (
	"context"
	"database/sql"
	"strings"
)

// Student is a child in a class.
type Student struct {
	ID        int64
	ClassID   int64
	FirstName string
	LastName  string
	Parents   []User
}

// FullName is "First Last".
func (st Student) FullName() string { return strings.TrimSpace(st.FirstName + " " + st.LastName) }

// Initials are used for avatars.
func (st Student) Initials() string {
	r := func(s string) string {
		for _, c := range s {
			return strings.ToUpper(string(c))
		}
		return ""
	}
	return r(st.FirstName) + r(st.LastName)
}

// ListStudents returns the class roster (without merged/deleted entries) with linked parents.
func (s *Store) ListStudents(ctx context.Context, classID int64) ([]Student, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, class_id, first_name, last_name FROM students WHERE class_id = ? AND deleted_at IS NULL ORDER BY last_name COLLATE NOCASE, first_name COLLATE NOCASE`, classID)
	if err != nil {
		return nil, err
	}
	var out []Student
	idx := map[int64]int{}
	for rows.Next() {
		var st Student
		if err := rows.Scan(&st.ID, &st.ClassID, &st.FirstName, &st.LastName); err != nil {
			rows.Close()
			return nil, err
		}
		idx[st.ID] = len(out)
		out = append(out, st)
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}
	prows, err := s.DB.QueryContext(ctx, `SELECT sp.student_id, u.id, u.email, u.name, u.phone, u.password_hash, u.is_admin, u.session_epoch, u.created_at, u.confirmed_at
		FROM student_parents sp JOIN users u ON u.id = sp.user_id JOIN students st ON st.id = sp.student_id WHERE st.class_id = ? ORDER BY u.name`, classID)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var sid int64
		var u User
		var admin int
		var created string
		var conf sql.NullString
		if err := prows.Scan(&sid, &u.ID, &u.Email, &u.Name, &u.Phone, &u.PasswordHash, &admin, &u.Epoch, &created, &conf); err != nil {
			return nil, err
		}
		u.IsAdmin, u.CreatedAt, u.ConfirmedAt = admin == 1, parseTime(created), parseNullTime(conf)
		if i, ok := idx[sid]; ok {
			out[i].Parents = append(out[i].Parents, u)
		}
	}
	return out, prows.Err()
}

// StudentByID loads one student of the class.
func (s *Store) StudentByID(ctx context.Context, classID, id int64) (Student, error) {
	var st Student
	err := s.DB.QueryRowContext(ctx, `SELECT id, class_id, first_name, last_name FROM students WHERE id = ? AND class_id = ? AND deleted_at IS NULL`, id, classID).Scan(&st.ID, &st.ClassID, &st.FirstName, &st.LastName)
	return st, wrapNotFound(err)
}

// AddStudent creates a student; parentIDs must be existing accounts.
func (s *Store) AddStudent(ctx context.Context, classID int64, first, last string, parentIDs []int64) (Student, error) {
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if len([]rune(first)) < 2 || len([]rune(last)) < 2 || len([]rune(first)) > 100 || len([]rune(last)) > 100 {
		return Student{}, errf(ErrInvalid, "first and last name need 2-100 characters")
	}
	var id int64
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO students (class_id, first_name, last_name) VALUES (?,?,?)`, classID, first, last)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		for _, pid := range parentIDs {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO student_parents (student_id, user_id) VALUES (?,?)`, id, pid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memberships (user_id, class_id, role) VALUES (?,?, 'parent')`, pid, classID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Student{}, err
	}
	return s.StudentByID(ctx, classID, id)
}

// UpdateStudent renames a student.
func (s *Store) UpdateStudent(ctx context.Context, classID, id int64, first, last string) error {
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if len([]rune(first)) < 2 || len([]rune(last)) < 2 {
		return errf(ErrInvalid, "first and last name need at least 2 characters")
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE students SET first_name = ?, last_name = ? WHERE id = ? AND class_id = ? AND deleted_at IS NULL`, first, last, id, classID)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LinkParent attaches a parent account to a student and enrols the parent in the class.
func (s *Store) LinkParent(ctx context.Context, classID, studentID, userID int64) error {
	if _, err := s.StudentByID(ctx, classID, studentID); err != nil {
		return err
	}
	return s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO student_parents (student_id, user_id) VALUES (?,?)`, studentID, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memberships (user_id, class_id, role) VALUES (?,?, 'parent')`, userID, classID)
		return err
	})
}

// UnlinkParent detaches a parent from a student.
func (s *Store) UnlinkParent(ctx context.Context, classID, studentID, userID int64) error {
	if _, err := s.StudentByID(ctx, classID, studentID); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM student_parents WHERE student_id = ? AND user_id = ?`, studentID, userID)
	return err
}

// StudentsOfParent lists a parent's children in the class.
func (s *Store) StudentsOfParent(ctx context.Context, userID, classID int64) ([]Student, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT st.id, st.class_id, st.first_name, st.last_name FROM students st JOIN student_parents sp ON sp.student_id = st.id
		WHERE sp.user_id = ? AND st.class_id = ? AND st.deleted_at IS NULL ORDER BY st.first_name`, userID, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Student
	for rows.Next() {
		var st Student
		if err := rows.Scan(&st.ID, &st.ClassID, &st.FirstName, &st.LastName); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// MergeStudents moves payments, parents and contributions from del to keep and soft-deletes del.
// Where both students owe the same contribution only one obligation survives (paid wins).
func (s *Store) MergeStudents(ctx context.Context, classID, keepID, delID int64) error {
	if keepID == delID {
		return errf(ErrInvalid, "choose two different students")
	}
	if _, err := s.StudentByID(ctx, classID, keepID); err != nil {
		return err
	}
	if _, err := s.StudentByID(ctx, classID, delID); err != nil {
		return err
	}
	return s.InTx(ctx, func(tx *sql.Tx) error {
		type sp struct {
			id, contrib int64
			status      string
		}
		load := func(student int64) ([]sp, error) {
			rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(contribution_id,0), status FROM student_payments WHERE student_id = ?`, student)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var out []sp
			for rows.Next() {
				var x sp
				if err := rows.Scan(&x.id, &x.contrib, &x.status); err != nil {
					return nil, err
				}
				out = append(out, x)
			}
			return out, rows.Err()
		}
		keepSPs, err := load(keepID)
		if err != nil {
			return err
		}
		delSPs, err := load(delID)
		if err != nil {
			return err
		}
		for _, d := range delSPs {
			var dup *sp
			for i := range keepSPs {
				if d.contrib != 0 && keepSPs[i].contrib == d.contrib {
					dup = &keepSPs[i]
				}
			}
			switch {
			case dup == nil:
			case d.status != SPPaid:
				if err := deleteStudentPayment(ctx, tx, d.id); err != nil {
					return err
				}
				continue
			case dup.status != SPPaid:
				if err := deleteStudentPayment(ctx, tx, dup.id); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE student_payments SET student_id = ? WHERE id = ?`, keepID, d.id); err != nil {
				return err
			}
		}
		for _, q := range []string{
			`INSERT OR IGNORE INTO student_parents (student_id, user_id) SELECT ?, user_id FROM student_parents WHERE student_id = ?`,
			`INSERT OR IGNORE INTO contribution_students (contribution_id, student_id) SELECT contribution_id, ? FROM contribution_students WHERE student_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, keepID, delID); err != nil {
				return err
			}
		}
		for _, q := range []string{`DELETE FROM student_parents WHERE student_id = ?`, `DELETE FROM contribution_students WHERE student_id = ?`} {
			if _, err := tx.ExecContext(ctx, q, delID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE students SET deleted_at = ? WHERE id = ?`, ts(s.Now()), delID)
		return err
	})
}

// deleteStudentPayment removes an obligation and any now-empty, unpaid payment it was attached to.
func deleteStudentPayment(ctx context.Context, tx *sql.Tx, id int64) error {
	var payID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT payment_id FROM student_payments WHERE id = ?`, id).Scan(&payID); err != nil {
		return wrapNotFound(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM student_payments WHERE id = ?`, id); err != nil {
		return err
	}
	if payID.Valid {
		return dropOrphanPayment(ctx, tx, payID.Int64)
	}
	return nil
}

// dropOrphanPayment deletes a pending/expired payment that no obligation and no transfer references.
func dropOrphanPayment(ctx context.Context, tx *sql.Tx, paymentID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM payments WHERE id = ? AND status IN ('pending','expired')
		AND NOT EXISTS (SELECT 1 FROM student_payments WHERE payment_id = payments.id)
		AND NOT EXISTS (SELECT 1 FROM transfers WHERE payment_id = payments.id)`, paymentID)
	return err
}
