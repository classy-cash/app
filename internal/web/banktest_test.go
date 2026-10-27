package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/classy-cash/app/internal/auth"
	"github.com/classy-cash/app/internal/bankmail"
	"github.com/classy-cash/app/internal/config"
	"github.com/classy-cash/app/internal/store"
)

// webhook sends one signed notification through the real mux, as the Cloudflare Worker would.
func (h *harness) webhook(id, title, amount string) int {
	h.t.Helper()
	now := time.Now().UTC()
	body, _ := json.Marshal(bankmail.Payload{ID: id, ReceivedAt: now.Format(time.RFC3339), Recipient: bankAddr, MailFrom: "powiadomienia@alior.pl",
		Subject: "Uznanie rachunku 91...6978 kwotą " + amount + " PLN", Body: "rachunek uznany kwotą " + amount + " PLN.<br/>Nadawca: JAN KOWALSKI<br/>Tytuł zlecenia: " + title + "<br/>Saldo: 1 PLN<br/>"})
	ts := now.Format(time.RFC3339)
	req, _ := http.NewRequest("POST", h.ts.URL+"/api/bank-mail", strings.NewReader(string(body)))
	req.Header.Set(bankmail.HeaderTimestamp, ts)
	req.Header.Set(bankmail.HeaderSignature, auth.Sign(bankSecret, ts, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func hexID(c byte) string { return strings.Repeat(string(c), 64) }

func TestBankTestEndToEndThroughSettingsAndWebhook(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	anna, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	h.st.CreateContribution(ctx, cls.ID, "Wycieczka", 100, nil, []int64{kid.ID})
	pay, err := h.st.CreateFastPayment(ctx, anna, cls.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, page, _ := tr.get("/treasurer/settings")
	for _, want := range []string{"Test połączenia z bankiem", "nie jest płatnością", "Przelew wykonaj sam", "Utwórz test"} {
		if !strings.Contains(page, want) {
			t.Fatalf("settings page lacks %q", want)
		}
	}
	if !strings.Contains(page, "PL 11 2222 3333") {
		t.Fatal("the payee account must be shown so the user knows where to send the transfer")
	}

	if code, _, hd := tr.post("/treasurer/settings/bank-tests", url.Values{"amount_pln": {"1,00"}}); code != 303 || !strings.Contains(hd.Get("Location"), "/treasurer/settings") {
		t.Fatalf("create: %d", code)
	}
	tests, _ := h.st.BankTests(ctx, cls.ID, 10)
	if len(tests) != 1 {
		t.Fatalf("%d tests", len(tests))
	}
	bt := tests[0]
	_, page, _ = tr.get("/treasurer/settings")
	if !strings.Contains(page, bt.Title()) || !strings.Contains(page, "Oczekuje") || strings.Contains(page, "✓ Sukces") {
		t.Fatal("a fresh test shows its code and 'Oczekuje'")
	}

	// The bank notification arrives; the title carries a LIVE payment code too.
	if code := h.webhook(hexID('a'), pay.Code+" "+bt.Title(), "1,00"); code != 204 {
		t.Fatalf("webhook: %d", code)
	}
	if code := h.webhook(hexID('a'), pay.Code+" "+bt.Title(), "1,00"); code != 204 { // redelivery
		t.Fatalf("redelivery: %d", code)
	}
	if code := h.webhook(hexID('b'), pay.Code+" "+bt.Title(), "1,00"); code != 204 { // retry under a new id
		t.Fatalf("retry: %d", code)
	}
	_, page, _ = tr.get("/treasurer/settings")
	if !strings.Contains(page, "✓ Sukces") || !strings.Contains(page, "Odebrano 2 powiadomień") {
		t.Fatalf("status must flip to success and report the repeat")
	}
	if got, _ := h.st.PaymentByID(ctx, cls.ID, pay.ID); got.Status != store.PaymentPending {
		t.Fatalf("a diagnostic settled a real payment: %s", got.Status)
	}
	if sum, _ := h.st.Summary(ctx, cls.ID); sum.TotalMinor != 0 {
		t.Fatalf("a diagnostic moved the till: %d", sum.TotalMinor)
	}
	if ts, _ := h.st.Transfers(ctx, cls.ID); len(ts) != 0 {
		t.Fatalf("a diagnostic became a transfer: %d", len(ts))
	}
	if _, pg, _ := tr.get("/treasurer/payments"); strings.Contains(pg, "JAN KOWALSKI") {
		t.Fatal("the diagnostic must not show up among the bank operations")
	}

	// A normal payment right afterwards still behaves normally.
	if code := h.webhook(hexID('c'), pay.Code, "1,00"); code != 204 {
		t.Fatal(code)
	}
	if got, _ := h.st.PaymentByID(ctx, cls.ID, pay.ID); got.Status != store.PaymentPaid {
		t.Fatalf("normal payment no longer settles: %s", got.Status)
	}
	// And an unknown title is an ordinary unmatched transfer.
	if code := h.webhook(hexID('d'), "nikt nie wie", "7,00"); code != 204 {
		t.Fatal(code)
	}
	if ts, _ := h.st.Transfers(ctx, cls.ID); len(ts) != 2 {
		t.Fatalf("expected the two real transfers, got %d", len(ts))
	}
}

func TestBankTestPermissionsAndCSRF(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	president, _ := h.st.CreateUser(ctx, "przew@example.com", "Przewodnicząca", mustHash(t, "councilpass1"), false)
	if err := h.st.SetMember(ctx, cls.ID, president.ID, store.RolePresident); err != nil {
		t.Fatal(err)
	}
	other, _ := h.st.CreateClass(ctx, "5B")
	t2, _ := h.st.CreateUser(ctx, "t2@example.com", "Druga Skarbnik", mustHash(t, "password222"), false)
	if err := h.st.SetMember(ctx, other.ID, t2.ID, store.RoleTreasurer); err != nil {
		t.Fatal(err)
	}
	bt, err := h.st.CreateBankTest(ctx, cls.ID, h.mustUser("teresa@example.com"), 100)
	if err != nil {
		t.Fatal(err)
	}
	cancel := "/treasurer/settings/bank-tests/" + id(bt.ID) + "/cancel"

	for name, who := range map[string][2]string{"parent": {"anna@example.com", "parentpass1"}, "council": {"przew@example.com", "councilpass1"}} {
		c := h.client()
		c.login(who[0], who[1])
		if code, _, _ := c.post("/treasurer/settings/bank-tests", url.Values{"amount_pln": {"1,00"}}); code != 403 {
			t.Errorf("%s create: %d", name, code)
		}
		if code, _, _ := c.post(cancel, url.Values{}); code != 403 {
			t.Errorf("%s cancel: %d", name, code)
		}
	}
	anon := h.client()
	if code, _, hd := anon.post("/treasurer/settings/bank-tests", url.Values{"amount_pln": {"1,00"}}); code != 303 && code != 403 {
		t.Errorf("anonymous create: %d %v", code, hd)
	}
	// Another class's treasurer can neither cancel nor see this class's test.
	c2 := h.client()
	c2.login("t2@example.com", "password222")
	c2.post(cancel, url.Values{})
	if got, _ := h.st.BankTestByID(ctx, cls.ID, bt.ID); got.Stored != "pending" {
		t.Fatal("a foreign treasurer cancelled the test")
	}
	if _, pg, _ := c2.get("/treasurer/settings"); strings.Contains(pg, bt.Title()) {
		t.Fatal("a foreign class's code leaked")
	}
	if list, _ := h.st.BankTests(ctx, cls.ID, 10); len(list) != 1 {
		t.Fatalf("unauthorised callers created tests: %d", len(list))
	}

	// CSRF: the owner without a valid token is refused and nothing changes.
	resp, err := tr.c.PostForm(h.ts.URL+"/treasurer/settings/bank-tests", url.Values{"amount_pln": {"1,00"}})
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("missing token: %v %d", err, resp.StatusCode)
	}
	resp, _ = tr.c.PostForm(h.ts.URL+cancel, url.Values{"_csrf": {"forged"}})
	if resp.StatusCode != 403 {
		t.Fatalf("forged token on cancel: %d", resp.StatusCode)
	}
	if got, _ := h.st.BankTestByID(ctx, cls.ID, bt.ID); got.Stored != "pending" {
		t.Fatal("cancelled without a CSRF token")
	}
	// GET never mutates.
	if code, _, _ := tr.get(cancel); code != 405 && code != 404 {
		t.Fatalf("GET on the cancel route: %d", code)
	}

	// The owner can cancel; a swallowed late delivery then leaves the state alone.
	if code, _, _ := tr.post(cancel, url.Values{}); code != 303 {
		t.Fatal(code)
	}
	if got, _ := h.st.BankTestByID(ctx, cls.ID, bt.ID); got.Stored != "cancelled" {
		t.Fatal("owner cancel failed")
	}
	if code := h.webhook(hexID('e'), bt.Title(), "1,00"); code != 204 {
		t.Fatal(code)
	}
	if ts, _ := h.st.Transfers(ctx, cls.ID); len(ts) != 0 {
		t.Fatal("a cancelled test's notification became a transfer")
	}
	_, pg, _ := tr.get("/treasurer/settings")
	if !strings.Contains(pg, "Anulowany") || !strings.Contains(pg, "nie liczy się jako sukces") {
		t.Fatal("cancelled-then-arrived must be explained")
	}
}

func (h *harness) mustUser(email string) store.User {
	h.t.Helper()
	u, err := h.st.UserByEmail(context.Background(), email)
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	hsh, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	return hsh
}

func TestBankTestInputLimits(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	for _, bad := range []string{"0", "-1", "abc", "10,01", "500"} {
		tr.post("/treasurer/settings/bank-tests", url.Values{"amount_pln": {bad}})
	}
	if list, _ := h.st.BankTests(ctx, cls.ID, 10); len(list) != 0 {
		t.Fatalf("invalid amounts created %d tests", len(list))
	}
	tr.post("/treasurer/settings/bank-tests", url.Values{}) // empty falls back to the 1.00 default
	list, _ := h.st.BankTests(ctx, cls.ID, 10)
	if len(list) != 1 || list[0].ExpectedMinor != 100 {
		t.Fatalf("default amount: %+v", list)
	}
	for i := 0; i < 5; i++ {
		tr.post("/treasurer/settings/bank-tests", url.Values{"amount_pln": {"2,00"}})
	}
	if list, _ := h.st.BankTests(ctx, cls.ID, 10); len(list) != store.MaxOpenBankTests {
		t.Fatalf("open tests must be capped at %d, got %d", store.MaxOpenBankTests, len(list))
	}
}

func TestBankTestUnavailableInDemoAndWithoutBank(t *testing.T) {
	t.Run("demo", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) {
			c.Demo = true
			c.Mail.Driver, c.Mail.RelayURL, c.Mail.RelayKey = "capture", "", ""
			c.Bank.Secret, c.Bank.Address = "", ""
		})
		seed := `INSERT INTO users (id,email,name,password_hash,is_admin,created_at,confirmed_at) VALUES (1,'skarbnik@example.test','Demo Skarbnik','',1,'2026-10-27T10:00:00Z','2026-10-27T10:00:00Z');
INSERT INTO classes (id,name,created_at) VALUES (1,'Demo 4A','2026-10-27T10:00:00Z');
INSERT INTO memberships (user_id,class_id,role) VALUES (1,1,'treasurer');
INSERT INTO settings (key,value) VALUES ('demo_accounts','[{"label":"Skarbnik","email":"skarbnik@example.test","role":"skarbnik"}]');`
		if _, err := h.st.DemoLoad(context.Background(), seed); err != nil {
			t.Fatal(err)
		}
		c := h.client()
		if code, _, _ := c.post("/demo/login", url.Values{"email": {"skarbnik@example.test"}}); code != 303 {
			t.Fatal(code)
		}
		_, page, _ := c.get("/treasurer/settings")
		if !strings.Contains(page, "prawdziwego webhooka nie da się tu sprawdzić") || strings.Contains(page, "Utwórz test") {
			t.Fatal("demo must explain that the real webhook cannot be tested and offer no form")
		}
		c.post("/treasurer/settings/bank-tests", url.Values{"amount_pln": {"1,00"}})
		if list, _ := h.st.BankTests(context.Background(), 1, 10); len(list) != 0 {
			t.Fatal("demo created a test reference")
		}
	})
	t.Run("bank not configured", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.Bank.Secret, c.Bank.Address = "", "" })
		tr, cls := h.onboarded()
		_, page, _ := tr.get("/treasurer/settings")
		if !strings.Contains(page, "nie jest skonfigurowany") || strings.Contains(page, "Utwórz test") {
			t.Fatal("an unconfigured bank webhook must be explained")
		}
		tr.post("/treasurer/settings/bank-tests", url.Values{"amount_pln": {"1,00"}})
		if list, _ := h.st.BankTests(context.Background(), cls.ID, 10); len(list) != 0 {
			t.Fatal("test created without a webhook")
		}
	})
}
