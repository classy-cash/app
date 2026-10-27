package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/classy-cash/app/internal/mail"
)

func mailMsgTo(key, to, subject string) mail.Message {
	return mail.Message{Key: key, ToEmail: to, Subject: subject, HTML: "<p>h</p>", Text: "t"}
}

func TestExportCoversEverythingHeldAboutTheAccount(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	pay, err := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.must(err)
	f.must(f.s.Enqueue(f.ctx, mailMsgTo("m1", "anna@example.com", "Zaproszenie")))
	f.must(f.s.Enqueue(f.ctx, mailMsgTo("m2", "someone@example.com", "Cudze")))

	ex, err := f.s.ExportUser(f.ctx, f.parent.ID)
	f.must(err)
	if ex.Profile.Email != "anna@example.com" || ex.Profile.Name != "Anna Kowalska" {
		t.Fatalf("profile %+v", ex.Profile)
	}
	if len(ex.Memberships) != 1 || ex.Memberships[0].Role != RoleParent || ex.Memberships[0].Class != "4A" {
		t.Fatalf("memberships %+v", ex.Memberships)
	}
	if len(ex.Children) != 1 || ex.Children[0].FirstName != "Jan" || len(ex.Children[0].Obligations) != 1 || ex.Children[0].Obligations[0].AmountPLN != "50.00" {
		t.Fatalf("children %+v", ex.Children)
	}
	if len(ex.Payments) != 1 || ex.Payments[0].Status != PaymentPending || ex.Payments[0].AmountPLN != "50.00" {
		t.Fatalf("payments %+v (payment %d)", ex.Payments, pay.ID)
	}
	if len(ex.Mail) != 1 || ex.Mail[0].Subject != "Zaproszenie" {
		t.Fatalf("only mail addressed to the person belongs in the export: %+v", ex.Mail)
	}
	if other, _ := f.s.ExportUser(f.ctx, f.treas.ID); len(other.Children) != 0 || len(other.Payments) != 0 {
		t.Fatal("an export must never include other people's children or payments")
	}
}

func TestEraseAnonymisesButKeepsTheClassBooks(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	pay, err := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.must(err)
	f.must(f.s.Transition(f.ctx, f.class.ID, pay.ID, "pay"))
	f.must(f.s.Enqueue(f.ctx, mailMsgTo("m1", "anna@example.com", "Potwierdzenie")))
	sess, err := f.s.CreateSession(f.ctx, f.parent)
	f.must(err)
	tok, err := f.s.CreateAuthToken(f.ctx, f.parent.ID, TokenLogin, time.Hour)
	f.must(err)
	before := f.cashBalance()

	f.must(f.s.EraseUser(f.ctx, f.parent.ID))
	f.must(f.s.EraseUser(f.ctx, f.parent.ID)) // idempotent

	u, err := f.s.UserByID(f.ctx, f.parent.ID)
	f.must(err)
	if !IsErased(u) || u.Name == "Anna Kowalska" || u.Phone != "" || u.PasswordHash != "" || strings.Contains(u.Email, "anna") {
		t.Fatalf("account not anonymised: %+v", u)
	}
	if _, err := f.s.UserByEmail(f.ctx, "anna@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatal("the old address must no longer resolve")
	}
	if _, _, err := f.s.SessionUser(f.ctx, sess); err == nil {
		t.Fatal("sessions must be revoked")
	}
	if _, err := f.s.ConsumeAuthToken(f.ctx, tok, TokenLogin); err == nil {
		t.Fatal("sign-in links must be revoked")
	}
	if mails, _ := f.s.RecentMail(f.ctx, 20); len(mails) != 0 {
		for _, m := range mails {
			if m.ToEmail == "anna@example.com" {
				t.Fatal("mail addressed to the person must be purged")
			}
		}
	}
	if classes, _ := f.s.ClassesFor(f.ctx, u); len(classes) != 0 {
		t.Fatal("memberships must be gone")
	}
	kid, err := f.s.StudentByID(f.ctx, f.class.ID, f.kid.ID)
	f.must(err)
	if len(kid.Parents) != 0 {
		t.Fatalf("the child must stay without the erased parent: %+v", kid.Parents)
	}
	// The class books are untouched: the payment is still paid and the till still adds up.
	if g, _ := f.s.PaymentByID(f.ctx, f.class.ID, pay.ID); g.Status != PaymentPaid || g.UserName == "Anna Kowalska" {
		t.Fatalf("payment %+v", g)
	}
	if f.cashBalance() != before || before != 5000 {
		t.Fatalf("balance %d, was %d", f.cashBalance(), before)
	}
}

func TestEraseRefusesTreasurerAndLastAdmin(t *testing.T) {
	f := newFixture(t)
	if err := f.s.EraseUser(f.ctx, f.treas.ID); !errors.Is(err, ErrIsTreasurer) {
		t.Fatalf("treasurer: %v", err)
	}
	admin, err := f.s.CreateUser(f.ctx, "admin@example.com", "Admin", "", true)
	f.must(err)
	// Onboarding made Teresa the first administrator too; two admins exist, so one may go...
	f.must(f.s.EraseUser(f.ctx, admin.ID))
	// ...but never the last one.
	only, _ := f.s.CreateUser(f.ctx, "solo@example.com", "Solo", "", false)
	_, _ = f.s.DB.ExecContext(f.ctx, `UPDATE users SET is_admin = 0 WHERE id = ?`, f.treas.ID)
	_, _ = f.s.DB.ExecContext(f.ctx, `UPDATE users SET is_admin = 1 WHERE id = ?`, only.ID)
	if err := f.s.EraseUser(f.ctx, only.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin: %v", err)
	}
}

// A second administrator (not a treasurer) who ran a bank-pipeline test: their export lists it, and erasure cuts
// the link to the person while the reference keeps swallowing notifications.
func TestExportAndEraseCoverBankTestReferences(t *testing.T) {
	f := newFixture(t)
	admin, err := f.s.CreateUser(f.ctx, "admin2@example.com", "Adam Admin", "", true)
	f.must(err)
	mine, err := f.s.CreateBankTest(f.ctx, f.class.ID, admin, 200)
	f.must(err)
	theirs := f.bankTest(300) // created by the treasurer
	f.deliver(evid(1), mine.Title(), "2,00", true)

	ex, err := f.s.ExportUser(f.ctx, admin.ID)
	f.must(err)
	if len(ex.BankTests) != 1 {
		t.Fatalf("export must list exactly the person's own references: %+v", ex.BankTests)
	}
	e := ex.BankTests[0]
	if e.Reference != mine.Title() || e.Class != "4A" || e.ExpectedPLN != "2.00" || e.Status != BankTestSuccess || e.Hits != 1 ||
		e.ObservedAt == nil || e.ObservedPLN == nil || *e.ObservedPLN != "2.00" {
		t.Fatalf("export entry %+v", e)
	}
	if blob, _ := json.Marshal(ex); strings.Contains(string(blob), theirs.Title()) {
		t.Fatal("another person's reference leaked into the export")
	}
	if none, _ := f.s.ExportUser(f.ctx, f.parent.ID); none.BankTests == nil || len(none.BankTests) != 0 {
		t.Fatalf("a person with none gets an empty list, not null: %#v", none.BankTests)
	}

	before := f.accounting()
	f.must(f.s.EraseUser(f.ctx, admin.ID))
	f.must(f.s.EraseUser(f.ctx, admin.ID)) // idempotent

	got, err := f.s.BankTestByID(f.ctx, f.class.ID, mine.ID)
	f.must(err)
	if got.CreatedByName != "" {
		t.Fatalf("the erased person is still named: %q", got.CreatedByName)
	}
	if got.Marker != mine.Marker || got.Stored != "success" || got.Hits != 1 || got.ExpectedMinor != 200 || got.EventID != evid(1) {
		t.Fatalf("erasure must keep the reference intact: %+v", got)
	}
	var by sql.NullInt64
	f.must(f.s.DB.QueryRowContext(f.ctx, `SELECT created_by FROM bank_tests WHERE id = ?`, mine.ID).Scan(&by))
	if by.Valid {
		t.Fatal("created_by must be cleared")
	}
	if kept, _ := f.s.BankTestByID(f.ctx, f.class.ID, theirs.ID); kept.CreatedByName != "Teresa Skarbnik" {
		t.Fatalf("someone else's reference was touched: %+v", kept)
	}
	if after, _ := f.s.ExportUser(f.ctx, admin.ID); len(after.BankTests) != 0 {
		t.Fatal("an erased account owns no references any more")
	}

	// Safety is unchanged: a repeat, and a notification for the other still-open reference, are swallowed.
	f.contribution(f.class.ID, "Wycieczka", 100, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	before = f.accounting()
	if res := f.deliver(evid(2), p.Code+" "+mine.Title(), "1,00", true); res.Outcome != OutcomeTest {
		t.Fatalf("an anonymised reference must still be swallowed: %+v", res)
	}
	if after := f.accounting(); after != before {
		t.Fatalf("accounting changed after erasure: %+v -> %+v", before, after)
	}
	if pay, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID); pay.Status != PaymentPending {
		t.Fatal("payment settled by an anonymised diagnostic")
	}
	if g, _ := f.s.BankTestByID(f.ctx, f.class.ID, mine.ID); g.Hits != 2 {
		t.Fatalf("hits %d", g.Hits)
	}
	// New references still get fresh markers.
	open, err := f.s.CreateBankTest(f.ctx, f.class.ID, f.treas, 100)
	f.must(err)
	if open.Marker == mine.Marker {
		t.Fatal("marker reused")
	}
}

func TestEraseBlockedForTreasurerKeepsTheirReferences(t *testing.T) {
	f := newFixture(t)
	bt := f.bankTest(100)
	if err := f.s.EraseUser(f.ctx, f.treas.ID); !errors.Is(err, ErrIsTreasurer) {
		t.Fatal(err)
	}
	if got, _ := f.s.BankTestByID(f.ctx, f.class.ID, bt.ID); got.CreatedByName != "Teresa Skarbnik" {
		t.Fatalf("a refused erasure must change nothing: %+v", got)
	}
}
