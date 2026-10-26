package money

import "testing"

// Cases ported from tests/Domain/TransferMoneyParserTest.php.
func TestParseLegacyCases(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0.00", 0}, {"15 011,75", 1501175}, {"2 000,00", 200000}, {"200,00", 20000},
		{"0,01", 1}, {"1 234 567,89", 123456789}, {"1.234.567,89", 123456789},
		{"1,23", 123}, {",5", 50}, {"5,", 500}, {"5", 500}, {"5.00", 500},
		{"50,00", 5000}, {"12,345", 1234}, {"1234.5", 123450},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil || got != c.want {
			t.Errorf("Parse(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "abc", " ", ",", "zł"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) should fail", in)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := map[int64]string{0: "0 zł", 5000: "50 zł", 5050: "50,50 zł", 123456789: "1 234 567,89 zł", -250: "-2,50 zł", 5: "0,05 zł"}
	for in, want := range cases {
		if got := Format(in); got != want {
			t.Errorf("Format(%d) = %q want %q", in, got, want)
		}
	}
	if Plain(1250) != "12.50" || Plain(-5) != "-0.05" {
		t.Errorf("Plain wrong: %s %s", Plain(1250), Plain(-5))
	}
}
