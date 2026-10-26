package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/auth"
)

// User is an account. Admins may act as treasurer in every class.
type User struct {
	ID           int64
	Email        string
	Name         string
	Phone        string
	PasswordHash string
	IsAdmin      bool
	Epoch        int64
	CreatedAt    time.Time
	ConfirmedAt  *time.Time
}

// FirstName is the part of the name before the first space.
func (u User) FirstName() string { f, _, _ := strings.Cut(strings.TrimSpace(u.Name), " "); return f }

// LastName is the part after the first space.
func (u User) LastName() string { _, l, _ := strings.Cut(strings.TrimSpace(u.Name), " "); return l }

const userCols = `id, email, name, phone, password_hash, is_admin, session_epoch, created_at, confirmed_at`

func scanUser(r interface{ Scan(...any) error }) (User, error) {
	var u User
	var admin int
	var created string
	var conf sql.NullString
	if err := r.Scan(&u.ID, &u.Email, &u.Name, &u.Phone, &u.PasswordHash, &admin, &u.Epoch, &created, &conf); err != nil {
		return u, err
	}
	u.IsAdmin = admin == 1
	u.CreatedAt = parseTime(created)
	u.ConfirmedAt = parseNullTime(conf)
	return u, nil
}

// CountUsers returns how many accounts exist (zero means the instance is unclaimed).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// NormalizeEmail lower-cases and trims an address.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func createUser(ctx context.Context, x querier, now time.Time, email, name, hash string, admin bool, confirmed bool) (int64, error) {
	email = NormalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") || strings.TrimSpace(name) == "" {
		return 0, errf(ErrInvalid, "email and name are required")
	}
	var conf any
	if confirmed {
		conf = ts(now)
	}
	res, err := x.ExecContext(ctx, `INSERT INTO users (email, name, password_hash, is_admin, created_at, confirmed_at) VALUES (?,?,?,?,?,?)`,
		email, strings.TrimSpace(name), hash, b2i(admin), ts(now), conf)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, errf(ErrConflict, "email already registered")
		}
		return 0, err
	}
	return res.LastInsertId()
}

// CreateUser registers an account (hash may be empty for passwordless accounts).
func (s *Store) CreateUser(ctx context.Context, email, name, hash string, admin bool) (User, error) {
	id, err := createUser(ctx, s.DB, s.Now(), email, name, hash, admin, false)
	if err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, id)
}

// UserByEmail looks an account up case-insensitively.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, NormalizeEmail(email)))
	return u, wrapNotFound(err)
}

// UserByID loads an account.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	return u, wrapNotFound(err)
}

// ConfirmUser marks the address as verified (first successful link login or password reset).
func (s *Store) ConfirmUser(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET confirmed_at = COALESCE(confirmed_at, ?), updated_at = ? WHERE id = ?`, ts(s.Now()), ts(s.Now()), id)
	return err
}

// UpdateProfile edits name and phone.
func (s *Store) UpdateProfile(ctx context.Context, id int64, name, phone string) error {
	if strings.TrimSpace(name) == "" {
		return errf(ErrInvalid, "name is required")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET name = ?, phone = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(name), strings.TrimSpace(phone), ts(s.Now()), id)
	return err
}

// SetPassword stores a new hash and revokes every existing session of the user.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, session_epoch = session_epoch + 1, confirmed_at = COALESCE(confirmed_at, ?), updated_at = ? WHERE id = ?`, hash, ts(s.Now()), ts(s.Now()), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
		return err
	})
}

// SearchUsers finds accounts by name or e-mail fragment (used to link parents to students).
func (s *Store) SearchUsers(ctx context.Context, q string, limit int) ([]User, error) {
	like := "%" + strings.ToLower(q) + "%"
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userCols+` FROM users WHERE LOWER(name) LIKE ? OR LOWER(email) LIKE ? ORDER BY id DESC LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Admins lists administrator accounts.
func (s *Store) Admins(ctx context.Context) ([]User, error) {
	return queryUsers(ctx, s.DB, `SELECT `+userCols+` FROM users WHERE is_admin = 1 ORDER BY id`)
}

func queryUsers(ctx context.Context, x querier, q string, args ...any) ([]User, error) {
	rows, err := x.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Session is a logged-in browser session.
type Session struct {
	UserID  int64
	ClassID sql.NullInt64
}

// SessionTTL is how long a login lasts (legacy remember-me: one year; shortened to 90 days).
const SessionTTL = 90 * 24 * time.Hour

// CreateSession issues a session and returns the raw cookie value.
func (s *Store) CreateSession(ctx context.Context, u User) (string, error) {
	raw, hash, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	now := s.Now()
	_, err = s.DB.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, epoch, created_at, expires_at) VALUES (?,?,?,?,?)`,
		hash, u.ID, u.Epoch, ts(now), ts(now.Add(SessionTTL)))
	return raw, err
}

// SessionUser resolves a cookie to its user, or ErrNotFound for unknown/expired/revoked sessions.
func (s *Store) SessionUser(ctx context.Context, raw string) (User, Session, error) {
	hash := auth.HashToken(raw)
	var sess Session
	var epoch int64
	var exp string
	err := s.DB.QueryRowContext(ctx, `SELECT user_id, epoch, class_id, expires_at FROM sessions WHERE token_hash = ?`, hash).Scan(&sess.UserID, &epoch, &sess.ClassID, &exp)
	if err != nil {
		return User{}, sess, wrapNotFound(err)
	}
	if parseTime(exp).Before(s.Now()) {
		_ = s.DeleteSession(ctx, raw)
		return User{}, sess, ErrNotFound
	}
	u, err := s.UserByID(ctx, sess.UserID)
	if err != nil {
		return User{}, sess, err
	}
	if u.Epoch != epoch {
		return User{}, sess, ErrNotFound
	}
	return u, sess, nil
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, raw string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, auth.HashToken(raw))
	return err
}

// SetSessionClass remembers the class a multi-class user is working in.
func (s *Store) SetSessionClass(ctx context.Context, raw string, classID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sessions SET class_id = ? WHERE token_hash = ?`, classID, auth.HashToken(raw))
	return err
}

// Token kinds.
const (
	TokenLogin = "login"
	TokenReset = "reset"
)

// CreateAuthToken issues a single-use token (login link or password reset) and returns its raw value.
func (s *Store) CreateAuthToken(ctx context.Context, userID int64, kind string, ttl time.Duration) (string, error) {
	raw, hash, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	// One live token per user and kind: a new request invalidates the previous link.
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM auth_tokens WHERE user_id = ? AND kind = ?`, userID, kind); err != nil {
		return "", err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO auth_tokens (token_hash, user_id, kind, expires_at) VALUES (?,?,?,?)`, hash, userID, kind, ts(s.Now().Add(ttl)))
	return raw, err
}

// PeekAuthToken reports the owner of a live token without consuming it.
func (s *Store) PeekAuthToken(ctx context.Context, raw, kind string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT user_id FROM auth_tokens WHERE token_hash = ? AND kind = ? AND used_at IS NULL AND expires_at > ?`,
		auth.HashToken(raw), kind, ts(s.Now())).Scan(&id)
	return id, wrapNotFound(err)
}

// ConsumeAuthToken atomically spends a live token.
func (s *Store) ConsumeAuthToken(ctx context.Context, raw, kind string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `UPDATE auth_tokens SET used_at = ? WHERE token_hash = ? AND kind = ? AND used_at IS NULL AND expires_at > ? RETURNING user_id`,
		ts(s.Now()), auth.HashToken(raw), kind, ts(s.Now())).Scan(&id)
	return id, wrapNotFound(err)
}

// PurgeExpired removes dead sessions and spent/expired tokens.
func (s *Store) PurgeExpired(ctx context.Context) error {
	now := ts(s.Now())
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM auth_tokens WHERE expires_at < ? OR used_at IS NOT NULL`, now)
	return err
}
