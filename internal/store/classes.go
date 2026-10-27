package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/auth"
)

// Class is a school class with its own payee details.
type Class struct {
	ID              int64
	Name            string
	SchoolName      string
	BlikPhone       string
	TransferAccount string
	CreatedAt       time.Time
	ArchivedAt      *time.Time // set = read-only history of a finished school year
}

func scanClass(r interface{ Scan(...any) error }) (Class, error) {
	var c Class
	var created string
	var archived sql.NullString
	err := r.Scan(&c.ID, &c.Name, &c.SchoolName, &c.BlikPhone, &c.TransferAccount, &created, &archived)
	c.CreatedAt, c.ArchivedAt = parseTime(created), parseNullTime(archived)
	return c, err
}

const classCols = `id, name, school_name, blik_phone, transfer_account, created_at, archived_at`

// CreateClass adds a class.
func (s *Store) CreateClass(ctx context.Context, name string) (Class, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Class{}, errf(ErrInvalid, "class name is required")
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO classes (name, created_at) VALUES (?,?)`, name, ts(s.Now()))
	if err != nil {
		return Class{}, err
	}
	id, _ := res.LastInsertId()
	return s.ClassByID(ctx, id)
}

// ClassByID loads a class.
func (s *Store) ClassByID(ctx context.Context, id int64) (Class, error) {
	c, err := scanClass(s.DB.QueryRowContext(ctx, `SELECT `+classCols+` FROM classes WHERE id = ?`, id))
	return c, wrapNotFound(err)
}

// UpdateClass saves payee details and names.
func (s *Store) UpdateClass(ctx context.Context, c Class) error {
	if strings.TrimSpace(c.Name) == "" {
		return errf(ErrInvalid, "class name is required")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE classes SET name = ?, school_name = ?, blik_phone = ?, transfer_account = ? WHERE id = ?`,
		strings.TrimSpace(c.Name), strings.TrimSpace(c.SchoolName), strings.TrimSpace(c.BlikPhone), strings.TrimSpace(c.TransferAccount), c.ID)
	return err
}

// ClassesFor lists the classes a user may enter: all for admins, memberships otherwise.
func (s *Store) ClassesFor(ctx context.Context, u User) ([]Class, error) {
	q := `SELECT ` + classCols + ` FROM classes ORDER BY archived_at IS NOT NULL, id`
	args := []any{}
	if !u.IsAdmin {
		q = `SELECT c.id, c.name, c.school_name, c.blik_phone, c.transfer_account, c.created_at, c.archived_at FROM classes c JOIN memberships m ON m.class_id = c.id WHERE m.user_id = ? ORDER BY c.archived_at IS NOT NULL, c.id`
		args = append(args, u.ID)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Class
	for rows.Next() {
		c, err := scanClass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RoleIn returns the user's role in a class ("" when not a member). Admins count as treasurer.
func (s *Store) RoleIn(ctx context.Context, u User, classID int64) (string, error) {
	var role string
	err := s.DB.QueryRowContext(ctx, `SELECT role FROM memberships WHERE user_id = ? AND class_id = ?`, u.ID, classID).Scan(&role)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if u.IsAdmin {
		var exists int
		if e := s.DB.QueryRowContext(ctx, `SELECT 1 FROM classes WHERE id = ?`, classID).Scan(&exists); e != nil {
			return "", nil
		}
		if role == "" {
			return RoleTreasurer, nil
		}
		if role != RoleTreasurer {
			return role, nil
		}
	}
	return role, nil
}

// CanManage reports treasurer-level access to the class (treasurer role or administrator). An archived class
// is read-only for everyone, administrators included, until it is restored.
func (s *Store) CanManage(ctx context.Context, u User, classID int64) bool {
	return s.hasManagerRole(ctx, u, classID) && !s.IsArchived(ctx, classID)
}

// hasManagerRole is the role check behind CanManage, ignoring the archive flag.
func (s *Store) hasManagerRole(ctx context.Context, u User, classID int64) bool {
	if u.IsAdmin {
		var exists int
		return s.DB.QueryRowContext(ctx, `SELECT 1 FROM classes WHERE id = ?`, classID).Scan(&exists) == nil
	}
	role, err := s.RoleIn(ctx, u, classID)
	return err == nil && role == RoleTreasurer
}

// IsArchived reports whether the class is archived.
func (s *Store) IsArchived(ctx context.Context, classID int64) bool {
	var at sql.NullString
	return s.DB.QueryRowContext(ctx, `SELECT archived_at FROM classes WHERE id = ?`, classID).Scan(&at) == nil && at.Valid
}

// IsCouncil reports a chairperson or deputy role in the class.
func IsCouncil(role string) bool { return role == RolePresident || role == RoleVicePresident }

// CanView reports read access to the class panel and reports: the treasurer and administrators
// (who can also change things) plus the class council, who can only look.
func (s *Store) CanView(ctx context.Context, u User, classID int64) bool {
	if s.hasManagerRole(ctx, u, classID) {
		return true
	}
	role, err := s.RoleIn(ctx, u, classID)
	return err == nil && IsCouncil(role)
}

// IsMember reports any membership (or administrator access) in the class.
func (s *Store) IsMember(ctx context.Context, u User, classID int64) bool {
	role, err := s.RoleIn(ctx, u, classID)
	return err == nil && role != ""
}

// SetMember adds or changes a membership; at most one treasurer exists per class.
func (s *Store) SetMember(ctx context.Context, classID, userID int64, role string) error {
	switch role {
	case RoleTreasurer, RolePresident, RoleVicePresident, RoleParent:
	default:
		return errf(ErrInvalid, "unknown role %q", role)
	}
	return s.InTx(ctx, func(tx *sql.Tx) error { return setMember(ctx, tx, classID, userID, role) })
}

func setMember(ctx context.Context, x querier, classID, userID int64, role string) error {
	// treasurer, president and vice_president are single seats per class.
	switch role {
	case RoleTreasurer, RolePresident, RoleVicePresident:
		var other int64
		err := x.QueryRowContext(ctx, `SELECT user_id FROM memberships WHERE class_id = ? AND role = ? AND user_id <> ?`, classID, role, userID).Scan(&other)
		if err == nil {
			return errf(ErrConflict, "the class already has a %s", role)
		}
		if err != sql.ErrNoRows {
			return err
		}
	}
	_, err := x.ExecContext(ctx, `INSERT INTO memberships (user_id, class_id, role) VALUES (?,?,?) ON CONFLICT(user_id, class_id) DO UPDATE SET role = excluded.role`, userID, classID, role)
	return err
}

// Members lists accounts that belong to the class with their role.
type Member struct {
	User User
	Role string
}

func (s *Store) Members(ctx context.Context, classID int64) ([]Member, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id, u.email, u.name, u.phone, u.password_hash, u.is_admin, u.session_epoch, u.created_at, u.confirmed_at, m.role
		FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.class_id = ? ORDER BY m.role, u.name`, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var u User
		var admin int
		var created string
		var conf sql.NullString
		var role string
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Phone, &u.PasswordHash, &admin, &u.Epoch, &created, &conf, &role); err != nil {
			return nil, err
		}
		u.IsAdmin, u.CreatedAt, u.ConfirmedAt = admin == 1, parseTime(created), parseNullTime(conf)
		out = append(out, Member{User: u, Role: role})
	}
	return out, rows.Err()
}

// OnboardInput is the first-run form.
type OnboardInput struct {
	FirstName, LastName, Email, Password string
	ClassName, SchoolName                string
	BlikPhone, Account                   string
}

// Onboard claims an empty instance: the first user becomes administrator and the class treasurer.
// It refuses when any user already exists, so a second visitor cannot take over.
func (s *Store) Onboard(ctx context.Context, in OnboardInput) (User, Class, error) {
	if len(in.Password) < auth.MinPasswordLen {
		return User{}, Class{}, errf(ErrInvalid, "password must have at least %d characters", auth.MinPasswordLen)
	}
	if strings.TrimSpace(in.ClassName) == "" {
		return User{}, Class{}, errf(ErrInvalid, "class name is required")
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return User{}, Class{}, err
	}
	var uid, cid int64
	err = s.InTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errf(ErrConflict, "instance is already set up")
		}
		now := s.Now()
		uid, err = createUser(ctx, tx, now, in.Email, strings.TrimSpace(in.FirstName+" "+in.LastName), hash, true, true)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO classes (name, school_name, blik_phone, transfer_account, created_at) VALUES (?,?,?,?,?)`,
			strings.TrimSpace(in.ClassName), strings.TrimSpace(in.SchoolName), strings.TrimSpace(in.BlikPhone), strings.TrimSpace(in.Account), ts(now))
		if err != nil {
			return err
		}
		cid, _ = res.LastInsertId()
		if err := setMember(ctx, tx, cid, uid, RoleTreasurer); err != nil {
			return err
		}
		for k, v := range map[string]string{"app_name": "Classy Cash " + strings.TrimSpace(in.ClassName), "parent_registration_enabled": "1", "bank_class_id": itoa(cid)} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return User{}, Class{}, err
	}
	u, _ := s.UserByID(ctx, uid)
	c, _ := s.ClassByID(ctx, cid)
	return u, c, nil
}

// Settings (instance-wide key/value).

// Setting reads a setting with a default.
func (s *Store) Setting(ctx context.Context, key, def string) string {
	var v string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

// SetSetting writes a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ParentRegistrationEnabled is the legacy parent_registration feature flag.
func (s *Store) ParentRegistrationEnabled(ctx context.Context) bool {
	return s.Setting(ctx, "parent_registration_enabled", "0") == "1"
}

// AppName is the display name of the installation.
func (s *Store) AppName(ctx context.Context) string { return s.Setting(ctx, "app_name", "Classy Cash") }

// ArchiveClass archives or restores a class.
func (s *Store) ArchiveClass(ctx context.Context, classID int64, archived bool) error {
	var at any
	if archived {
		at = ts(s.Now())
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE classes SET archived_at = ? WHERE id = ?`, at, classID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RolloverClass starts the next school year from an existing class: a new class with the same payee details,
// the active students with their parent links, the parents' memberships and the treasurer. Contributions,
// payments, the ledger and the class council are not carried over. With archiveOld the previous class is
// archived and, when it was the bank-notification class, the new one takes over that role. It returns the new
// class and the number of students copied.
func (s *Store) RolloverClass(ctx context.Context, fromID int64, newName string, archiveOld bool) (Class, int, error) {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return Class{}, 0, errf(ErrInvalid, "class name is required")
	}
	var newID int64
	copied := 0
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		old, err := scanClass(tx.QueryRowContext(ctx, `SELECT `+classCols+` FROM classes WHERE id = ?`, fromID))
		if err != nil {
			return wrapNotFound(err)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO classes (name, school_name, blik_phone, transfer_account, created_at) VALUES (?,?,?,?,?)`,
			newName, old.SchoolName, old.BlikPhone, old.TransferAccount, ts(s.Now()))
		if err != nil {
			return err
		}
		newID, _ = res.LastInsertId()
		srows, err := tx.QueryContext(ctx, `SELECT id, first_name, last_name FROM students WHERE class_id = ? AND deleted_at IS NULL ORDER BY id`, fromID)
		if err != nil {
			return err
		}
		type stu struct {
			id          int64
			first, last string
		}
		var students []stu
		for srows.Next() {
			var x stu
			if err := srows.Scan(&x.id, &x.first, &x.last); err != nil {
				srows.Close()
				return err
			}
			students = append(students, x)
		}
		srows.Close()
		for _, x := range students {
			r, err := tx.ExecContext(ctx, `INSERT INTO students (class_id, first_name, last_name) VALUES (?,?,?)`, newID, x.first, x.last)
			if err != nil {
				return err
			}
			nid, _ := r.LastInsertId()
			if _, err := tx.ExecContext(ctx, `INSERT INTO student_parents (student_id, user_id) SELECT ?, user_id FROM student_parents WHERE student_id = ?`, nid, x.id); err != nil {
				return err
			}
			copied++
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memberships (user_id, class_id, role) SELECT DISTINCT sp.user_id, ?, 'parent' FROM student_parents sp JOIN students st ON st.id = sp.student_id WHERE st.class_id = ?`, newID, newID); err != nil {
			return err
		}
		// The treasurer carries on (also a parent of a copied student: the treasurer seat wins).
		if _, err := tx.ExecContext(ctx, `DELETE FROM memberships WHERE class_id = ? AND user_id IN (SELECT user_id FROM memberships WHERE class_id = ? AND role = 'treasurer')`, newID, fromID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memberships (user_id, class_id, role) SELECT user_id, ?, 'treasurer' FROM memberships WHERE class_id = ? AND role = 'treasurer'`, newID, fromID); err != nil {
			return err
		}
		if archiveOld {
			if _, err := tx.ExecContext(ctx, `UPDATE classes SET archived_at = COALESCE(archived_at, ?) WHERE id = ?`, ts(s.Now()), fromID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = 'bank_class_id' AND value = ?`, itoa(newID), itoa(fromID)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Class{}, 0, err
	}
	c, err := s.ClassByID(ctx, newID)
	return c, copied, err
}
