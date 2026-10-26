package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// DemoLoad atomically replaces all application data with the supplied seed SQL. It exists only for
// the external demo wrapper (classy-cash/demo): the app has no seed data and no reset schedule of
// its own. The caller (CLI) refuses to run unless DEMO_MODE is enabled.
//
// In one IMMEDIATE transaction every table except schema_migrations is emptied (foreign keys are
// checked at commit), the seed is executed and the "generation" setting is incremented. Sessions
// live in the database, so they are wiped too; browsers holding old cookies are logged out cleanly.
// The seed is the wrapper's responsibility; it must be valid for the current schema.
func (s *Store) DemoLoad(ctx context.Context, seedSQL string) (generation int64, err error) {
	err = s.InTx(ctx, func(tx *sql.Tx) error {
		prev, _ := strconv.ParseInt(s.settingTx(ctx, tx, "generation", "0"), 10, 64)
		if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`)
		if err != nil {
			return err
		}
		var tables []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			tables = append(tables, n)
		}
		rows.Close()
		for _, t := range tables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM "`+t+`"`); err != nil {
				return err
			}
		}
		if seedSQL != "" {
			if _, err := tx.ExecContext(ctx, seedSQL); err != nil {
				return fmt.Errorf("seed: %w", err)
			}
		}
		generation = prev + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('generation', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(generation, 10)); err != nil {
			return err
		}
		var bad string
		if err := tx.QueryRowContext(ctx, `SELECT "table" FROM pragma_foreign_key_check LIMIT 1`).Scan(&bad); err == nil {
			return fmt.Errorf("seed violates foreign keys in table %s", bad)
		} else if err != sql.ErrNoRows {
			return err
		}
		return nil
	})
	return generation, err
}

// Generation is the number of completed DemoLoad runs (0 outside the demo).
func (s *Store) Generation(ctx context.Context) int64 {
	n, _ := strconv.ParseInt(s.Setting(ctx, "generation", "0"), 10, 64)
	return n
}
