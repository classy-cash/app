package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/classy-cash/app/internal/db"
	"github.com/classy-cash/app/internal/mail"
	"github.com/classy-cash/app/migrations"
)

type fixture struct {
	t      *testing.T
	s      *Store
	ctx    context.Context
	now    time.Time
	class  Class
	treas  User
	parent User
	kid    Student
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, migrations.FS); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Warsaw")
	s := New(conn, loc)
	s.BaseURL = "https://skarbiec.example"
	f := &fixture{t: t, s: s, ctx: context.Background(), now: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)}
	s.Now = func() time.Time { return f.now }
	f.treas, f.class, err = s.Onboard(f.ctx, OnboardInput{FirstName: "Teresa", LastName: "Skarbnik", Email: "teresa@example.com", Password: "password123", ClassName: "4A", SchoolName: "SP 1"})
	if err != nil {
		t.Fatal(err)
	}
	f.parent = f.user("anna@example.com", "Anna Kowalska")
	f.kid = f.student(f.class.ID, "Jan", "Kowalski", f.parent)
	return f
}

func (f *fixture) user(email, name string) User {
	f.t.Helper()
	u, err := f.s.CreateUser(f.ctx, email, name, "", false)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *fixture) student(classID int64, first, last string, parents ...User) Student {
	f.t.Helper()
	var ids []int64
	for _, p := range parents {
		ids = append(ids, p.ID)
	}
	st, err := f.s.AddStudent(f.ctx, classID, first, last, ids)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// contribution creates a collection for the given students and returns their obligations.
func (f *fixture) contribution(classID int64, title string, minor int64, students ...Student) []StudentPayment {
	f.t.Helper()
	var ids []int64
	for _, st := range students {
		ids = append(ids, st.ID)
	}
	if _, err := f.s.CreateContribution(f.ctx, classID, title, minor, nil, ids); err != nil {
		f.t.Fatal(err)
	}
	all, err := f.s.StudentPaymentsByClass(f.ctx, classID)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []StudentPayment
	for _, sp := range all {
		if sp.Label == title {
			out = append(out, sp)
		}
	}
	return out
}

func (f *fixture) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func mailMsg(key string) mail.Message {
	return mail.Message{Key: key, ToEmail: "x@example.com", Subject: "s", HTML: "<p>h</p>", Text: "t"}
}
