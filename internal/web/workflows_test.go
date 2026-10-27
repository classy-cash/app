package web

import (
	"context"
	"github.com/classy-cash/app/internal/auth"
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

func TestQuarantinedBankMailIsAdminOnly(t *testing.T) {
	h := newHarness(t, nil)
	admin, cls := h.onboarded()
	ctx := context.Background()
	if _, err := h.st.IngestBankMail(ctx, store.BankEventIn{ID: strings.Repeat("c9", 32), ReceivedAt: h.st.Now(), Recipient: "r", MailFrom: "m", Subject: "Tajny wyciąg", Body: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, body, _ := admin.get("/treasurer/payments"); !strings.Contains(body, "Tajny wyciąg") {
		t.Fatal("administrator must see quarantined mail")
	}
	hash, _ := auth.HashPassword("password222")
	u, _ := h.st.CreateUser(ctx, "t2@example.com", "Druga Skarbnik", hash, false)
	other, _ := h.st.CreateClass(ctx, "5B")
	_ = h.st.SetMember(ctx, other.ID, u.ID, store.RoleTreasurer)
	_ = cls
	tr2 := h.client()
	tr2.login("t2@example.com", "password222")
	if _, body, _ := tr2.get("/treasurer/payments"); strings.Contains(body, "Tajny wyciąg") {
		t.Fatal("another class's treasurer must not see the shared bank inbox")
	}
	if code, _, _ := tr2.post("/treasurer/bank-events/"+strings.Repeat("c9", 32)+"/replay", url.Values{}); code != 403 {
		t.Fatalf("replay by non-admin: %d", code)
	}
}

func TestClassCouncilIsReadOnlyOverHTTP(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	_, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	if _, err := h.st.CreateContribution(ctx, cls.ID, "=cmd|' /C calc'!A0", 5000, nil, []int64{kid.ID}); err != nil {
		t.Fatal(err)
	}

	// The treasurer appoints a chairperson and a deputy through the UI.
	for _, f := range []url.Values{
		{"email": {"ewa@example.com"}, "name": {"Ewa Przewodnicząca"}, "role": {"president"}},
		{"email": {"jan@example.com"}, "name": {"Jan Wice"}, "role": {"vice_president"}},
	} {
		if code, _, _ := tr.post("/treasurer/members", f); code != 303 {
			t.Fatal(code)
		}
	}
	if code, _, _ := tr.post("/treasurer/members", url.Values{"email": {"trzeci@example.com"}, "name": {"Trzeci"}, "role": {"president"}}); code != 303 {
		t.Fatal(code)
	}
	if _, body, _ := tr.get("/treasurer/members"); !strings.Contains(body, "Przewodniczący") {
		t.Fatal("members page")
	}
	if u, err := h.st.UserByEmail(ctx, "trzeci@example.com"); err == nil {
		if role, _ := h.st.RoleIn(ctx, u, cls.ID); role == store.RolePresident {
			t.Fatal("a second chairperson must be refused")
		}
	}
	if code, _, _ := tr.post("/treasurer/members", url.Values{"email": {"teresa@example.com"}, "role": {"president"}}); code != 303 {
		t.Fatal(code)
	}
	if role, _ := h.st.RoleIn(ctx, store.User{ID: 1}, cls.ID); role != store.RoleTreasurer {
		t.Fatal("the treasurer's seat must not be overwritten through the council form")
	}

	ewa, _ := h.st.UserByEmail(ctx, "ewa@example.com")
	hash, _ := auth.HashPassword("councilpass1")
	if err := h.st.SetPassword(ctx, ewa.ID, hash); err != nil {
		t.Fatal(err)
	}
	c := h.client()
	c.login("ewa@example.com", "councilpass1")
	for _, p := range []string{"/treasurer/dashboard", "/treasurer/payments", "/treasurer/contributions", "/treasurer/students", "/treasurer/reports", "/treasurer/members",
		"/treasurer/reports/contributions", "/treasurer/reports/outstanding", "/treasurer/reports/cash", "/treasurer/reports/parents", "/treasurer/reports/transfers"} {
		code, body, _ := c.get(p)
		if code != 200 {
			t.Errorf("council %s: %d", p, code)
			continue
		}
		if p == "/treasurer/dashboard" && (!strings.Contains(body, "Tryb podglądu") || strings.Contains(body, "Nowa transakcja")) {
			t.Error("council dashboard must show the read-only notice and no write buttons")
		}
		if p == "/treasurer/contributions" && (strings.Contains(body, "Uruchom składkę") || strings.Contains(body, "Oznacz jako opłacone")) {
			t.Error("council must not see contribution controls")
		}
	}
	for _, p := range []string{"/treasurer/manual-transactions", "/treasurer/settings", "/admin/mail", "/admin/classes"} {
		if code, _, _ := c.get(p); code != 403 {
			t.Errorf("council %s: %d, want 403", p, code)
		}
	}
	spID := func() string {
		all, _ := h.st.StudentPaymentsByClass(ctx, cls.ID)
		return strconv.FormatInt(all[0].ID, 10)
	}()
	for p, f := range map[string]url.Values{
		"/treasurer/contributions":                 {"title": {"X"}, "amount_pln": {"1"}},
		"/treasurer/obligations/" + spID + "/paid": {},
		"/treasurer/manual-transactions":           {"transaction_type": {"expense"}, "amount_pln": {"1"}, "expense_description": {"x"}},
		"/treasurer/students":                      {"first_name": {"Aa"}, "last_name": {"Bb"}},
		"/treasurer/members":                       {"email": {"x@example.com"}, "role": {"vice_president"}},
		"/treasurer/settings":                      {"app_name": {"Hack"}, "class_name": {"x"}},
	} {
		if code, _, _ := c.post(p, f); code != 403 {
			t.Errorf("council POST %s: %d, want 403", p, code)
		}
	}
	if all, _ := h.st.StudentPaymentsByClass(ctx, cls.ID); all[0].Status == store.SPPaid {
		t.Fatal("a council member must not be able to settle obligations")
	}
	// Landing page: own children's view with a link to the read-only panel.
	if code, body, _ := c.get("/parent"); code != 200 || !strings.Contains(body, "Panel rady klasowej") {
		t.Fatalf("council parent page: %d", code)
	}
	// An ordinary parent has none of this.
	anna := h.client()
	anna.login("anna@example.com", "parentpass1")
	for _, p := range []string{"/treasurer/reports", "/treasurer/reports/cash", "/treasurer/members"} {
		if code, _, _ := anna.get(p); code != 403 {
			t.Errorf("parent %s: %d, want 403", p, code)
		}
	}

	// CSV: BOM, semicolons, decimal comma, formula neutralised.
	code, csvBody, hd := tr.get("/treasurer/reports/contributions?format=csv")
	if code != 200 || !strings.HasPrefix(hd.Get("Content-Type"), "text/csv") || !strings.Contains(hd.Get("Content-Disposition"), "attachment") {
		t.Fatalf("csv headers: %d %v", code, hd)
	}
	if !strings.HasPrefix(csvBody, "\xef\xbb\xbf") || !strings.Contains(csvBody, ";50,00;") || strings.Contains(csvBody, ";=cmd") || !strings.Contains(csvBody, "'=cmd") {
		t.Fatalf("csv content/safety: %q", csvBody)
	}
	if code, _, _ := tr.get("/treasurer/reports/nonsense"); code != 404 {
		t.Fatal("unknown report")
	}
	if _, html, _ := tr.get("/treasurer/reports/outstanding"); !strings.Contains(html, "Zaległości razem") {
		t.Fatal("outstanding report page")
	}
}
