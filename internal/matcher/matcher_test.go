package matcher

import (
	"slices"
	"testing"
)

func TestCandidatesOrder(t *testing.T) {
	got := Candidates("kod AB 12")
	for _, want := range []string{"KOD", "AB", "12", "AB12"} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	if slices.Index(got, "AB") > slices.Index(got, "AB12") {
		t.Fatal("single words must come before glued words")
	}
}

func TestCandidatesZeroAndLetterO(t *testing.T) {
	if !slices.Contains(Candidates("AOB1"), "A0B1") {
		t.Fatal("O→0 substitution missing")
	}
	if !slices.Contains(Candidates("XYZ0"), "XYZO") {
		t.Fatal("0→O substitution missing")
	}
	if !slices.Contains(Candidates("TOS0"), "T0SO") {
		t.Fatal("simultaneous swap missing")
	}
}

func TestCandidatesEmptyAndPunctuation(t *testing.T) {
	for _, in := range []string{"", "   ", "!!! ---"} {
		if c := Candidates(in); len(c) != 0 {
			t.Fatalf("%q => %v", in, c)
		}
	}
}

func TestCandidatesDeduplicated(t *testing.T) {
	c := Candidates("AAAA AAAA")
	seen := map[string]bool{}
	for _, x := range c {
		if seen[x] {
			t.Fatalf("duplicate %q", x)
		}
		seen[x] = true
	}
}

// Table ported from MatchPaymentForTransferHandlerTest::paymentMatchingProvider.
func TestLegacyMatchingTable(t *testing.T) {
	cases := []struct{ code, title string }{
		{"TEST", "Payment for order TEST additional text"},
		{"TES0", "Payment for order TESO additional text"},
		{"TESO", "Payment for order TES0 additional text"},
		{"T0SO", "Payment for order TOS0 additional text"},
		{"XYZ9", "Some random text XYZ9 more text here"},
		{"END1", "Transfer description ending with END1"},
		{"SPEC", "Payment: SPEC - for services!"},
		{"SPEC", "Payment: SPEC, - for services!"},
		{"SPEC", "Payment: SPE C, - for services!"},
		{"SPEC", "Pay ment: SPE C, - for services!"},
		{"SPEC", "Pay  ment: SPE  C, - for services!"},
		{"SPEC", "Pay  ment: SPE   C, - for services!"},
	}
	for _, c := range cases {
		if !slices.Contains(Candidates(c.title), c.code) {
			t.Errorf("code %s not found in %q: %v", c.code, c.title, Candidates(c.title))
		}
	}
}

func TestHintDescription(t *testing.T) {
	h := Hint{StudentNames: []string{"Jan Kowalski"}, SameAccountNumber: true, SameSender: true}
	if got := h.Description(); got != "Ten sam nadawca i numer konta — wcześniej przypisano do: Jan Kowalski" {
		t.Fatal(got)
	}
	if (Hint{SameSender: true}).Description() != "Ten sam nadawca — wcześniej przypisano do tego rodzica" {
		t.Fatal("parent-only hint")
	}
}
