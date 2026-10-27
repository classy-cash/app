package web

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/classy-cash/app/internal/store"
)

func TestProfileExportIsPrivateAndComplete(t *testing.T) {
	h := newHarness(t, nil)
	_, cls := h.onboarded()
	h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	h.parent("ewa@example.com", "Ewa Nowak", cls, "Ola", "Nowak")

	if code, _, _ := h.client().get("/profile/export"); code != 303 {
		t.Fatalf("anonymous visitors must be sent to the login page, got %d", code)
	}
	anna := h.client()
	anna.login("anna@example.com", "parentpass1")
	code, body, hd := anna.get("/profile/export")
	if code != 200 || !strings.HasPrefix(hd.Get("Content-Type"), "application/json") || !strings.Contains(hd.Get("Content-Disposition"), "attachment") || hd.Get("Cache-Control") != "no-store" {
		t.Fatalf("export headers: %d %v", code, hd)
	}
	var ex store.UserExport
	if err := json.Unmarshal([]byte(body), &ex); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	if ex.Profile.Email != "anna@example.com" || len(ex.Children) != 1 || ex.Children[0].FirstName != "Jan" {
		t.Fatalf("export %+v", ex)
	}
	if strings.Contains(body, "Nowak") || strings.Contains(body, "ewa@example.com") || strings.Contains(body, "password_hash") {
		t.Fatal("the export must contain only the caller's own data and no secrets")
	}
	if _, page, _ := anna.get("/profile"); !strings.Contains(page, "Pobierz moje dane") || !strings.Contains(page, "Usuń moje konto") {
		t.Fatal("profile must offer export and erasure")
	}
}

func TestParentCanEraseTheirOwnAccount(t *testing.T) {
	h := newHarness(t, nil)
	_, cls := h.onboarded()
	ctx := context.Background()
	h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	anna := h.client()
	anna.login("anna@example.com", "parentpass1")

	for name, f := range map[string]url.Values{
		"missing confirmation": {"current": {"parentpass1"}},
		"wrong word":           {"confirm": {"usun"}, "current": {"parentpass1"}},
		"wrong password":       {"confirm": {"USUŃ"}, "current": {"nope"}},
	} {
		if code, _, _ := anna.post("/profile/delete", f); code != 303 {
			t.Fatalf("%s: %d", name, code)
		}
		if _, err := h.st.UserByEmail(ctx, "anna@example.com"); err != nil {
			t.Fatalf("%s must not erase the account", name)
		}
	}
	code, _, hd := anna.post("/profile/delete", url.Values{"confirm": {"USUŃ"}, "current": {"parentpass1"}})
	if code != 303 || !strings.HasSuffix(hd.Get("Location"), "/login") {
		t.Fatalf("erase: %d %s", code, hd.Get("Location"))
	}
	if _, err := h.st.UserByEmail(ctx, "anna@example.com"); err == nil {
		t.Fatal("the address must be gone")
	}
	if code, _, _ := anna.get("/parent"); code != 303 {
		t.Fatalf("the old session must be dead, got %d", code)
	}
	if code, body, _ := h.client().post("/login", url.Values{"email": {"anna@example.com"}, "password": {"parentpass1"}}); code != 422 || !strings.Contains(body, "Nieprawidłowy") {
		t.Fatalf("the erased account must not log in: %d", code)
	}
	// A sign-in link can no longer be requested for the tombstone address either.
	u, _ := h.st.UserByID(ctx, 2)
	before, _ := h.st.RecentMail(ctx, 50)
	h.client().post("/login/link", url.Values{"email": {u.Email}})
	after, _ := h.st.RecentMail(ctx, 50)
	if len(after) != len(before) {
		t.Fatal("no mail may be queued for an erased account")
	}
}

func TestTreasurerAndDemoCannotEraseThroughTheForm(t *testing.T) {
	h := newHarness(t, nil)
	tr, _ := h.onboarded()
	tr.post("/profile/delete", url.Values{"confirm": {"USUŃ"}, "current": {"password123"}})
	u, err := h.st.UserByEmail(context.Background(), "teresa@example.com")
	if err != nil || store.IsErased(u) {
		t.Fatal("a treasurer must hand the class over before erasing the account")
	}
	if _, body, _ := tr.get("/parent"); body == "" {
		t.Fatal("the treasurer stays signed in after the refusal")
	}
}
