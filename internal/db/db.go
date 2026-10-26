// Package db opens SQLite with the production pragmas and applies embedded, versioned migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: CGO-free static binaries
)

// Open opens (creating if needed) the database. Foreign keys, WAL, a busy timeout and IMMEDIATE
// write transactions are applied to every connection; the pool is small to fit 256 MB hosts.
func Open(path string) (*sql.DB, error) {
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		path = abs
	}
	q := url.Values{}
	q.Set("_txlock", "immediate")
	for _, p := range []string{
		"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(1)", "synchronous(NORMAL)",
		"cache_size(-4000)", "journal_size_limit(8388608)", "wal_autocheckpoint(500)",
	} {
		q.Add("_pragma", p)
	}
	dsn := "file:" + path + "?" + q.Encode()
	if path == ":memory:" {
		dsn = "file::memory:?cache=shared&" + q.Encode()
	}
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(4)
	conn.SetConnMaxIdleTime(5 * time.Minute)
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return conn, nil
}

// Migrate applies every NNNN_name.sql from migrations that has not been applied yet.
func Migrate(conn *sql.DB, migrations fs.FS) error {
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	names, err := Names(migrations)
	if err != nil {
		return err
	}
	for _, name := range names {
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			return err
		}
		if err := apply(conn, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// Names lists migration files in application order.
func Names(migrations fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(migrations, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Pending returns the migrations that have not been applied (used by /ready).
func Pending(conn *sql.DB, migrations fs.FS) ([]string, error) {
	names, err := Names(migrations)
	if err != nil {
		return nil, err
	}
	applied := map[string]bool{}
	rows, err := conn.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return names, nil // table missing: everything is pending
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		applied[n] = true
	}
	var out []string
	for _, n := range names {
		if !applied[n] {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

func apply(conn *sql.DB, name, body string) error {
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}
