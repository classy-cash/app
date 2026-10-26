package db

import (
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestMigrateIsIdempotentAndOrdered(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fs := fstest.MapFS{
		"0002_b.sql": {Data: []byte(`INSERT INTO t (v) VALUES ('second');`)},
		"0001_a.sql": {Data: []byte(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT);`)},
		"readme.txt": {Data: []byte("ignored")},
	}
	if p, _ := Pending(conn, fs); len(p) != 2 {
		t.Fatalf("pending before: %v", p)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(conn, fs); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	conn.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n)
	if n != 1 {
		t.Fatalf("each migration must run exactly once, rows=%d", n)
	}
	if p, _ := Pending(conn, fs); len(p) != 0 {
		t.Fatalf("pending after: %v", p)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	conn, _ := Open(filepath.Join(t.TempDir(), "m.db"))
	defer conn.Close()
	fs := fstest.MapFS{"0001_bad.sql": {Data: []byte(`CREATE TABLE ok (id INTEGER); INSERT INTO missing VALUES (1);`)}}
	if err := Migrate(conn, fs); err == nil {
		t.Fatal("bad migration must fail")
	}
	var n int
	conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'ok'`).Scan(&n)
	if n != 0 {
		t.Fatal("a failed migration must leave no partial schema")
	}
	if p, _ := Pending(conn, fs); len(p) != 1 {
		t.Fatal("failed migration must stay pending")
	}
}

func TestPragmasAreApplied(t *testing.T) {
	conn, _ := Open(filepath.Join(t.TempDir(), "p.db"))
	defer conn.Close()
	var fk int
	var mode string
	conn.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	conn.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if fk != 1 || mode != "wal" {
		t.Fatalf("foreign_keys=%d journal_mode=%s", fk, mode)
	}
}
