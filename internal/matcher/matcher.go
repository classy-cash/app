// Package matcher ports the payment-code tokenizer and the historical scoring weights from the
// legacy TransferPaymentMatcher. It is pure: store.go supplies data and applies results.
package matcher

import (
	"fmt"
	"regexp"
	"strings"
)

// Scoring weights (legacy constants).
const (
	ScoreStudentMatch = 200
	ScoreUserMatch    = 100
	ScoreAmountMatch  = 50
)

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// Candidates returns the ordered, de-duplicated payment-code candidates found in a transfer title:
// single words first, then adjacent words glued together (codes split by a space), then the same
// tokens with 0/O substituted. The first candidate that is a known code wins.
func Candidates(title string) []string {
	upper := strings.ToUpper(title)
	tokens := strings.Fields(nonAlnum.ReplaceAllString(upper, " "))
	var emitted []string
	emitted = append(emitted, tokens...)
	for i := 0; i+1 < len(tokens); i++ {
		emitted = append(emitted, tokens[i]+tokens[i+1])
	}
	out := append([]string(nil), emitted...)
	swap := strings.NewReplacer("0", "O", "O", "0")
	for _, tok := range emitted {
		if s := strings.ReplaceAll(tok, "0", "O"); s != tok {
			out = append(out, s)
		}
		if s := strings.ReplaceAll(tok, "O", "0"); s != tok {
			out = append(out, s)
		}
		if s := swap.Replace(tok); s != tok {
			out = append(out, s)
		}
	}
	seen := make(map[string]bool, len(out))
	uniq := out[:0:0]
	for _, c := range out {
		if !seen[c] {
			seen[c] = true
			uniq = append(uniq, c)
		}
	}
	return uniq
}

// Hint explains why a payment is suggested for a transfer.
type Hint struct {
	StudentNames         []string
	SameAccountNumber    bool
	SameSender           bool
	HistoricalMatchCount int
	Score                int
}

// HasStudentMatch reports whether a previously matched student recurs.
func (h Hint) HasStudentMatch() bool { return len(h.StudentNames) > 0 }

// Description is the Polish explanation shown in the assignment dialog.
func (h Hint) Description() string {
	var source string
	switch {
	case h.SameAccountNumber && h.SameSender:
		source = "Ten sam nadawca i numer konta"
	case h.SameAccountNumber:
		source = "Ten sam numer konta"
	default:
		source = "Ten sam nadawca"
	}
	if h.HasStudentMatch() {
		return fmt.Sprintf("%s — wcześniej przypisano do: %s", source, strings.Join(h.StudentNames, ", "))
	}
	return source + " — wcześniej przypisano do tego rodzica"
}
