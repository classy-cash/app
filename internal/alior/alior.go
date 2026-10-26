// Package alior parses Alior Bank credit notifications ("Uznanie rachunku ...").
package alior

import (
	"regexp"
	"strings"
)

// Notification is the data extracted from one credit notification.
type Notification struct {
	AccountNumber string
	Sender        string
	Title         string
	Amount        string // as printed by the bank, e.g. "50,00"
}

var (
	subjectRe = regexp.MustCompile(`Uznanie rachunku ([0-9.]+) kwotą ([0-9 ,.]+) PLN`)
	bodyRe    = regexp.MustCompile(`(?s)kwotą ([0-9., ]+) PLN.*?Nadawca: (.*?)<br.*?Tytuł zlecenia: (.*?)<br`)
	tagRe     = regexp.MustCompile(`(?i)<[^>]+>`)
	nlRe      = regexp.MustCompile(`\r?\n`)
)

// HasCreditSubject reports whether the subject looks like a credit notification.
func HasCreditSubject(subject string) bool { return strings.HasPrefix(subject, "Uznanie rachunku") }

// Parse mirrors the legacy AliorMailParser. The body may be the bank's HTML or its plain-text
// alternative; text is normalised so the same expression applies. Returns false when the message
// is not a recognisable credit notification.
func Parse(subject, body string) (Notification, bool) {
	m := subjectRe.FindStringSubmatch(subject)
	if m == nil {
		return Notification{}, false
	}
	account := strings.TrimSpace(m[1])
	b := body
	if !strings.Contains(strings.ToLower(b), "<br") {
		b = nlRe.ReplaceAllString(b, "<br/>\n") + "<br/>"
	}
	bm := bodyRe.FindStringSubmatch(b)
	if bm == nil {
		return Notification{}, false
	}
	return Notification{
		AccountNumber: account,
		Amount:        strings.TrimSpace(bm[1]),
		Sender:        clean(bm[2]),
		Title:         clean(bm[3]),
	}, true
}

func clean(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	return strings.TrimSpace(htmlUnescape(s))
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ")
	return r.Replace(s)
}
