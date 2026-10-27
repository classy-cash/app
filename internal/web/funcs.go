package web

import (
	"context"
	"fmt"
	"html/template"
	"net"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/money"
	"github.com/classy-cash/app/internal/store"
	"github.com/classy-cash/app/internal/version"
)

func (s *Server) funcs() template.FuncMap {
	loc := s.Cfg.Location
	return template.FuncMap{
		"money": money.Format,
		"plain": money.Plain,
		"date": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.In(loc).Format("02.01.2006")
		},
		"datep": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.In(loc).Format("02.01.2006")
		},
		"datetime": func(t time.Time) string { return t.In(loc).Format("02.01.2006 15:04") },
		"input_date": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.In(loc).Format("2006-01-02")
		},
		"today": func(t time.Time) bool {
			n := time.Now().In(loc)
			return t.In(loc).Format("2006-01-02") == n.Format("2006-01-02")
		},
		"hm":      func(t time.Time) string { return t.In(loc).Format("15:04") },
		"initial": initial,
		"add":     func(a, b int) int { return a + b },
		"neg":     func(a int64) int64 { return -a },
		"abs": func(a int64) int64 {
			if a < 0 {
				return -a
			}
			return a
		},
		"status":   statusLabel,
		"spStatus": spStatusLabel,
		"deref":    func(p *int64) int64 { return *p },
		"has":      func(p *int64) bool { return p != nil },
		"deref_time": func(t *time.Time) time.Time {
			if t == nil {
				return time.Time{}
			}
			return *t
		},
		"eqid":  func(a int64, b *int64) bool { return b != nil && *b == a },
		"lower": strings.ToLower,
		"cell": func(c store.Cell) string {
			if c.IsMoney {
				return money.Format(c.Minor)
			}
			return c.Text
		},
		"release": func() string { return version.Label(loc) },
		"month":   func(t time.Time) string { return polishMonths[t.In(loc).Month()-1] },
		"now":     time.Now,
		"pct": func(n, d int) int {
			if d == 0 {
				return 0
			}
			return n * 100 / d
		},
		"join":        strings.Join,
		"maskAccount": maskAccount,
		"quote":       func(s string) string { return fmt.Sprintf("%q", s) },
	}
}

var polishMonths = []string{"styczeń", "luty", "marzec", "kwiecień", "maj", "czerwiec", "lipiec", "sierpień", "wrzesień", "październik", "listopad", "grudzień"}

func initial(s string) string {
	for _, r := range strings.TrimSpace(s) {
		return strings.ToUpper(string(r))
	}
	return "?"
}

func statusLabel(s string) string {
	switch s {
	case "pending":
		return "Oczekuje"
	case "paid":
		return "Opłacona"
	case "failed":
		return "Nieudana"
	case "refunded":
		return "Zwrócona"
	case "expired":
		return "Wygasła"
	}
	return s
}

func spStatusLabel(s string) string {
	switch s {
	case "pending":
		return "Do zapłaty"
	case "partial":
		return "Częściowo"
	case "paid":
		return "Zapłacono"
	}
	return s
}

func maskAccount(s string) string { return s }

// webmail guesses the provider's inbox for the "Open your mail" button on the sent-link screen.
func webmail(email string) (name, url string) {
	domain := email
	if i := strings.LastIndex(email, "@"); i >= 0 {
		domain = strings.ToLower(email[i+1:])
	}
	switch {
	case domain == "gmail.com" || domain == "googlemail.com":
		return "Gmail", "https://mail.google.com"
	case domain == "outlook.com" || domain == "hotmail.com" || domain == "live.com" || domain == "outlook.pl":
		return "Outlook", "https://outlook.live.com/mail"
	case domain == "wp.pl":
		return "Poczta WP", "https://poczta.wp.pl"
	case domain == "o2.pl" || domain == "tlen.pl" || domain == "go2.pl":
		return "Poczta o2", "https://poczta.o2.pl"
	case domain == "interia.pl" || domain == "interia.eu" || domain == "poczta.fm":
		return "Poczta Interia", "https://poczta.interia.pl"
	case domain == "onet.pl" || domain == "op.pl" || domain == "onet.eu" || domain == "vp.pl":
		return "Poczta Onet", "https://poczta.onet.pl"
	case domain == "icloud.com" || domain == "me.com":
		return "iCloud Mail", "https://www.icloud.com/mail"
	case strings.HasPrefix(domain, "proton") || domain == "pm.me":
		return "Proton Mail", "https://mail.proton.me"
	case domain == "yahoo.com":
		return "Yahoo Mail", "https://mail.yahoo.com"
	}
	return "skrzynkę pocztową", ""
}

// webmailFromMX recognises custom domains hosted by the big providers (legacy WebmailUrlService looked at
// MX records). Only called for domains webmail() did not know; the lookup is short and cached.
func webmailFromMX(ctx context.Context, email string, lookup func(context.Context, string) ([]*net.MX, error)) (name, url string) {
	i := strings.LastIndex(email, "@")
	if i < 0 {
		return "skrzynkę pocztową", ""
	}
	domain := strings.ToLower(email[i+1:])
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	mxs, err := lookup(ctx, domain)
	if err != nil {
		return "skrzynkę pocztową", ""
	}
	var hosts []string
	for _, m := range mxs {
		hosts = append(hosts, strings.ToLower(m.Host))
	}
	all := strings.Join(hosts, " ")
	switch {
	case strings.Contains(all, "google.com") || strings.Contains(all, "googlemail.com"):
		return "Gmail", "https://mail.google.com"
	case strings.Contains(all, "outlook.com") || strings.Contains(all, "microsoft.com"):
		return "Outlook", "https://outlook.office.com/mail"
	case strings.Contains(all, "zoho"):
		return "Zoho Mail", "https://mail.zoho.com"
	}
	return "skrzynkę pocztową", ""
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }

func constEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func moneyFmt(minor int64) string { return money.Format(minor) }

// webmailMX caches MX-based provider detection per domain so repeated link requests do not hit DNS.
func (s *Server) webmailMX(ctx context.Context, email string) (string, string) {
	domain := email[strings.LastIndex(email, "@")+1:]
	s.mxMu.Lock()
	if c, ok := s.mxCache[domain]; ok && time.Since(c.at) < time.Hour {
		s.mxMu.Unlock()
		return c.name, c.url
	}
	s.mxMu.Unlock()
	name, url := webmailFromMX(ctx, email, net.DefaultResolver.LookupMX)
	s.mxMu.Lock()
	if len(s.mxCache) > 256 {
		s.mxCache = nil
	}
	if s.mxCache == nil {
		s.mxCache = map[string]mxEntry{}
	}
	s.mxCache[domain] = mxEntry{name, url, time.Now()}
	s.mxMu.Unlock()
	return name, url
}

type mxEntry struct {
	name, url string
	at        time.Time
}
