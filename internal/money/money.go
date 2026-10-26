// Package money handles PLN amounts as integer grosze. Floating point is never used.
package money

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// ErrInvalid is returned when a string cannot be read as an amount.
var ErrInvalid = errors.New("invalid amount")

var (
	numericRe = regexp.MustCompile(`^\s*[+-]?(\d+(\.\d*)?|\.\d+)\s*$`)
	nonAmount = regexp.MustCompile(`[^\d,]`)
)

// Parse reads an amount the way the legacy TransferMoneyParser did: plain "5.00" decimals are taken
// as-is; anything else (bank formats such as "15 011,75" or "1.234.567,89") keeps only digits and
// the decimal comma. More than two decimals are truncated, fewer are padded.
func Parse(s string) (int64, error) {
	if numericRe.MatchString(s) {
		return fromDecimal(strings.TrimSpace(s))
	}
	cleaned := nonAmount.ReplaceAllString(s, "")
	if cleaned == "" || strings.Trim(cleaned, ",") == "" {
		return 0, ErrInvalid
	}
	// Only the first comma is the decimal separator.
	number := strings.Replace(cleaned, ",", ".", 1)
	number = strings.ReplaceAll(number, ",", "")
	return fromDecimal(number)
}

func fromDecimal(s string) (int64, error) {
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if len(frac) > 2 {
		frac = frac[:2]
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > (1<<62)/100 {
		return 0, ErrInvalid
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, ErrInvalid
	}
	v := w*100 + f
	if neg {
		v = -v
	}
	return v, nil
}

// Format renders grosze as Polish currency: "1 234,50 zł", and "50 zł" for whole amounts.
func Format(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	whole := strconv.FormatInt(minor/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(r)
	}
	out := sign + b.String()
	if f := minor % 100; f != 0 {
		out += "," + pad2(f)
	}
	return out + " zł"
}

// Plain renders "12.50" for form values and CSV-like contexts.
func Plain(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return sign + strconv.FormatInt(minor/100, 10) + "." + pad2(minor%100)
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}
