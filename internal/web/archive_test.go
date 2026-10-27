package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/classy-cash/app/internal/auth"
	"github.com/classy-cash/app/internal/store"
)

func TestArchivingAClassFreezesItOverHTTP(t *testing.T) {
	h := newHarness(t, nil)
	admin, cls := h.onboarded() // the first user is administrator and treasurer
	ctx := context.Background()
	_, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	if _, err := h.st.CreateContribution(ctx, cls.ID, "Wycieczka", 5000, nil, []int64{kid.ID}); err != nil {
		t.Fatal(err)
	}
	tre, _ := h.st.CreateUser(ctx, "skarb@example.com", "Skarbnik Drugi", "", false)
	hash, _ := auth.HashPassword("treasurer-pass1")
	h.st.SetPassword(ctx, tre.ID, hash)
	// Hand the treasurer seat to a non-admin so both kinds of manager can be observed.
	if err := h.st.SetMember(ctx, cls.ID, adminUser(h).ID, "parent"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMember(ctx, cls.ID, tre.ID, "treasurer"); err != nil {
		t.Fatal(err)
	}
	treas := h.client()
	treas.login("skarb@example.com", "treasurer-pass1")
	anna := h.client()
	anna.login("anna@example.com", "parentpass1")

	_, page, _ := admin.get("/admin/classes")
	if !strings.Contains(page, "Archiwizuj") || !strings.Contains(page, "Nowy rok szkolny") {
		t.Fatal("admin page must offer archive and rollover")
	}
	if code, _, _ := admin.post("/admin/classes/"+id(cls.ID)+"/archive", url.Values{}); code != 303 {
		t.Fatal(code)
	}
	// Read access survives, with an explanatory banner and no write controls.
	code, body, _ := treas.get("/treasurer/contributions")
	if code != 200 || !strings.Contains(body, "Klasa zarchiwizowana") || strings.Contains(body, "Uruchom składkę") || strings.Contains(body, "Edytuj lub zakończ") {
		t.Fatalf("archived contributions page: %d", code)
	}
	for _, p := range []string{"/treasurer/dashboard", "/treasurer/payments", "/treasurer/students", "/treasurer/reports"} {
		if code, _, _ := treas.get(p); code != 200 {
			t.Errorf("treasurer must still read %s: %d", p, code)
		}
	}
	// Every write is refused with 403, for the treasurer and for the administrator.
	forms := map[string]url.Values{
		"/treasurer/contributions":       {"title": {"X"}, "amount_pln": {"1"}},
		"/treasurer/students":            {"first_name": {"Aa"}, "last_name": {"Bb"}},
		"/treasurer/manual-transactions": {"transaction_type": {"expense"}, "amount_pln": {"1"}, "expense_description": {"x"}},
		"/treasurer/reminders":           {"scope": {"all"}},
		"/treasurer/settings":            {"app_name": {"X"}, "class_name": {"Y"}},
	}
	for name, c := range map[string]*client{"treasurer": treas, "admin": admin} {
		for p, f := range forms {
			if code, _, _ := c.post(p, f); code != 403 {
				t.Errorf("%s POST %s on an archived class: %d, want 403", name, p, code)
			}
		}
	}
	if code, _, _ := anna.post("/parent/pay", url.Values{}); code != 403 {
		t.Fatalf("parents cannot open new payments in an archived class: %d", code)
	}
	if v, _ := h.st.ContributionViews(ctx, cls.ID); len(v) != 1 {
		t.Fatal("nothing may have changed")
	}
	if _, body, _ := anna.get("/parent"); !strings.Contains(body, "Wycieczka") {
		t.Fatal("parents still see their history")
	}

	// Restore gives it all back.
	admin.post("/admin/classes/"+id(cls.ID)+"/restore", url.Values{})
	if code, _, _ := treas.post("/treasurer/contributions", url.Values{"title": {"Po przywróceniu"}, "amount_pln": {"10"}}); code != 303 {
		t.Fatalf("restored class must accept writes: %d", code)
	}
}

func TestRolloverFromTheAdminPage(t *testing.T) {
	h := newHarness(t, nil)
	admin, cls := h.onboarded()
	ctx := context.Background()
	h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")

	if code, _, _ := admin.post("/admin/classes/"+id(cls.ID)+"/rollover", url.Values{"name": {""}}); code != 303 {
		t.Fatal(code)
	}
	if classes, _ := h.st.ClassesFor(ctx, adminUser(h)); len(classes) != 1 {
		t.Fatal("an empty name must not create a class")
	}
	if code, _, _ := admin.post("/admin/classes/"+id(cls.ID)+"/rollover", url.Values{"name": {"5A"}, "archive_old": {"1"}}); code != 303 {
		t.Fatal(code)
	}
	classes, _ := h.st.ClassesFor(ctx, adminUser(h))
	if len(classes) != 2 || classes[0].Name != "5A" || classes[1].ArchivedAt == nil {
		t.Fatalf("classes %+v", classes)
	}
	students, _ := h.st.ListStudents(ctx, classes[0].ID)
	if len(students) != 1 || students[0].FirstName != "Jan" {
		t.Fatalf("roster of the new year %+v", students)
	}
	_, page, _ := admin.get("/admin/classes")
	if !strings.Contains(page, "Archiwum") || !strings.Contains(page, "Przywróć") {
		t.Fatal("the old class is listed as archived with a restore button")
	}
	// A non-admin cannot do any of this.
	anna := h.client()
	anna.login("anna@example.com", "parentpass1")
	for _, p := range []string{"/archive", "/restore", "/rollover"} {
		if code, _, _ := anna.post("/admin/classes/"+id(cls.ID)+p, url.Values{"name": {"X"}}); code != 403 {
			t.Errorf("parent POST %s: %d, want 403", p, code)
		}
	}
}

func adminUser(h *harness) store.User {
	u, _ := h.st.UserByEmail(context.Background(), "teresa@example.com")
	return u
}
