package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/money"
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
		"eqid":    func(a int64, b *int64) bool { return b != nil && *b == a },
		"lower":   strings.ToLower,
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
