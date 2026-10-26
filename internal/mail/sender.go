package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Sender delivers one message. Implementations must be safe for concurrent use.
type Sender interface {
	// Send returns nil when the message is accepted. A *PermanentError must not be retried.
	Send(ctx context.Context, m Message) error
	// Captures reports whether messages stay local (log/capture drivers): they are stored in the
	// outbox for in-app preview and never leave the host.
	Captures() bool
}

// PermanentError marks a rejection that retrying cannot fix.
type PermanentError struct{ Msg string }

func (e *PermanentError) Error() string { return e.Msg }

// CaptureSender keeps mail local: used by DEMO_MODE and when no relay is configured.
type CaptureSender struct{ Log *slog.Logger }

func (CaptureSender) Send(context.Context, Message) error { return nil }
func (CaptureSender) Captures() bool                      { return true }

// RelayRequest is the JSON body POSTed to the mail relay (documented in docs/INTEGRATION.md).
type RelayRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	FromName       string `json:"from_name"`
	ReplyTo        string `json:"reply_to,omitempty"`
	To             string `json:"to"`
	ToName         string `json:"to_name,omitempty"`
	Subject        string `json:"subject"`
	HTML           string `json:"html"`
	Text           string `json:"text"`
}

// RelaySender posts messages to the authenticated HTTPS mail relay.
type RelaySender struct {
	URL      string
	Key      string
	FromName string
	Client   *http.Client
}

// NewRelaySender builds a sender with sane timeouts and no redirect following.
func NewRelaySender(url, key, fromName string) *RelaySender {
	return &RelaySender{URL: url, Key: key, FromName: fromName, Client: &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (r *RelaySender) Captures() bool { return false }

func (r *RelaySender) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(RelayRequest{IdempotencyKey: m.Key, FromName: r.FromName, ReplyTo: m.ReplyTo, To: m.ToEmail, ToName: m.ToName, Subject: m.Subject, HTML: m.HTML, Text: m.Text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.Key)
	req.Header.Set("Idempotency-Key", m.Key)
	resp, err := r.Client.Do(req)
	if err != nil {
		return err // transport failure: retry
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("relay returned HTTP %d", resp.StatusCode)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("relay rejected credentials (HTTP %d)", resp.StatusCode) // operator fixes the key; keep retrying
	default:
		return &PermanentError{Msg: fmt.Sprintf("relay refused the message (HTTP %d)", resp.StatusCode)}
	}
}

// ErrNoSender is returned when mail is enqueued with no sender configured.
var ErrNoSender = errors.New("no mail sender configured")
