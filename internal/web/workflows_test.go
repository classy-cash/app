package web

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/classy-cash/app/internal/store"
)

func TestTreasurerWorkflowsOverHTTP(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()

	// Students, with a parent invited by e-mail.
	if code, _, _ := tr.post("/treasurer/students", url.Values{"first_name": {"Jan"}, "last_name": {"Kowalski"}, "parent_emails": {"anna@example.com, bad-address"}}); code != 303 {
		t.Fatal(code)
	}
	if students, _ := h.st.ListStudents(ctx, cls.ID); len(students) != 0 {
		t.Fatal("a malformed parent address must abort the whole add")
	}
	tr.post("/treasurer/students", url.Values{"first_name": {"Jan"}, "last_name": {"Kowalski"}, "parent_emails": {"anna@example.com"}})
	tr.post("/treasurer/students", url.Values{"first_name": {"Zofia"}, "last_name": {"Kowalska"}})
	students, _ := h.st.ListStudents(ctx, cls.ID)
	if len(students) != 2 || len(students[0].Parents)+len(students[1].Parents) != 1 {
		t.Fatalf("roster: %+v", students)
	}
	mails, _ := h.st.RecentMail(ctx, 10)
	var invite string
	for _, m := range mails {
		if m.ToEmail == "anna@example.com" && strings.Contains(m.Subject, "Zaproszenie") {
			invite = regexp.MustCompile(`/login/link/\S+`).FindString(m.Text)
		}
	}
	if invite == "" {
		t.Fatal("the new parent must receive an invitation with a login link")
	}
	anna := h.client()
	if code, _, _ := anna.post(invite, url.Values{}); code != 303 {
		t.Fatalf("invited parent logs in: %d", code)
	}
	// Link another parent to the second child through the form, then search suggestions stay class-local.
	zofia := students[0].ID
	if students[0].FirstName != "Zofia" {
		zofia = students[1].ID
	}
	if code, _, _ := tr.post("/treasurer/students/"+strconv.FormatInt(zofia, 10)+"/parents", url.Values{"email": {"anna@example.com"}}); code != 303 {
		t.Fatal(code)
	}
	_, body, _ := tr.get("/treasurer/users/search?q=anna")
	if !strings.Contains(body, "anna@example.com") {
		t.Fatalf("search: %s", body)
	}
	other, _ := h.st.CreateClass(ctx, "5B")
	h.parent("stranger@example.com", "Stranger Person", other, "Obcy", "Uczen")
	if _, body, _ := tr.get("/treasurer/users/search?q=stranger"); strings.Contains(body, "stranger@example.com") {
		t.Fatal("search must not leak other classes' parents")
	}

	// Contribution with validation.
	if code, _, _ := tr.post("/treasurer/contributions", url.Values{"title": {""}, "amount_pln": {"50"}}); code != 303 {
		t.Fatal(code)
	}
	if views, _ := h.st.ContributionViews(ctx, cls.ID); len(views) != 0 {
		t.Fatal("invalid contribution must not be created")
	}
	form := url.Values{"title": {"Wycieczka"}, "amount_pln": {"50,00"}, "due_at": {"2026-12-15"}, "students": {strconv.FormatInt(students[0].ID, 10), strconv.FormatInt(students[1].ID, 10)}}
	tr.post("/treasurer/contributions", form)
	views, _ := h.st.ContributionViews(ctx, cls.ID)
	if len(views) != 1 || len(views[0].Items) != 2 || views[0].AmountMinor != 5000 || views[0].DueAt == nil {
		t.Fatalf("contribution: %+v", views)
	}
	_, page, _ := tr.get("/treasurer/contributions")
	if !strings.Contains(page, "Wycieczka") || !strings.Contains(page, "0/2") {
		t.Fatal("contribution page must show progress")
	}

	// Obligation actions: create payment (code appears), mark paid, cancel.
	sps, _ := h.st.StudentPaymentsByClass(ctx, cls.ID)
	tr.post("/treasurer/obligations/"+strconv.FormatInt(sps[0].ID, 10)+"/payment", url.Values{})
	tr.post("/treasurer/obligations/"+strconv.FormatInt(sps[1].ID, 10)+"/paid", url.Values{})
	sps, _ = h.st.StudentPaymentsByClass(ctx, cls.ID)
	paid, coded := 0, 0
	for _, sp := range sps {
		if sp.Status == store.SPPaid {
			paid++
		}
		if sp.Code != "" {
			coded++
		}
	}
	if paid != 1 || coded != 1 {
		t.Fatalf("paid=%d coded=%d", paid, coded)
	}
	for _, sp := range sps {
		if sp.Status != store.SPPaid {
			tr.post("/treasurer/obligations/"+strconv.FormatInt(sp.ID, 10)+"/cancel", url.Values{})
		}
	}
	if left, _ := h.st.StudentPaymentsByClass(ctx, cls.ID); len(left) != 1 {
		t.Fatalf("cancel left %d obligations", len(left))
	}

	// Manual cash.
	tr.post("/treasurer/manual-transactions", url.Values{"transaction_type": {"income"}, "amount_pln": {"20"}})
	tr.post("/treasurer/manual-transactions", url.Values{"transaction_type": {"expense"}, "amount_pln": {"7,50"}, "expense_description": {"Kwiaty"}})
	if code, _, _ := tr.post("/treasurer/manual-transactions", url.Values{"transaction_type": {"expense"}, "amount_pln": {"abc"}, "expense_description": {"x"}}); code != 303 {
		t.Fatal(code)
	}
	sum, _ := h.st.Summary(ctx, cls.ID)
	if sum.TotalMinor != 5000+2000-750 {
		t.Fatalf("cash %d", sum.TotalMinor)
	}
	_, dash, _ := tr.get("/treasurer/dashboard")
	if !strings.Contains(dash, "Kwiaty") || !strings.Contains(dash, "Wpłata ogólna") {
		t.Fatal("dashboard must list manual operations")
	}

	// Merge.
	if code, _, _ := tr.post("/treasurer/students/merge", url.Values{"keep_student_id": {strconv.FormatInt(students[0].ID, 10)}, "delete_student_id": {strconv.FormatInt(students[1].ID, 10)}}); code != 303 {
		t.Fatal(code)
	}
	if left, _ := h.st.ListStudents(ctx, cls.ID); len(left) != 1 {
		t.Fatalf("merge left %d students", len(left))
	}

	// Settings validation and persistence; the new payee details reach the parent dialog.
	if code, body, _ := tr.post("/treasurer/settings", url.Values{"app_name": {""}, "class_name": {"4A"}, "email_from": {"nope"}}); code != 422 || !strings.Contains(body, "Podaj poprawny adres e-mail") {
		t.Fatalf("settings validation: %d", code)
	}
	tr.post("/treasurer/settings", url.Values{"app_name": {"Klasa 4A — kasa"}, "class_name": {"4A"}, "blik_phone": {"600 100 200"}, "transfer_account": {"PL 00 1111"}, "email_from": {"skarbnik@example.com"}, "parent_registration": {"1"}})
	if c, _ := h.st.ClassByID(ctx, cls.ID); c.BlikPhone != "600 100 200" || h.st.AppName(ctx) != "Klasa 4A — kasa" || !h.st.ParentRegistrationEnabled(ctx) {
		t.Fatal("settings not saved")
	}
	tr.post("/treasurer/settings", url.Values{"app_name": {"Klasa 4A — kasa"}, "class_name": {"4A"}})
	if h.st.ParentRegistrationEnabled(ctx) {
		t.Fatal("unchecked box must disable registration")
	}

	// Administrator: create a class with its treasurer; that treasurer is invited and confined to it.
	tr.post("/admin/classes", url.Values{"name": {"6C"}, "treasurer_email": {"nowa@example.com"}, "treasurer_name": {"Nowa Skarbnik"}})
	classes, _ := h.st.ClassesFor(ctx, store.User{IsAdmin: true})
	if len(classes) != 3 {
		t.Fatalf("classes: %d", len(classes))
	}
	nu, err := h.st.UserByEmail(ctx, "nowa@example.com")
	if err != nil {
		t.Fatal(err)
	}
	var c6 store.Class
	for _, c := range classes {
		if c.Name == "6C" {
			c6 = c
		}
	}
	if !h.st.CanManage(ctx, nu, c6.ID) || h.st.CanManage(ctx, nu, cls.ID) {
		t.Fatal("new treasurer must manage only their class")
	}
	if code, _, _ := tr.post("/class/switch", url.Values{"class_id": {strconv.FormatInt(c6.ID, 10)}}); code != 303 {
		t.Fatal(code)
	}
	if _, body, _ := tr.get("/treasurer/dashboard"); !strings.Contains(body, "6C") {
		t.Fatal("class switch must change the active class")
	}
	if code, _, _ := tr.post("/class/switch", url.Values{"class_id": {"999"}}); code != 303 {
		t.Fatal(code)
	}
}
