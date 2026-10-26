// Package bankmail implements POST /api/bank-mail: the signed webhook the Cloudflare Email Worker
// calls with each bank notification. The wire contract is identical to Ceramiza's:
//
//	headers  X-Ceramiza-Bank-Timestamp  RFC3339
//	         X-Ceramiza-Bank-Signature  hex(HMAC-SHA256(secret, timestamp + "\n" + exact body))
//	body     {"id","received_at","recipient","mail_from","subject","body"}
//
// Success is always 204 No Content — including redelivery of a known id and notifications that were
// quarantined as unrecognised — so the queue stops retrying once the event is durable.
package bankmail

import (
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/alior"
	"github.com/classy-cash/app/internal/auth"
	"github.com/classy-cash/app/internal/config"
	"github.com/classy-cash/app/internal/store"
)

// Header names (kept Ceramiza-compatible so the Worker is shared).
const (
	HeaderTimestamp = "X-Ceramiza-Bank-Timestamp"
	HeaderSignature = "X-Ceramiza-Bank-Signature"
	ReplayWindow    = 5 * time.Minute
)

// Payload is the JSON body sent by the Worker.
type Payload struct {
	ID         string `json:"id"`
	ReceivedAt string `json:"received_at"`
	Recipient  string `json:"recipient"`
	MailFrom   string `json:"mail_from"`
	Subject    string `json:"subject"`
	Body       string `json:"body"`
}

// Handler serves the webhook.
type Handler struct {
	Cfg   config.BankConfig
	Store *store.Store
	Log   *slog.Logger
	Now   func() time.Time
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.Cfg.Enabled() || h.Cfg.Address == "" {
		http.Error(w, "bank mail is not configured", http.StatusServiceUnavailable)
		return
	}
	limit := h.Cfg.MaxBody
	if limit <= 0 {
		limit = 128 << 10
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	now := h.now()
	ts := r.Header.Get(HeaderTimestamp)
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil || now.Sub(t) > ReplayWindow || t.Sub(now) > ReplayWindow {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	provided, err := hex.DecodeString(r.Header.Get(HeaderSignature))
	want, _ := hex.DecodeString(auth.Sign(h.Cfg.Secret, ts, body))
	if err != nil || !hmac.Equal(provided, want) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if len(p.ID) != 64 || !isHex(p.ID) || !strings.EqualFold(p.Recipient, h.Cfg.Address) ||
		len(p.Subject) > 2000 || len(p.Body) > 100000 || p.MailFrom == "" {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	received, err := time.Parse(time.RFC3339, p.ReceivedAt)
	if err != nil {
		http.Error(w, "invalid timestamp", http.StatusBadRequest)
		return
	}
	ev := store.BankEventIn{ID: strings.ToLower(p.ID), ReceivedAt: received, Recipient: strings.ToLower(p.Recipient), MailFrom: strings.ToLower(p.MailFrom), Subject: p.Subject, Body: p.Body}
	if h.Cfg.FromAddress != "" && ev.MailFrom != h.Cfg.FromAddress {
		if err := h.Store.RecordRejectedBankEvent(r.Context(), ev, "Nadawca inny niż oczekiwany ("+h.Cfg.FromAddress+")"); err != nil {
			h.Log.Error("bank mail: record rejected", "err", err)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		h.Log.Warn("bank mail: unexpected sender quarantined", "id", ev.ID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var parsed *alior.Notification
	if alior.HasCreditSubject(ev.Subject) {
		if n, ok := alior.Parse(ev.Subject, ev.Body); ok {
			parsed = &n
		}
	}
	res, err := h.Store.IngestBankMail(r.Context(), ev, parsed)
	if err != nil {
		h.Log.Error("bank mail: save", "err", err) // never log the body: it holds financial data
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if res.Outcome == store.OutcomeUnrecognized {
		h.Log.Warn("bank mail: unrecognized", "id", ev.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}
