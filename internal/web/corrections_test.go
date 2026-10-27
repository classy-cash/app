package web

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/classy-cash/app/internal/auth"
	"github.com/classy-cash/app/internal/store"
)

func id(n int64) string { return strconv.FormatInt(n, 10) }

func TestLateStudentAndCollectionCorrectionsOverHTTP(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	_, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")

	if code, _, _ := tr.post("/treasurer/contributions", url.Values{"title": {"Wycieczka"}, "amount_pln": {"50,00"}, "students": {id(kid.ID)}}); code != 303 {
		t.Fatal(code)
	}
	views, _ := h.st.ContributionViews(ctx, cls.ID)
	cid := id(views[0].ID)

	// A new pupil joins the class: the add form enrols them in running collections.
	tr.post("/treasurer/students", url.Values{"first_name": {"Zofia"}, "last_name": {"Nowak"}, "join_open": {"1"}})
	tr.post("/treasurer/students", url.Values{"first_name": {"Piotr"}, "last_name": {"Bez"}}) // checkbox off
	views, _ = h.st.ContributionViews(ctx, cls.ID)
	if len(views[0].Items) != 2 {
		t.Fatalf("only the pupil added with the checkbox joins automatically, got %d items", len(views[0].Items))
	}
	if len(views[0].Available) != 1 || views[0].Available[0].FirstName != "Piotr" {
		t.Fatalf("Piotr should be offered for manual enrolment: %+v", views[0].Available)
	}
	if _, body, _ := tr.get("/treasurer/contributions"); !strings.Contains(body, "Dodaj ucznia do zbiórki") || !strings.Contains(body, "Piotr Bez") {
		t.Fatal("contributions page must offer the manual add")
	}
	if code, _, _ := tr.post("/treasurer/contributions/"+cid+"/students", url.Values{"student_id": {id(views[0].Available[0].ID)}}); code != 303 {
		t.Fatal(code)
	}
	if v, _ := h.st.ContributionViews(ctx, cls.ID); len(v[0].Items) != 3 {
		t.Fatal("manual add failed")
	}

	// Correct a typo in the amount and the title.
	tr.post("/treasurer/contributions/"+cid+"/edit", url.Values{"title": {"Wycieczka górska"}, "amount_pln": {"65,00"}, "due_at": {"2099-05-01"}})
	v, _ := h.st.ContributionViews(ctx, cls.ID)
	if v[0].Title != "Wycieczka górska" || v[0].AmountMinor != 6500 || v[0].DueAt == nil || v[0].Items[0].SP.AmountMinor != 6500 {
		t.Fatalf("edit not applied: %+v", v[0].Contribution)
	}
	if code, _, _ := tr.post("/treasurer/contributions/"+cid+"/edit", url.Values{"title": {""}, "amount_pln": {"x"}}); code != 303 {
		t.Fatal("invalid edit should flash and redirect")
	}
	if v, _ = h.st.ContributionViews(ctx, cls.ID); v[0].Title != "Wycieczka górska" {
		t.Fatal("an invalid edit must change nothing")
	}

	// Close: confirmation text, unpaid obligations withdrawn on request, reopen restores.
	tr.post("/treasurer/contributions/"+cid+"/close", url.Values{"cancel_unpaid": {"1"}})
	v, _ = h.st.ContributionViews(ctx, cls.ID)
	if !v[0].Closed || len(v[0].Items) != 0 {
		t.Fatalf("closing with withdrawal: closed=%v items=%d", v[0].Closed, len(v[0].Items))
	}
	if _, body, _ := tr.get("/treasurer/contributions"); !strings.Contains(body, "Zakończona") || !strings.Contains(body, "Wznów zbiórkę") {
		t.Fatal("closed collections show a badge and a reopen button")
	}
	tr.post("/treasurer/contributions/"+cid+"/reopen", url.Values{})
	if v, _ = h.st.ContributionViews(ctx, cls.ID); v[0].Closed {
		t.Fatal("reopen failed")
	}

	// Delete a mistaken, unpaid collection; a paid one refuses.
	tr.post("/treasurer/contributions/"+cid+"/delete", url.Values{})
	if v, _ = h.st.ContributionViews(ctx, cls.ID); len(v) != 0 {
		t.Fatal("unpaid collection should be deletable")
	}
	h.st.CreateContribution(ctx, cls.ID, "Opłacona", 1000, nil, []int64{kid.ID})
	v, _ = h.st.ContributionViews(ctx, cls.ID)
	tr.post("/treasurer/obligations/"+id(v[0].Items[0].SP.ID)+"/paid", url.Values{})
	_, body, _ := tr.get("/treasurer/contributions")
	if strings.Contains(body, "Usuń (pomyłka)") {
		t.Fatal("no delete button once money was collected")
	}
	tr.post("/treasurer/contributions/"+id(v[0].ID)+"/delete", url.Values{})
	if v, _ = h.st.ContributionViews(ctx, cls.ID); len(v) != 1 {
		t.Fatal("a collection with payments must survive a forged delete request")
	}
}

func TestManualEntriesCanBeReversedOverHTTP(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	tr.post("/treasurer/manual-transactions", url.Values{"transaction_type": {"income"}, "amount_pln": {"100,00"}})
	tr.post("/treasurer/manual-transactions", url.Values{"transaction_type": {"expense"}, "amount_pln": {"250,00"}, "expense_description": {"Kwiaty"}})
	if sum, _ := h.st.Summary(ctx, cls.ID); sum.TotalMinor != -15000 {
		t.Fatalf("balance %d", sum.TotalMinor)
	}
	_, body, _ := tr.get("/treasurer/manual-transactions")
	if !strings.Contains(body, "Ostatnie wpisy ręczne") || !strings.Contains(body, "Kwiaty") || !strings.Contains(body, "Cofnij") {
		t.Fatal("history with reverse buttons expected")
	}
	entries, _ := h.st.ManualEntries(ctx, cls.ID, 10)
	for _, e := range entries {
		if code, _, _ := tr.post("/treasurer/manual-transactions/"+e.Kind+"/"+id(e.ID)+"/reverse", url.Values{}); code != 303 {
			t.Fatal(code)
		}
	}
	if sum, _ := h.st.Summary(ctx, cls.ID); sum.TotalMinor != 0 {
		t.Fatalf("both entries reversed, balance %d", sum.TotalMinor)
	}
	_, body, _ = tr.get("/treasurer/manual-transactions")
	if strings.Count(body, "Cofnięty") != 2 || strings.Contains(body, `>Cofnij</button>`) {
		t.Fatal("reversed entries are shown as such and cannot be reversed again")
	}
	if code, _, _ := tr.post("/treasurer/manual-transactions/bogus/1/reverse", url.Values{}); code != 303 {
		t.Fatal("unknown kind should be handled gracefully")
	}
}

func TestCorrectionsAreForbiddenToParentsAndCouncil(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	_, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	cidv, _ := h.st.CreateContribution(ctx, cls.ID, "Wycieczka", 5000, nil, []int64{kid.ID})
	cid := id(cidv)
	h.st.ManualIncome(ctx, cls.ID, store.User{ID: 1}, 100, 0)
	entries, _ := h.st.ManualEntries(ctx, cls.ID, 5)

	tr.post("/treasurer/members", url.Values{"email": {"ewa@example.com"}, "name": {"Ewa"}, "role": {"president"}})
	ewa, _ := h.st.UserByEmail(ctx, "ewa@example.com")
	hash, _ := auth.HashPassword("councilpass1")
	h.st.SetPassword(ctx, ewa.ID, hash)
	council := h.client()
	council.login("ewa@example.com", "councilpass1")
	anna := h.client()
	anna.login("anna@example.com", "parentpass1")

	forms := map[string]url.Values{
		"/treasurer/contributions/" + cid + "/edit":                                {"title": {"x"}, "amount_pln": {"1"}},
		"/treasurer/contributions/" + cid + "/students":                            {"student_id": {id(kid.ID)}},
		"/treasurer/contributions/" + cid + "/close":                               {},
		"/treasurer/contributions/" + cid + "/reopen":                              {},
		"/treasurer/contributions/" + cid + "/delete":                              {},
		"/treasurer/manual-transactions/income/" + id(entries[0].ID) + "/reverse":  {},
		"/treasurer/manual-transactions/expense/" + id(entries[0].ID) + "/reverse": {},
	}
	for name, c := range map[string]*client{"council": council, "parent": anna} {
		for p, f := range forms {
			if code, _, _ := c.post(p, f); code != 403 {
				t.Errorf("%s POST %s: %d, want 403", name, p, code)
			}
		}
	}
	if v, _ := h.st.ContributionViews(ctx, cls.ID); len(v) != 1 || v[0].Title != "Wycieczka" {
		t.Fatal("forbidden requests must not change anything")
	}
	if e, _ := h.st.ManualEntries(ctx, cls.ID, 5); e[0].Reversed {
		t.Fatal("forbidden reversal went through")
	}
	// The read-only council page shows none of the management controls.
	if _, body, _ := council.get("/treasurer/contributions"); strings.Contains(body, "Edytuj lub zakończ") {
		t.Fatal("council must not see the correction controls")
	}
}

func TestRemindersOverHTTPAreManagerOnly(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	_, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	cid, _ := h.st.CreateContribution(ctx, cls.ID, "Wycieczka", 5000, nil, []int64{kid.ID})

	_, body, _ := tr.get("/treasurer/contributions")
	if !strings.Contains(body, "Przypomnienia dla rodziców") || !strings.Contains(body, "Przypomnij rodzicom") {
		t.Fatal("reminder controls expected for the treasurer")
	}
	if code, _, _ := tr.post("/treasurer/reminders", url.Values{"scope": {"all"}, "contribution_id": {id(cid)}}); code != 303 {
		t.Fatal(code)
	}
	var queued int
	mails, _ := h.st.RecentMail(ctx, 50)
	for _, m := range mails {
		if m.ToEmail == "anna@example.com" && strings.Contains(m.Subject, "Przypomnienie") {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("queued %d", queued)
	}
	tr.post("/treasurer/reminders", url.Values{"scope": {"all"}}) // double click: cooled down
	mails, _ = h.st.RecentMail(ctx, 50)
	if countSubject(mails, "Przypomnienie") != 1 {
		t.Fatal("cooldown must prevent a second reminder")
	}

	anna := h.client()
	anna.login("anna@example.com", "parentpass1")
	if code, _, _ := anna.post("/treasurer/reminders", url.Values{"scope": {"all"}}); code != 403 {
		t.Fatalf("parents must not send reminders: %d", code)
	}
}

func countSubject(mails []store.OutboxMail, sub string) int {
	n := 0
	for _, m := range mails {
		if strings.Contains(m.Subject, sub) {
			n++
		}
	}
	return n
}

func TestIndividualAmountOverHTTP(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	_, kid := h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")
	h.st.CreateContribution(ctx, cls.ID, "Wycieczka", 5000, nil, []int64{kid.ID})
	v, _ := h.st.ContributionViews(ctx, cls.ID)
	sp := id(v[0].Items[0].SP.ID)

	_, body, _ := tr.get("/treasurer/contributions")
	if !strings.Contains(body, "Zmień kwotę") {
		t.Fatal("amount control expected for unpaid obligations")
	}
	if code, _, _ := tr.post("/treasurer/obligations/"+sp+"/amount", url.Values{"amount_pln": {"30,00"}}); code != 303 {
		t.Fatal(code)
	}
	v, _ = h.st.ContributionViews(ctx, cls.ID)
	if v[0].Items[0].SP.AmountMinor != 3000 || !v[0].Items[0].SP.Overridden {
		t.Fatalf("%+v", v[0].Items[0].SP)
	}
	if _, body, _ = tr.get("/treasurer/contributions"); !strings.Contains(body, "Kwota indywidualna") || !strings.Contains(body, "Przywróć") {
		t.Fatal("overridden obligations are flagged with a reset button")
	}
	tr.post("/treasurer/obligations/"+sp+"/amount", url.Values{"amount_pln": {"abc"}})
	tr.post("/treasurer/obligations/"+sp+"/amount", url.Values{"amount_pln": {"0"}})
	if v, _ = h.st.ContributionViews(ctx, cls.ID); v[0].Items[0].SP.AmountMinor != 3000 {
		t.Fatal("invalid amounts must change nothing")
	}
	tr.post("/treasurer/obligations/"+sp+"/amount", url.Values{"reset": {"1"}})
	if v, _ = h.st.ContributionViews(ctx, cls.ID); v[0].Items[0].SP.AmountMinor != 5000 || v[0].Items[0].SP.Overridden {
		t.Fatal("reset must restore the collection amount")
	}
	anna := h.client()
	anna.login("anna@example.com", "parentpass1")
	if code, _, _ := anna.post("/treasurer/obligations/"+sp+"/amount", url.Values{"amount_pln": {"1"}}); code != 403 {
		t.Fatalf("parents must not change amounts: %d", code)
	}
}
