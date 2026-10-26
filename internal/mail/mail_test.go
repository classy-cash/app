package mail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var brand = Branding{AppName: "Classy Cash 4A", BaseURL: "https://x.test"}

func TestTemplatesEscapeUserContent(t *testing.T) {
	r := LoginLink(brand, `<script>alert(1)</script>`, "https://x.test/login/link/abc?x=1&y=2", false)
	if strings.Contains(r.HTML, "<script>") || !strings.Contains(r.HTML, "&lt;script&gt;") {
		t.Fatal("names must be HTML-escaped")
	}
	if !strings.Contains(r.HTML, "Classy Cash 4A") || !strings.Contains(r.Text, "https://x.test/login/link/abc?x=1&y=2") {
		t.Fatal("branding and plain-text link expected")
	}
	if first := LoginLink(brand, "A", "l", true); first.Subject != "Potwierdzenie rejestracji" || r.Subject != "Zaloguj się do swojego konta" {
		t.Fatal("subjects")
	}
}

func TestSettlementAndNoticeContent(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Warsaw")
	paid := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	s := SettlementConfirmation(brand, SettlementData{Items: []SettlementItem{{Student: "Jan K", Label: "Wycieczka", AmountMinor: 5000, Paid: 3, Total: 10}},
		Previous: []PreviousPayment{{Date: paid, AmountMinor: 10000}}, SumPrevious: 10000, SumIncludingThis: 15000}, loc)
	for _, want := range []string{"Jan K", "Wycieczka", "50 zł", "3/10", "01.10.2026", "150 zł"} {
		if !strings.Contains(s.HTML, want) || !strings.Contains(s.Text, want) {
			t.Errorf("settlement missing %q", want)
		}
	}
	n := TransferNotMatched(brand, TransferNotMatchedData{AmountMinor: 12345, Sender: "ANNA", Title: "x", Date: paid, ReviewURL: "https://x.test/p"}, loc)
	if !strings.Contains(n.HTML, "123,45") || !strings.Contains(n.Subject, "dopasowania") {
		t.Fatal("unmatched notice")
	}
	p := PartialPayment(brand, "Anna", "Wycieczka", 10000, 4000)
	if !strings.Contains(p.Text, "Brakuje jeszcze 60") {
		t.Fatalf("underpayment wording: %s", p.Text)
	}
	if o := PartialPayment(brand, "Anna", "W", 10000, 12000); !strings.Contains(o.Text, "więcej niż należność") {
		t.Fatal("overpayment wording")
	}
}

func TestRelaySenderContract(t *testing.T) {
	var got *http.Request
	var body []byte
	status := 200
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		w.WriteHeader(status)
	}))
	defer ts.Close()
	r := NewRelaySender(ts.URL, "secret-key", "Classy Cash — 4A")
	m := Message{Key: "k-1", ToEmail: "a@example.com", ToName: "Anna", ReplyTo: "t@example.com", Subject: "S", HTML: "<p>h</p>", Text: "t"}
	if err := r.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("Authorization") != "Bearer secret-key" || got.Header.Get("Idempotency-Key") != "k-1" || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers: %v", got.Header)
	}
	for _, want := range []string{`"idempotency_key":"k-1"`, `"from_name":"Classy Cash — 4A"`, `"to":"a@example.com"`, `"reply_to":"t@example.com"`, `"subject":"S"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
	if strings.Contains(string(body), `"from"`) || strings.Contains(string(body), `"from_address"`) {
		t.Fatal("the app must never choose the visible From address")
	}
	cases := map[int]string{500: "retry", 429: "retry", 401: "retry", 400: "permanent", 422: "permanent"}
	for code, kind := range cases {
		status = code
		err := r.Send(context.Background(), m)
		var perm *PermanentError
		if err == nil || errors.As(err, &perm) != (kind == "permanent") {
			t.Errorf("status %d: err=%v want %s", code, err, kind)
		}
	}
	if r.Captures() || !(CaptureSender{}).Captures() {
		t.Fatal("Captures()")
	}
}

func TestRelayDoesNotFollowRedirects(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect was followed") }))
	defer evil.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, evil.URL, http.StatusFound) }))
	defer ts.Close()
	err := NewRelaySender(ts.URL, "k", "n").Send(context.Background(), Message{Key: "k", ToEmail: "a@b.pl", Subject: "s"})
	if err == nil {
		t.Fatal("a redirect is not a success")
	}
}
