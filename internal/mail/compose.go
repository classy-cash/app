package mail

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/money"
)

// Branding carries instance-level values used by every template.
type Branding struct {
	AppName string
	BaseURL string
}

var baseTpl = template.Must(template.New("base").Parse(`<!DOCTYPE html>
<html lang="pl"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:0;background-color:#f7f6f0;font-family:'Helvetica Neue',Helvetica,Arial,sans-serif;color:#15203d;">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="background-color:#f7f6f0;padding-bottom:40px;"><tr><td align="center">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:600px;background-color:#ffffff;border-spacing:0;">
<tr><td style="background-color:#1a2a52;padding:30px;text-align:center;color:#e8b441;font-family:Georgia,'Playfair Display',serif;font-size:22px;font-weight:bold;letter-spacing:1px;">{{.AppName}}</td></tr>
<tr><td style="padding:40px;line-height:24px;font-size:16px;">{{.Body}}</td></tr>
<tr><td style="padding:20px;text-align:center;font-size:11px;color:#6b7796;">Classy Cash — system zarządzania finansami klasowymi.<br>Ta wiadomość została wysłana automatycznie. Prosimy na nią nie odpowiadać.</td></tr>
</table></td></tr></table></body></html>`))

func wrap(b Branding, subject, body string) string {
	var buf bytes.Buffer
	_ = baseTpl.Execute(&buf, map[string]any{"Subject": subject, "AppName": b.AppName, "Body": template.HTML(body)})
	return buf.String()
}

func esc(s string) string { return template.HTMLEscapeString(s) }

func button(url, label string) string {
	return fmt.Sprintf(`<p style="margin:24px 0;"><a href="%s" style="display:inline-block;padding:14px 30px;background-color:#e8b441;color:#1a2a52;text-decoration:none;border-radius:10px;font-weight:bold;">%s</a></p>`, esc(url), esc(label))
}

// Rendered is a composed message body.
type Rendered struct{ Subject, HTML, Text string }

// LoginLink is the magic-link e-mail (first = the account has never logged in).
func LoginLink(b Branding, name, link string, first bool) Rendered {
	subject, lead := "Zaloguj się do swojego konta", "Ostatnio poprosiłeś(-aś) o link do logowania. Kliknij poniższy przycisk, aby bezpiecznie zalogować się na swoje konto:"
	if first {
		subject, lead = "Potwierdzenie rejestracji", "Kliknij poniższy przycisk, aby po raz pierwszy bezpiecznie zalogować się na swoje konto:"
	}
	body := fmt.Sprintf(`<p>Witaj %s,</p><p>%s</p>%s<p style="font-size:12px;color:#6b7796;">Jeśli powyższy przycisk nie działa, skopiuj i wklej poniższy link do przeglądarki:<br>%s</p><p style="font-size:12px;color:#6b7796;">Jeśli nie prosiłeś(-aś) o link do logowania, zignoruj tę wiadomość. Link działa przez godzinę i można go użyć tylko raz.</p>`,
		esc(name), esc(lead), button(link, "Zaloguj się"), esc(link))
	text := fmt.Sprintf("Witaj %s,\n\n%s\n\n%s\n\nLink działa przez godzinę i można go użyć tylko raz. Jeśli nie prosiłeś(-aś) o link, zignoruj tę wiadomość.\n", name, lead, link)
	return Rendered{subject, wrap(b, subject, body), text}
}

// PasswordReset is the treasurer/parent password recovery message.
func PasswordReset(b Branding, name, link string) Rendered {
	subject := "Ustaw nowe hasło"
	body := fmt.Sprintf(`<p>Witaj %s,</p><p>Otrzymaliśmy prośbę o ustawienie nowego hasła do Twojego konta. Link wygaśnie po godzinie i można go użyć tylko raz.</p>%s<p style="font-size:12px;color:#6b7796;">Jeśli to nie Ty, zignoruj tę wiadomość — hasło pozostanie bez zmian.<br>%s</p>`, esc(name), button(link, "Ustaw nowe hasło"), esc(link))
	text := fmt.Sprintf("Witaj %s,\n\nUstaw nowe hasło: %s\n\nLink wygaśnie po godzinie i można go użyć tylko raz. Jeśli to nie Ty, zignoruj tę wiadomość.\n", name, link)
	return Rendered{subject, wrap(b, subject, body), text}
}

// Invitation welcomes a parent added by the treasurer.
func Invitation(b Branding, name, className, link string) Rendered {
	subject := "Zaproszenie do klasy " + className
	body := fmt.Sprintf(`<p>Witaj %s,</p><p>Skarbnik dodał Cię do klasy <strong>%s</strong> w aplikacji %s. Zobaczysz tam składki do opłacenia i historię wpłat. Pieniądze trafiają bezpośrednio do skarbnika.</p>%s<p style="font-size:12px;color:#6b7796;">%s</p>`, esc(name), esc(className), esc(b.AppName), button(link, "Zaloguj się po raz pierwszy"), esc(link))
	text := fmt.Sprintf("Witaj %s,\n\nSkarbnik dodał Cię do klasy %s. Zaloguj się: %s\n", name, className, link)
	return Rendered{subject, wrap(b, subject, body), text}
}

// SettlementItem is one settled obligation in a confirmation.
type SettlementItem struct {
	Student     string
	Label       string
	AmountMinor int64
	Paid, Total int
	PaidAt      *time.Time
	DueAt       *time.Time
}

// PreviousPayment is an earlier paid payment of the same parent.
type PreviousPayment struct {
	Date        time.Time
	AmountMinor int64
}

// SettlementData feeds the parent confirmation.
type SettlementData struct {
	Items            []SettlementItem
	Previous         []PreviousPayment
	SumPrevious      int64
	SumIncludingThis int64
}

// SettlementConfirmation tells a parent their payment was booked.
func SettlementConfirmation(b Branding, d SettlementData, loc *time.Location) Rendered {
	subject := "Potwierdzenie rozliczenia składek"
	var rows, prev strings.Builder
	var text strings.Builder
	text.WriteString("Dziękujemy! Twoja płatność została zaksięgowana.\n\nRozliczone składki:\n")
	for _, it := range d.Items {
		fmt.Fprintf(&rows, `<tr><td style="padding:8px 0;border-bottom:1px solid #f0f2f7;">%s</td><td style="padding:8px 0;border-bottom:1px solid #f0f2f7;">%s</td><td style="padding:8px 0;border-bottom:1px solid #f0f2f7;">%s</td><td style="padding:8px 0;border-bottom:1px solid #f0f2f7;">%d/%d</td></tr>`,
			esc(it.Student), esc(it.Label), esc(money.Format(it.AmountMinor)), it.Paid, it.Total)
		fmt.Fprintf(&text, "- %s: %s — %s (opłacone w klasie: %d/%d)\n", it.Student, it.Label, money.Format(it.AmountMinor), it.Paid, it.Total)
	}
	if len(d.Previous) == 0 {
		prev.WriteString("<p>Brak wcześniejszych wpłat.</p>")
		text.WriteString("\nBrak wcześniejszych wpłat.\n")
	} else {
		prev.WriteString("<ul>")
		text.WriteString("\nPoprzednie wpłaty:\n")
		for _, p := range d.Previous {
			fmt.Fprintf(&prev, "<li>%s: %s</li>", p.Date.In(loc).Format("02.01.2006"), esc(money.Format(p.AmountMinor)))
			fmt.Fprintf(&text, "- %s: %s\n", p.Date.In(loc).Format("02.01.2006"), money.Format(p.AmountMinor))
		}
		prev.WriteString("</ul>")
	}
	fmt.Fprintf(&text, "\nSuma poprzednich wpłat: %s\nŁącznie z tą wpłatą: %s\n\nDziękujemy za terminowe wpłaty i wsparcie!\n", money.Format(d.SumPrevious), money.Format(d.SumIncludingThis))
	body := `<h1 style="font-size:24px;color:#1a2a52;margin:0 0 20px;">Potwierdzenie płatności</h1><p>Dziękujemy! Twoja płatność została zaksięgowana. Poniżej znajdziesz szczegóły rozliczenia składek.</p><h3 style="color:#1a2a52;">Rozliczone składki</h3><table width="100%" style="border-collapse:collapse;font-size:14px;"><tr><th align="left">Uczeń</th><th align="left">Składka</th><th align="left">Kwota</th><th align="left">Opłacone</th></tr>` + rows.String() + `</table><h3 style="color:#1a2a52;">Poprzednie wpłaty</h3>` + prev.String() +
		fmt.Sprintf(`<p>Suma poprzednich wpłat: <strong>%s</strong><br>Łącznie z tą wpłatą: <strong>%s</strong></p><p>Dziękujemy za terminowe wpłaty i wsparcie!</p>`, esc(money.Format(d.SumPrevious)), esc(money.Format(d.SumIncludingThis)))
	return Rendered{subject, wrap(b, subject, body), text.String()}
}

// TreasurerPaymentData feeds the treasurer's "new payment" notice.
type TreasurerPaymentData struct {
	ClassName    string
	ParentName   string
	ParentEmail  string
	Items        []SettlementItem
	MissingMinor int64
}

// TreasurerPayment tells the treasurer a parent's payment was booked.
func TreasurerPayment(b Branding, d TreasurerPaymentData, loc *time.Location) Rendered {
	subject := "Nowa wpłata w klasie - podsumowanie zaległości"
	var li strings.Builder
	var text strings.Builder
	fmt.Fprintf(&text, "Nowa wpłata w kasie!\nOd: %s (%s)\nKlasa: %s\n\nSzczegóły wpłaty:\n", d.ParentName, d.ParentEmail, d.ClassName)
	for _, it := range d.Items {
		extra := ""
		if it.PaidAt != nil {
			extra += " (opłacono: " + it.PaidAt.In(loc).Format("02.01.2006") + ")"
		}
		if it.DueAt != nil {
			extra += " (termin: " + it.DueAt.In(loc).Format("02.01.2006") + ")"
		}
		fmt.Fprintf(&li, "<li>%s: %s — %s%s</li>", esc(it.Student), esc(it.Label), esc(money.Format(it.AmountMinor)), esc(extra))
		fmt.Fprintf(&text, "- %s: %s — %s%s\n", it.Student, it.Label, money.Format(it.AmountMinor), extra)
	}
	fmt.Fprintf(&text, "\nŁączna kwota zaległości: %s\n", money.Format(d.MissingMinor))
	body := fmt.Sprintf(`<h1 style="font-size:24px;color:#1a2a52;margin:0 0 20px;">Nowa wpłata w kasie!</h1><p>System zaksięgował wpłatę od rodzica:</p><p><strong>Od:</strong> %s (%s)<br><strong>Klasa:</strong> %s</p><h3 style="color:#1a2a52;">Szczegóły wpłaty</h3><ul>%s</ul><h3 style="color:#1a2a52;">Zaległe składki w klasie</h3><p>Łączna kwota zaległości: <strong>%s</strong></p>`,
		esc(d.ParentName), esc(d.ParentEmail), esc(d.ClassName), li.String(), esc(money.Format(d.MissingMinor)))
	return Rendered{subject, wrap(b, subject, body), text.String()}
}

// TransferNotMatchedData describes an unmatched transfer.
type TransferNotMatchedData struct {
	AmountMinor int64
	Sender      string
	Title       string
	Date        time.Time
	Reason      string
	ReviewURL   string
}

// TransferNotMatched alerts the administrators to a transfer needing manual review.
func TransferNotMatched(b Branding, d TransferNotMatchedData, loc *time.Location) Rendered {
	subject := "Nie znaleziono dopasowania dla przelewu"
	when := d.Date.In(loc).Format("2006-01-02 15:04")
	reason := ""
	if d.Reason != "" {
		reason = "<p><em>" + esc(d.Reason) + "</em></p>"
	}
	body := fmt.Sprintf(`<p>Dzień dobry,</p><p>Otrzymaliśmy nowy przelew, którego nie udało się automatycznie dopasować do żadnej płatności.</p><ul><li>Kwota: %s</li><li>Nadawca: %s</li><li>Tytuł: %s</li><li>Data: %s</li></ul>%s<p>Prosimy o ręczne sprawdzenie i dopasowanie przelewu w panelu.</p>%s`,
		esc(money.Format(d.AmountMinor)), esc(d.Sender), esc(d.Title), esc(when), reason, button(d.ReviewURL, "Otwórz operacje bankowe"))
	text := fmt.Sprintf("Otrzymaliśmy nowy przelew, którego nie udało się automatycznie dopasować.\n\nKwota: %s\nNadawca: %s\nTytuł: %s\nData: %s\n%s\nSprawdź: %s\n", money.Format(d.AmountMinor), d.Sender, d.Title, when, d.Reason, d.ReviewURL)
	return Rendered{subject, wrap(b, subject, body), text}
}

// NewUserAdmin tells the administrators that an account signed in for the first time.
func NewUserAdmin(b Branding, name, email string, id int64) Rendered {
	subject := "Nowy użytkownik zarejestrowany - " + email
	body := fmt.Sprintf(`<p>Dzień dobry,</p><p>Nowe konto zalogowało się po raz pierwszy:</p><ul><li>ID użytkownika: #%d</li><li>Imię: %s</li><li>E-mail: %s</li></ul>`, id, esc(name), esc(email))
	text := fmt.Sprintf("Nowe konto zalogowało się po raz pierwszy.\n\nID użytkownika: #%d\nImię: %s\nE-mail: %s\n", id, name, email)
	return Rendered{subject, wrap(b, subject, body), text}
}

// PartialPayment tells a parent that their transfer differs from the amount due.
func PartialPayment(b Branding, parentName, label string, dueMinor, paidMinor int64) Rendered {
	subject := "Wpłata różni się od należnej kwoty"
	diff := paidMinor - dueMinor
	what := "Brakuje jeszcze " + money.Format(-diff) + "."
	if diff > 0 {
		what = "Wpłacono o " + money.Format(diff) + " więcej niż należność — skarbnik się z Tobą skontaktuje."
	}
	body := fmt.Sprintf(`<p>Witaj %s,</p><p>Otrzymaliśmy Twoją wpłatę za „%s”: %s, a należność wynosi %s. %s</p><p>W razie pytań skontaktuj się ze skarbnikiem klasy.</p>`, esc(parentName), esc(label), esc(money.Format(paidMinor)), esc(money.Format(dueMinor)), esc(what))
	text := fmt.Sprintf("Witaj %s,\n\nOtrzymaliśmy Twoją wpłatę za %q: %s, a należność wynosi %s. %s\n", parentName, label, money.Format(paidMinor), money.Format(dueMinor), what)
	return Rendered{subject, wrap(b, subject, body), text}
}
