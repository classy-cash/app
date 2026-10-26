package alior

import "testing"

// Ported from tests/Application/Service/AliorMailParserTest.php.
const legacyBody = `<html><br/>
Uprzejmie informujemy, że rachunek 91...6978 został uznany kwotą 50,00 PLN.<br/>
Nadawca: JAKIŚ RANDOM<br/>
Tytuł zlecenia: X2el<br/>
Saldo rachunku po operacji: 100,00 PLN
<br/>
Z poważaniem<br/>
Zespół Alior Bank<br/>
</html>`

func TestParseLegacyFixture(t *testing.T) {
	n, ok := Parse("Uznanie rachunku 91...6978 kwotą 50,00 PLN", legacyBody)
	if !ok {
		t.Fatal("expected a parse")
	}
	if n.AccountNumber != "91...6978" || n.Sender != "JAKIŚ RANDOM" || n.Title != "X2el" || n.Amount != "50,00" {
		t.Fatalf("unexpected result: %+v", n)
	}
}

func TestParsePlainTextAlternative(t *testing.T) {
	text := "Uprzejmie informujemy, że rachunek 91...6978 został uznany kwotą 1 250,50 PLN.\nNadawca: ANNA KOWALSKA\nTytuł zlecenia: AB12 wycieczka\nSaldo rachunku po operacji: 100,00 PLN\n"
	n, ok := Parse("Uznanie rachunku 91...6978 kwotą 1 250,50 PLN", text)
	if !ok || n.Amount != "1 250,50" || n.Sender != "ANNA KOWALSKA" || n.Title != "AB12 wycieczka" {
		t.Fatalf("got %+v ok=%v", n, ok)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string][2]string{
		"other subject": {"Wyciąg z rachunku", legacyBody},
		"no sender":     {"Uznanie rachunku 91...6978 kwotą 50,00 PLN", "<br/>kwotą 50,00 PLN.<br/>Brak danych<br/>"},
		"empty":         {"", ""},
	}
	for name, c := range cases {
		if _, ok := Parse(c[0], c[1]); ok {
			t.Errorf("%s: should not parse", name)
		}
	}
}

func TestParseStripsMarkupAndEntities(t *testing.T) {
	body := `kwotą 10,00 PLN.<br/>Nadawca: <b>FIRMA &amp; SYN</b><br/>Tytuł zlecenia: ZA KSIĄŻKI<br/>`
	n, ok := Parse("Uznanie rachunku 12.34 kwotą 10,00 PLN", body)
	if !ok || n.Sender != "FIRMA & SYN" || n.Title != "ZA KSIĄŻKI" {
		t.Fatalf("got %+v ok=%v", n, ok)
	}
}
