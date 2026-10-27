package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/classy-cash/app/internal/auth"
	"github.com/classy-cash/app/internal/bankmail"
	"github.com/classy-cash/app/internal/config"
	"github.com/classy-cash/app/internal/db"
	"github.com/classy-cash/app/internal/mail"
	"github.com/classy-cash/app/internal/store"
	"github.com/classy-cash/app/migrations"
)

const (
	bankSecret = "0123456789abcdef0123456789abcdef"
	bankAddr   = "bank-abc123@classycash.dpdns.org"
)

type harness struct {
	t   *testing.T
	st  *store.Store
	srv *Server
	ts  *httptest.Server
	cfg *config.Config
}

func newHarness(t *testing.T, mutate func(*config.Config)) *harness {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, migrations.FS); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CLASSYCASH_ENV": "dev", "CLASSYCASH_BASE_URL": "http://app.test", "CLASSYCASH_SESSION_SECRET": "unit-test-session-secret-unit-test-xx",
		"CLASSYCASH_BANK_MAIL_SECRET": bankSecret, "CLASSYCASH_BANK_MAIL_ADDRESS": bankAddr, "CLASSYCASH_MAIL_DRIVER": "capture"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(cfg)
	}
	st := store.New(conn, cfg.Location)
	st.BaseURL = cfg.BaseURL
	srv, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), mail.CaptureSender{}, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{t: t, st: st, srv: srv, ts: ts, cfg: cfg}
}

type client struct {
	h *harness
	c *http.Client
	t *testing.T
}

func (h *harness) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{h: h, t: h.t, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

var csrfRe = regexp.MustCompile(`<meta name="csrf" content="([^"]+)"`)

func (c *client) get(path string) (int, string, http.Header) {
	c.t.Helper()
	resp, err := c.c.Get(c.h.ts.URL + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (c *client) token() string {
	c.t.Helper()
	_, body, _ := c.get("/login")
	m := csrfRe.FindStringSubmatch(body)
	for _, p := range []string{"/onboarding", "/profile"} {
		if m != nil {
			break
		}
		_, body, _ = c.get(p)
		m = csrfRe.FindStringSubmatch(body)
	}
	if m == nil {
		c.t.Fatal("no csrf token found")
	}
	return m[1]
}

func (c *client) post(path string, form url.Values) (int, string, http.Header) {
	c.t.Helper()
	if form.Get("_csrf") == "" {
		form.Set("_csrf", c.token())
	}
	resp, err := c.c.PostForm(c.h.ts.URL+path, form)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (c *client) login(email, password string) {
	c.t.Helper()
	code, body, _ := c.post("/login", url.Values{"email": {email}, "password": {password}})
	if code != http.StatusSeeOther {
		c.t.Fatalf("login %s: %d %s", email, code, firstText(body))
	}
}

func firstText(b string) string {
	if len(b) > 300 {
		return b[:300]
	}
	return b
}

// onboard claims the instance and returns a logged-in treasurer client.
func (h *harness) onboarded() (*client, store.Class) {
	h.t.Helper()
	c := h.client()
	code, _, _ := c.post("/onboarding", url.Values{"first_name": {"Teresa"}, "last_name": {"Skarbnik"}, "email": {"teresa@example.com"}, "password": {"password123"}, "class_name": {"4A"}, "school_name": {"SP 1"}, "blik_phone": {"+48 111 222 333"}, "account": {"PL 11 2222 3333"}})
	if code != http.StatusSeeOther {
		h.t.Fatalf("onboarding: %d", code)
	}
	classes, _ := h.st.ClassesFor(context.Background(), store.User{IsAdmin: true})
	return c, classes[0]
}

func (h *harness) parent(email, name string, cls store.Class, kidFirst, kidLast string) (store.User, store.Student) {
	h.t.Helper()
	ctx := context.Background()
	hash, _ := auth.HashPassword("parentpass1")
	u, err := h.st.CreateUser(ctx, email, name, hash, false)
	if err != nil {
		h.t.Fatal(err)
	}
	kid, err := h.st.AddStudent(ctx, cls.ID, kidFirst, kidLast, []int64{u.ID})
	if err != nil {
		h.t.Fatal(err)
	}
	return u, kid
}

func TestOnboardingClaimsInstanceOnlyOnce(t *testing.T) {
	h := newHarness(t, nil)
	anon := h.client()
	if code, _, hd := anon.get("/"); code != 303 || !strings.HasSuffix(hd.Get("Location"), "/onboarding") {
		t.Fatalf("unclaimed instance must route to onboarding, got %d %s", code, hd.Get("Location"))
	}
	c, _ := h.onboarded()
	if code, body, _ := c.get("/treasurer/dashboard"); code != 200 || !strings.Contains(body, "Panel główny") {
		t.Fatalf("dashboard: %d", code)
	}
	second := h.client()
	if code, _, _ := second.get("/onboarding"); code != 303 {
		t.Fatalf("second visitor must be redirected, got %d", code)
	}
	if code, _, _ := second.post("/onboarding", url.Values{"first_name": {"E"}, "last_name": {"V"}, "email": {"eve@example.com"}, "password": {"password123"}, "class_name": {"X"}}); code == 303 {
		t.Fatal("a second onboarding must not succeed")
	}
	if n, _ := h.st.CountUsers(context.Background()); n != 1 {
		t.Fatalf("users: %d", n)
	}
}

func TestProductionOnboardingNeedsClaimToken(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Env = "prod"; c.ClaimToken = "s3cret-claim-code" })
	c := h.client()
	if code, body, _ := c.get("/onboarding"); code != 200 || !strings.Contains(body, "Kod aktywacyjny") {
		t.Fatalf("claim field missing: %d", code)
	}
	form := func(claim string) url.Values {
		return url.Values{"claim": {claim}, "first_name": {"T"}, "last_name": {"S"}, "email": {"t@example.com"}, "password": {"password123"}, "class_name": {"4A"}}
	}
	if code, _, _ := c.post("/onboarding", form("wrong")); code != 422 {
		t.Fatalf("wrong claim must be rejected, got %d", code)
	}
	if code, _, _ := c.post("/onboarding", form("s3cret-claim-code")); code != 303 {
		t.Fatalf("right claim must be accepted, got %d", code)
	}
	h2 := newHarness(t, func(c *config.Config) { c.Env = "prod"; c.ClaimToken = "" })
	if code, _, _ := h2.client().get("/onboarding"); code != 503 {
		t.Fatalf("prod without a claim token must refuse to onboard, got %d", code)
	}
}

func TestEveryTreasurerPageRenders(t *testing.T) {
	h := newHarness(t, nil)
	c, cls := h.onboarded()
	ctx := context.Background()
	_, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	if _, err := h.st.CreateContribution(ctx, cls.ID, "Wycieczka", 5000, nil, []int64{kid.ID}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/treasurer", "/treasurer/dashboard", "/treasurer/dashboard/cards", "/treasurer/ledger?page=1", "/treasurer/payments", "/treasurer/contributions",
		"/treasurer/students", "/treasurer/manual-transactions", "/treasurer/settings", "/admin/mail", "/admin/classes", "/parent", "/profile", "/treasurer/users/search?q=ann", "/treasurer/reports", "/treasurer/members",
		"/treasurer/reports/contributions", "/treasurer/reports/outstanding", "/treasurer/reports/cash?from=2026-09-01&to=2026-10-31", "/treasurer/reports/parents", "/treasurer/reports/transfers"} {
		resp, err := c.c.Get(h.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(b)
		if p == "/" {
			if resp.StatusCode != 303 {
				t.Errorf("%s: %d", p, resp.StatusCode)
			}
			continue
		}
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d: %s", p, resp.StatusCode, firstText(body))
		}
		for _, bad := range []string{"<no value>", "%!", "{{", "ZgotmplZ"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: rendering artefact %q", p, bad)
			}
		}
	}
}

func TestSecurityHeadersAndAssets(t *testing.T) {
	h := newHarness(t, nil)
	c := h.client()
	_, _, hd := c.get("/login")
	csp := hd.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") || hd.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("weak headers: %v", hd)
	}
	for _, p := range []string{"/assets/app/app.css", "/assets/app/app.js", "/assets/brand/icon.svg", "/assets/brand/logo.svg", "/assets/fonts/inter-latin.woff2", "/assets/images/favicon.svg", "/site.webmanifest"} {
		if code, _, _ := c.get(p); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}
	_, body, _ := c.get("/login")
	if strings.Contains(body, "fonts.googleapis") || strings.Contains(body, "<script>") || strings.Contains(body, " style=") {
		t.Fatal("pages must not use external fonts, inline scripts or inline styles (CSP)")
	}
}

func TestCSRFAndOriginAreEnforced(t *testing.T) {
	h := newHarness(t, nil)
	h.onboarded()
	c := h.client()
	c.get("/login")
	resp, _ := c.c.PostForm(h.ts.URL+"/login", url.Values{"email": {"teresa@example.com"}, "password": {"password123"}})
	if resp.StatusCode != 403 {
		t.Fatalf("missing token must be 403, got %d", resp.StatusCode)
	}
	resp, _ = c.c.PostForm(h.ts.URL+"/login", url.Values{"email": {"teresa@example.com"}, "password": {"password123"}, "_csrf": {"forged"}})
	if resp.StatusCode != 403 {
		t.Fatalf("forged token must be 403, got %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", h.ts.URL+"/login", strings.NewReader(url.Values{"email": {"x@y.pl"}, "password": {"p"}, "_csrf": {c.token()}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, _ = c.c.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("foreign origin must be 403, got %d", resp.StatusCode)
	}
}

func TestPasswordLoginAndThrottle(t *testing.T) {
	h := newHarness(t, nil)
	h.onboarded()
	c := h.client()
	if code, body, _ := c.post("/login", url.Values{"email": {"teresa@example.com"}, "password": {"wrong"}}); code != 422 || !strings.Contains(body, "Nieprawidłowy adres e-mail lub hasło") {
		t.Fatalf("bad password: %d", code)
	}
	if code, body, _ := c.post("/login", url.Values{"email": {"nobody@example.com"}, "password": {"wrong"}}); code != 422 || !strings.Contains(body, "Nieprawidłowy adres e-mail lub hasło") {
		t.Fatalf("unknown user must look identical: %d", code)
	}
	for i := 0; i < 10; i++ {
		c.post("/login", url.Values{"email": {"teresa@example.com"}, "password": {"wrong"}})
	}
	if _, body, _ := c.post("/login", url.Values{"email": {"teresa@example.com"}, "password": {"password123"}}); !strings.Contains(body, "Zbyt wiele prób") {
		t.Fatal("repeated failures must be throttled, even for the right password")
	}
}

func TestMagicLinkIsTwoStepAndSingleUse(t *testing.T) {
	h := newHarness(t, nil)
	_, cls := h.onboarded()
	h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	c := h.client()
	if code, body, _ := c.post("/login/link", url.Values{"email": {"anna@example.com"}}); code != 200 || !strings.Contains(body, "Sprawdź skrzynkę") {
		t.Fatalf("link request: %d", code)
	}
	if code, _, _ := c.post("/login/link", url.Values{"email": {"ghost@example.com"}}); code != 200 {
		t.Fatal("unknown address must look identical (no enumeration)")
	}
	mails, _ := h.st.RecentMail(context.Background(), 10)
	var link string
	for _, m := range mails {
		if m.ToEmail == "anna@example.com" {
			link = regexp.MustCompile(`http://app\.test(/login/link/\S+)`).FindStringSubmatch(m.Text)[1]
		}
		if m.ToEmail == "ghost@example.com" {
			t.Fatal("no mail may be sent to unknown addresses")
		}
	}
	if link == "" {
		t.Fatal("no login link mailed")
	}
	if code, body, _ := c.get(link); code != 200 || !strings.Contains(body, "Zaloguj mnie") {
		t.Fatalf("GET must only show a confirmation: %d", code)
	}
	if code, _, _ := c.get(link); code != 200 {
		t.Fatal("a GET (mail scanner) must not burn the link")
	}
	code, _, hd := c.post(link, url.Values{})
	if code != 303 || !strings.Contains(strings.Join(hd.Values("Set-Cookie"), ";"), cookieSession) {
		t.Fatalf("confirm must log in: %d", code)
	}
	if code, _, _ := h.client().post(link, url.Values{}); code != http.StatusGone {
		t.Fatalf("link reuse must fail, got %d", code)
	}
	if code, body, _ := c.get("/parent"); code != 200 || !strings.Contains(body, "Konto rodzica") {
		t.Fatalf("parent area after login: %d", code)
	}
}

func TestPasswordResetFlowRevokesSessions(t *testing.T) {
	h := newHarness(t, nil)
	tr, _ := h.onboarded()
	if code, _, _ := tr.get("/treasurer/dashboard"); code != 200 {
		t.Fatal("setup")
	}
	anon := h.client()
	code, _, hd := anon.post("/reset-password", url.Values{"email": {"teresa@example.com"}})
	if code != 303 || !strings.HasSuffix(hd.Get("Location"), "/reset-password/check-email") {
		t.Fatalf("reset request: %d", code)
	}
	mails, _ := h.st.RecentMail(context.Background(), 10)
	var path string
	for _, m := range mails {
		if m.ToEmail == "teresa@example.com" && strings.Contains(m.Subject, "hasło") {
			path = regexp.MustCompile(`/reset-password/reset/\S+`).FindString(m.Text)
		}
	}
	if path == "" {
		t.Fatal("reset mail not queued")
	}
	if code, _, _ := anon.post(path, url.Values{"password": {"short"}, "password_confirm": {"short"}}); code != 422 {
		t.Fatal("short passwords rejected")
	}
	if code, _, _ := anon.post(path, url.Values{"password": {"newpassword1"}, "password_confirm": {"different1"}}); code != 422 {
		t.Fatal("mismatched passwords rejected")
	}
	if code, _, hd := anon.post(path, url.Values{"password": {"newpassword1"}, "password_confirm": {"newpassword1"}}); code != 303 || !strings.Contains(hd.Get("Location"), "/login") {
		t.Fatalf("reset: %d", code)
	}
	if code, _, _ := anon.post(path, url.Values{"password": {"another-one-1"}, "password_confirm": {"another-one-1"}}); code != http.StatusGone {
		t.Fatalf("a reset link is single-use, got %d", code)
	}
	if code, _, _ := tr.get("/treasurer/dashboard"); code != 303 {
		t.Fatalf("existing sessions must be revoked, got %d", code)
	}
	fresh := h.client()
	if code, _, _ := fresh.post("/login", url.Values{"email": {"teresa@example.com"}, "password": {"password123"}}); code == 303 {
		t.Fatal("old password must stop working")
	}
	fresh.login("teresa@example.com", "newpassword1")
}

func TestPermissionsAcrossRoles(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	_, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	other, _ := h.st.CreateClass(context.Background(), "5B")
	h.parent("ola@example.com", "Ola Nowak", other, "Ola", "Nowak")
	parent := h.client()
	parent.login("anna@example.com", "parentpass1")
	for _, p := range []string{"/treasurer/dashboard", "/treasurer/payments", "/treasurer/contributions", "/treasurer/students", "/treasurer/settings", "/admin/mail", "/admin/classes"} {
		if code, _, _ := parent.get(p); code != 403 {
			t.Errorf("parent %s: %d, want 403", p, code)
		}
	}
	if code, _, _ := parent.post("/treasurer/contributions", url.Values{"title": {"x"}, "amount_pln": {"1"}}); code != 403 {
		t.Errorf("parent must not create contributions: %d", code)
	}
	anon := h.client()
	if code, _, hd := anon.get("/treasurer/dashboard"); code != 303 || !strings.HasSuffix(hd.Get("Location"), "/login") {
		t.Errorf("anonymous: %d", code)
	}
	// A treasurer of another class cannot touch this class.
	tr2 := h.client()
	hash, _ := auth.HashPassword("password222")
	u2, _ := h.st.CreateUser(context.Background(), "t2@example.com", "Druga Skarbnik", hash, false)
	_ = h.st.SetMember(context.Background(), other.ID, u2.ID, store.RoleTreasurer)
	tr2.login("t2@example.com", "password222")
	_, body, _ := tr2.get("/treasurer/students")
	if strings.Contains(body, "Kowalski") || !strings.Contains(body, "Nowak") {
		t.Fatal("class isolation broken on students page")
	}
	spID := func() int64 {
		h.st.CreateContribution(context.Background(), cls.ID, "Wycieczka", 5000, nil, []int64{kid.ID})
		all, _ := h.st.StudentPaymentsByClass(context.Background(), cls.ID)
		return all[0].ID
	}()
	if code, _, _ := tr2.post("/treasurer/obligations/"+strconv.FormatInt(spID, 10)+"/paid", url.Values{}); code != 303 {
		t.Fatal("redirect expected")
	}
	if all, _ := h.st.StudentPaymentsByClass(context.Background(), cls.ID); all[0].Status == store.SPPaid {
		t.Fatal("another class's treasurer must not settle this class's obligations")
	}
	if code, _, _ := tr.post("/treasurer/obligations/"+strconv.FormatInt(spID, 10)+"/paid", url.Values{}); code != 303 {
		t.Fatal("own treasurer redirect expected")
	}
	if all, _ := h.st.StudentPaymentsByClass(context.Background(), cls.ID); all[0].Status != store.SPPaid {
		t.Fatal("own treasurer must be able to settle")
	}
}

func TestParentFastPaymentJSON(t *testing.T) {
	h := newHarness(t, nil)
	_, cls := h.onboarded()
	u, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	h.st.CreateContribution(context.Background(), cls.ID, "Wycieczka", 5000, nil, []int64{kid.ID})
	p := h.client()
	p.login("anna@example.com", "parentpass1")
	do := func() map[string]any {
		req, _ := http.NewRequest("POST", h.ts.URL+"/parent/pay", nil)
		req.Header.Set("X-CSRF-Token", p.token())
		resp, err := p.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	r := do()
	if r["ok"] != true || len(r["code"].(string)) != 4 || r["blik_phone"] != "+48 111 222 333" {
		t.Fatalf("pay response: %v", r)
	}
	if again := do(); again["code"] != r["code"] {
		t.Fatal("same code on repeat")
	}
	code, body, _ := p.get("/parent/pay/" + strconv.FormatInt(int64(r["id"].(float64)), 10) + "/status")
	if code != 200 || !strings.Contains(body, `"paid":false`) {
		t.Fatalf("status: %d %s", code, body)
	}
	// Another parent cannot poll someone else's payment.
	h.parent("ola@example.com", "Ola Nowak", cls, "Ola", "Nowak")
	o := h.client()
	o.login("ola@example.com", "parentpass1")
	if code, _, _ := o.get("/parent/pay/" + strconv.FormatInt(int64(r["id"].(float64)), 10) + "/status"); code != 404 {
		t.Fatalf("foreign payment status must be 404, got %d", code)
	}
	_ = u
}

// The full bank loop through the real mux: signed webhook → match → parent sees it paid.
func TestWebhookEndToEndThroughMux(t *testing.T) {
	h := newHarness(t, nil)
	_, cls := h.onboarded()
	_, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	ctx := context.Background()
	h.st.CreateContribution(ctx, cls.ID, "Wycieczka", 5000, nil, []int64{kid.ID})
	anna, _ := h.st.UserByEmail(ctx, "anna@example.com")
	pay, err := h.st.CreateFastPayment(ctx, anna, cls.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	body, _ := json.Marshal(bankmail.Payload{ID: strings.Repeat("ab", 32), ReceivedAt: now.Format(time.RFC3339), Recipient: bankAddr, MailFrom: "powiadomienia@alior.pl",
		Subject: "Uznanie rachunku 91...6978 kwotą 50,00 PLN", Body: "rachunek uznany kwotą 50,00 PLN.<br/>Nadawca: ANNA KOWALSKA<br/>Tytuł zlecenia: wycieczka " + pay.Code + "<br/>Saldo: 1 PLN<br/>"})
	send := func() int {
		ts := now.Format(time.RFC3339)
		req, _ := http.NewRequest("POST", h.ts.URL+"/api/bank-mail", strings.NewReader(string(body)))
		req.Header.Set(bankmail.HeaderTimestamp, ts)
		req.Header.Set(bankmail.HeaderSignature, auth.Sign(bankSecret, ts, body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send(); code != 204 {
		t.Fatalf("webhook: %d", code)
	}
	if code := send(); code != 204 {
		t.Fatalf("redelivery: %d", code)
	}
	p := h.client()
	p.login("anna@example.com", "parentpass1")
	if _, page, _ := p.get("/parent"); !strings.Contains(page, "Wszystkie składki opłacone") {
		t.Fatal("parent dashboard must show everything paid")
	}
	tr := h.client()
	tr.login("teresa@example.com", "password123")
	if _, page, _ := tr.get("/treasurer/payments"); !strings.Contains(page, "Rozpoznano") || strings.Count(page, "ANNA KOWALSKA") != 1 {
		t.Fatalf("payments page: recognised once expected")
	}
	if sum, _ := h.st.Summary(ctx, cls.ID); sum.TotalMinor != 5000 {
		t.Fatalf("cash %d", sum.TotalMinor)
	}
}

func TestHealthReadyAndVersion(t *testing.T) {
	h := newHarness(t, nil)
	c := h.client()
	if code, body, _ := c.get("/health"); code != 200 || strings.TrimSpace(body) != "ok" {
		t.Fatal("health")
	}
	code, body, _ := c.get("/ready")
	if code != 200 || !strings.Contains(body, `"migrations":"current"`) {
		t.Fatalf("ready: %d %s", code, body)
	}
	if code, body, _ := c.get("/version"); code != 200 || !strings.Contains(body, "config_version") {
		t.Fatal("version")
	}
	h.st.DB.Exec(`DELETE FROM schema_migrations`)
	if code, _, _ := c.get("/ready"); code != 503 {
		t.Fatalf("pending migrations must make /ready fail, got %d", code)
	}
}

func TestDemoModeIsolation(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Demo = true
		c.Mail.Driver, c.Mail.RelayURL, c.Mail.RelayKey = "capture", "", ""
		c.Bank.Secret, c.Bank.Address = "", ""
	})
	ctx := context.Background()
	seed := `INSERT INTO users (id,email,name,password_hash,is_admin,created_at,confirmed_at) VALUES (1,'skarbnik@example.test','Demo Skarbnik','',1,'2026-10-27T10:00:00Z','2026-10-27T10:00:00Z');
INSERT INTO classes (id,name,blik_phone,created_at) VALUES (1,'Demo 4A','+48 000 000 000','2026-10-27T10:00:00Z');
INSERT INTO memberships (user_id,class_id,role) VALUES (1,1,'treasurer');
INSERT INTO settings (key,value) VALUES ('demo_accounts','[{"label":"Skarbnik","email":"skarbnik@example.test","role":"skarbnik"}]'),('app_name','Classy Cash Demo');`
	if _, err := h.st.DemoLoad(ctx, seed); err != nil {
		t.Fatal(err)
	}
	c := h.client()
	_, body, _ := c.get("/login")
	if !strings.Contains(body, "Tryb demo") || !strings.Contains(body, "data-countdown") || !strings.Contains(body, "Konta demonstracyjne") {
		t.Fatal("demo banner, countdown and quick login must show")
	}
	if code, _, _ := c.post("/demo/login", url.Values{"email": {"intruder@example.test"}}); code != 403 {
		t.Fatalf("only advertised accounts may quick-login, got %d", code)
	}
	if code, _, _ := c.post("/demo/login", url.Values{"email": {"skarbnik@example.test"}}); code != 303 {
		t.Fatalf("demo quick login: %d", code)
	}
	if code, _, _ := c.get("/treasurer/dashboard"); code != 200 {
		t.Fatalf("demo dashboard: %d", code)
	}
	if code, _, _ := c.post("/profile/password", url.Values{"password": {"hackhack12"}, "password_confirm": {"hackhack12"}}); code != 303 {
		t.Fatal("redirect expected")
	}
	if u, _ := h.st.UserByEmail(ctx, "skarbnik@example.test"); u.PasswordHash != "" {
		t.Fatal("demo credentials must not be changeable")
	}
	if code, _, _ := h.client().get("/onboarding"); code == 200 {
		t.Fatal("onboarding must be closed in demo mode")
	}
	// Real bank webhook is off.
	ts := time.Now().UTC().Format(time.RFC3339)
	req, _ := http.NewRequest("POST", h.ts.URL+"/api/bank-mail", strings.NewReader("{}"))
	req.Header.Set(bankmail.HeaderTimestamp, ts)
	req.Header.Set(bankmail.HeaderSignature, auth.Sign(bankSecret, ts, []byte("{}")))
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 503 {
		t.Fatalf("webhook must be disabled in demo, got %d", resp.StatusCode)
	}
	// Password reset sends nothing in demo; link logins are captured, never delivered.
	c.post("/reset-password", url.Values{"email": {"skarbnik@example.test"}})
	if list, _ := h.st.RecentMail(ctx, 10); len(list) != 0 {
		t.Fatal("password reset must not queue mail in demo")
	}
	if code, body, _ := h.client().get("/ready"); code != 200 || !strings.Contains(body, `"mode":"demo"`) || !strings.Contains(body, `"bank_webhook":"disabled"`) {
		t.Fatalf("ready should report demo: %s", body)
	}
}
