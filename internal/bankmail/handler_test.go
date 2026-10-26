package bankmail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/classy-cash/app/internal/auth"
	"github.com/classy-cash/app/internal/config"
	"github.com/classy-cash/app/internal/db"
	"github.com/classy-cash/app/internal/store"
	"github.com/classy-cash/app/migrations"
)

const (
	secret  = "0123456789abcdef0123456789abcdef"
	address = "bank-3f9a6c1e0d@classycash.dpdns.org"
)

type env struct {
	t   *testing.T
	st  *store.Store
	h   *Handler
	now time.Time
	cls store.Class
	par store.User
	kid store.Student
}

func newEnv(t *testing.T) *env {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, migrations.FS); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Warsaw")
	e := &env{t: t, now: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)}
	e.st = store.New(conn, loc)
	e.st.Now = func() time.Time { return e.now }
	ctx := context.Background()
	_, e.cls, err = e.st.Onboard(ctx, store.OnboardInput{FirstName: "T", LastName: "S", Email: "t@example.com", Password: "password123", ClassName: "4A"})
	if err != nil {
		t.Fatal(err)
	}
	e.par, _ = e.st.CreateUser(ctx, "p@example.com", "Anna Kowalska", "", false)
	e.kid, _ = e.st.AddStudent(ctx, e.cls.ID, "Jan", "Kowalski", []int64{e.par.ID})
	e.h = &Handler{Cfg: config.BankConfig{Address: address, Secret: secret, MaxBody: 128 << 10}, Store: e.st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.now }}
	return e
}

func eventID(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func (e *env) payload(id, subject, body string) []byte {
	b, _ := json.Marshal(Payload{ID: id, ReceivedAt: e.now.Format("2006-01-02T15:04:05.000Z"), Recipient: address, MailFrom: "powiadomienia@alior.pl", Subject: subject, Body: body})
	return b
}

func (e *env) post(body []byte, mutate func(r *http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/bank-mail", bytes.NewReader(body))
	ts := e.now.Format("2006-01-02T15:04:05.000Z")
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, auth.Sign(secret, ts, body))
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

const aliorBody = `<html><br/>rachunek 91...6978 został uznany kwotą 100,00 PLN.<br/>Nadawca: ANNA KOWALSKA<br/>Tytuł zlecenia: %s<br/>Saldo: 1,00 PLN<br/></html>`

func (e *env) pendingPayment(minor int64) store.Payment {
	ctx := context.Background()
	if _, err := e.st.CreateContribution(ctx, e.cls.ID, "Wycieczka", minor, nil, []int64{e.kid.ID}); err != nil {
		e.t.Fatal(err)
	}
	p, err := e.st.CreateFastPayment(ctx, e.par, e.cls.ID, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func TestAcceptsSignedAliorNotificationAndSettles(t *testing.T) {
	e := newEnv(t)
	p := e.pendingPayment(10000)
	body := e.payload(eventID("1"), "Uznanie rachunku 91...6978 kwotą 100,00 PLN", strings.Replace(aliorBody, "%s", p.Code, 1))
	if rec := e.post(body, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got, _ := e.st.PaymentByID(context.Background(), e.cls.ID, p.ID)
	if got.Status != store.PaymentPaid || got.ReceivedMinor != 10000 {
		t.Fatalf("not settled: %+v", got)
	}
	if last := e.st.LastBankEvent(context.Background()); last == nil {
		t.Fatal("last delivery time must be recorded")
	}
}

func TestRedeliveryIsAcknowledgedWithoutSecondEffect(t *testing.T) {
	e := newEnv(t)
	p := e.pendingPayment(10000)
	body := e.payload(eventID("2"), "Uznanie rachunku 91...6978 kwotą 100,00 PLN", strings.Replace(aliorBody, "%s", p.Code, 1))
	for i := 0; i < 3; i++ {
		if rec := e.post(body, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("delivery %d: %d", i, rec.Code)
		}
	}
	ts, _ := e.st.Transfers(context.Background(), e.cls.ID)
	sum, _ := e.st.Summary(context.Background(), e.cls.ID)
	if len(ts) != 1 || sum.TotalMinor != 10000 {
		t.Fatalf("%d transfers, cash %d", len(ts), sum.TotalMinor)
	}
}

func TestRejectsBadSignaturesAndStaleTimestamps(t *testing.T) {
	e := newEnv(t)
	body := e.payload(eventID("3"), "Uznanie rachunku 91...6978 kwotą 100,00 PLN", "x")
	cases := map[string]func(r *http.Request){
		"missing signature": func(r *http.Request) { r.Header.Del(HeaderSignature) },
		"wrong signature":   func(r *http.Request) { r.Header.Set(HeaderSignature, strings.Repeat("0", 64)) },
		"non-hex signature": func(r *http.Request) { r.Header.Set(HeaderSignature, "zz") },
		"bad timestamp":     func(r *http.Request) { r.Header.Set(HeaderTimestamp, "yesterday") },
		"stale timestamp": func(r *http.Request) {
			old := e.now.Add(-6 * time.Minute).Format(time.RFC3339)
			r.Header.Set(HeaderTimestamp, old)
			r.Header.Set(HeaderSignature, auth.Sign(secret, old, body))
		},
		"future timestamp": func(r *http.Request) {
			f := e.now.Add(6 * time.Minute).Format(time.RFC3339)
			r.Header.Set(HeaderTimestamp, f)
			r.Header.Set(HeaderSignature, auth.Sign(secret, f, body))
		},
		"signed with another secret": func(r *http.Request) {
			r.Header.Set(HeaderSignature, auth.Sign("another-secret-another-secret-0000", r.Header.Get(HeaderTimestamp), body))
		},
	}
	for name, mut := range cases {
		if rec := e.post(body, mut); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
	}
	// A tampered body fails even with a once-valid signature.
	tampered := bytes.Replace(body, []byte("100,00"), []byte("999,00"), 1)
	if rec := e.post(tampered, func(r *http.Request) {
		r.Header.Set(HeaderSignature, auth.Sign(secret, r.Header.Get(HeaderTimestamp), body))
	}); rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered body: %d", rec.Code)
	}
	if evs, _ := e.st.BankEvents(context.Background(), "", 10); len(evs) != 0 {
		t.Fatal("rejected requests must not be stored")
	}
}

func TestPayloadValidation(t *testing.T) {
	e := newEnv(t)
	mk := func(mut func(*Payload)) []byte {
		p := Payload{ID: eventID("v"), ReceivedAt: e.now.Format(time.RFC3339), Recipient: address, MailFrom: "powiadomienia@alior.pl", Subject: "s", Body: "b"}
		mut(&p)
		b, _ := json.Marshal(p)
		return b
	}
	bad := map[string][]byte{
		"short id":        mk(func(p *Payload) { p.ID = "abc" }),
		"non-hex id":      mk(func(p *Payload) { p.ID = strings.Repeat("g", 64) }),
		"wrong recipient": mk(func(p *Payload) { p.Recipient = "someone@classycash.dpdns.org" }),
		"no sender":       mk(func(p *Payload) { p.MailFrom = "" }),
		"long subject":    mk(func(p *Payload) { p.Subject = strings.Repeat("x", 2001) }),
		"long body":       mk(func(p *Payload) { p.Body = strings.Repeat("x", 100001) }),
		"bad received_at": mk(func(p *Payload) { p.ReceivedAt = "nope" }),
		"not json":        []byte("{"),
	}
	for name, body := range bad {
		if rec := e.post(body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
	upper := mk(func(p *Payload) { p.Recipient = strings.ToUpper(address) })
	if rec := e.post(upper, nil); rec.Code != http.StatusNoContent {
		t.Errorf("recipient comparison must be case-insensitive: %d", rec.Code)
	}
}

func TestOversizedRequestAndMethod(t *testing.T) {
	e := newEnv(t)
	if rec := e.post(bytes.Repeat([]byte("a"), 128<<10+1), nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/bank-mail", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}
}

func TestUnconfiguredAndDemoAreDisabled(t *testing.T) {
	e := newEnv(t)
	e.h.Cfg.Secret = ""
	if rec := e.post([]byte("{}"), nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no secret: %d", rec.Code)
	}
	e.h.Cfg.Secret = "short"
	if rec := e.post([]byte("{}"), nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("short secret: %d", rec.Code)
	}
	cfg, err := config.Load(func(k string) string {
		return map[string]string{"CLASSYCASH_DEMO_MODE": "1", "CLASSYCASH_ENV": "dev", "CLASSYCASH_BANK_MAIL_SECRET": secret, "CLASSYCASH_BANK_MAIL_ADDRESS": address, "CLASSYCASH_MAIL_RELAY_URL": "https://relay.example", "CLASSYCASH_MAIL_RELAY_KEY": "k"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bank.Enabled() || cfg.Mail.Driver != "capture" || cfg.Mail.RelayURL != "" {
		t.Fatalf("demo mode must force-disable the bank webhook and real mail: %+v %+v", cfg.Bank, cfg.Mail)
	}
}

func TestUnrecognizedMailIsQuarantinedButAcknowledged(t *testing.T) {
	e := newEnv(t)
	body := e.payload(eventID("u"), "Wyciąg miesięczny", "<p>nic</p>")
	if rec := e.post(body, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	evs, _ := e.st.BankEvents(context.Background(), "unrecognized", 10)
	if len(evs) != 1 {
		t.Fatal("unrecognised mail must be reviewable")
	}
	if ts, _ := e.st.Transfers(context.Background(), e.cls.ID); len(ts) != 0 {
		t.Fatal("unrecognised mail must not create a transfer")
	}
}

func TestUnexpectedSenderIsQuarantinedWhenConfigured(t *testing.T) {
	e := newEnv(t)
	e.h.Cfg.FromAddress = "powiadomienia@alior.pl"
	p := e.pendingPayment(10000)
	b, _ := json.Marshal(Payload{ID: eventID("s"), ReceivedAt: e.now.Format(time.RFC3339), Recipient: address, MailFrom: "evil@example.com", Subject: "Uznanie rachunku 91...6978 kwotą 100,00 PLN", Body: strings.Replace(aliorBody, "%s", p.Code, 1)})
	if rec := e.post(b, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	got, _ := e.st.PaymentByID(context.Background(), e.cls.ID, p.ID)
	if got.Status == store.PaymentPaid {
		t.Fatal("a spoofed sender must never settle a payment")
	}
	if evs, _ := e.st.BankEvents(context.Background(), "rejected_sender", 10); len(evs) != 1 {
		t.Fatal("spoofed delivery must be kept for review")
	}
}

func TestHeadersMatchCeramizaContract(t *testing.T) {
	if HeaderTimestamp != "X-Ceramiza-Bank-Timestamp" || HeaderSignature != "X-Ceramiza-Bank-Signature" || ReplayWindow != 5*time.Minute {
		t.Fatal("wire contract drifted from Ceramiza")
	}
}
