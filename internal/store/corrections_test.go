package store

import (
	"errors"
	"testing"
	"time"
)

func (f *fixture) views() []ContributionView {
	f.t.Helper()
	v, err := f.s.ContributionViews(f.ctx, f.class.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) cashBalance() int64 {
	f.t.Helper()
	sum, err := f.s.Summary(f.ctx, f.class.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return sum.TotalMinor
}

func TestLateStudentJoinsOnlyRunningCollections(t *testing.T) {
	f := newFixture(t)
	past := f.now.Add(-48 * time.Hour)
	future := f.now.Add(72 * time.Hour)
	mk := func(title string, due *time.Time) int64 {
		id, err := f.s.CreateContribution(f.ctx, f.class.ID, title, 5000, due, []int64{f.kid.ID})
		f.must(err)
		return id
	}
	mk("Bez terminu", nil)
	mk("Przyszła", &future)
	mk("Minęła", &past)
	closedID := mk("Zamknięta", nil)
	_, err := f.s.SetContributionClosed(f.ctx, f.class.ID, closedID, true, false)
	f.must(err)

	late := f.student(f.class.ID, "Zofia", "Nowak", f.parent)
	titles, err := f.s.EnrolInOpenContributions(f.ctx, f.class.ID, late.ID)
	f.must(err)
	if len(titles) != 2 || titles[0] != "Bez terminu" || titles[1] != "Przyszła" {
		t.Fatalf("enrolled in %v, want only the running collections", titles)
	}
	if again, _ := f.s.EnrolInOpenContributions(f.ctx, f.class.ID, late.ID); len(again) != 0 {
		t.Fatalf("enrolment must be idempotent, got %v", again)
	}
	if sum, _ := f.s.Summary(f.ctx, f.class.ID); sum.OutstandingCount != 6 { // 4 for the first kid + 2 for Zofia
		t.Fatalf("outstanding obligations %d", sum.OutstandingCount)
	}
}

func TestAddStudentToContributionManuallyAndGuards(t *testing.T) {
	f := newFixture(t)
	past := f.now.Add(-24 * time.Hour)
	cid, err := f.s.CreateContribution(f.ctx, f.class.ID, "Stara", 3000, &past, []int64{f.kid.ID})
	f.must(err)
	late := f.student(f.class.ID, "Ola", "Nowak")
	f.must(f.s.AddStudentToContribution(f.ctx, f.class.ID, cid, late.ID)) // explicit choice may join a finished one
	f.must(f.s.AddStudentToContribution(f.ctx, f.class.ID, cid, late.ID)) // idempotent
	v := f.views()[0]
	if len(v.Items) != 2 || len(v.Available) != 0 {
		t.Fatalf("items %d available %d", len(v.Items), len(v.Available))
	}
	if sp := v.Items[0].SP; sp == nil || sp.AmountMinor != 3000 || sp.DueAt == nil {
		t.Fatalf("obligation should copy amount and due date: %+v", sp)
	}
	other := f.class
	other2, err := f.s.CreateClass(f.ctx, "5B")
	f.must(err)
	foreign := f.student(other2.ID, "Obcy", "Uczeń")
	if err := f.s.AddStudentToContribution(f.ctx, other.ID, cid, foreign.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a student of another class must not be enrolled, got %v", err)
	}
	if err := f.s.AddStudentToContribution(f.ctx, other2.ID, cid, foreign.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a collection of another class must not be reachable, got %v", err)
	}
	_, err = f.s.SetContributionClosed(f.ctx, f.class.ID, cid, true, false)
	f.must(err)
	if err := f.s.AddStudentToContribution(f.ctx, f.class.ID, cid, f.student(f.class.ID, "Pi", "Otr").ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("closed collections accept nobody, got %v", err)
	}
}

func TestUpdateContributionRetotalsOpenPaymentsAndKeepsPaidOnes(t *testing.T) {
	f := newFixture(t)
	second := f.student(f.class.ID, "Ewa", "Kowalska", f.parent)
	cid, err := f.s.CreateContribution(f.ctx, f.class.ID, "Wycieczka", 5000, nil, []int64{f.kid.ID, second.ID})
	f.must(err)
	pay, err := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.must(err)
	if pay.AmountMinor != 10000 {
		t.Fatalf("bundle %d", pay.AmountMinor)
	}
	// Ewa pays in cash first, then the amount is corrected for the rest.
	var ewa StudentPayment
	for _, sp := range f.views()[0].Items {
		if sp.Student.ID == second.ID {
			ewa = *sp.SP
		}
	}
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 5000, ewa.ID))
	due := f.now.Add(10 * 24 * time.Hour)
	f.must(f.s.UpdateContribution(f.ctx, f.class.ID, cid, "  Wycieczka górska ", 6500, &due))

	v := f.views()[0]
	if v.Title != "Wycieczka górska" || v.AmountMinor != 6500 || v.DueAt == nil {
		t.Fatalf("contribution not updated: %+v", v.Contribution)
	}
	for _, it := range v.Items {
		switch it.Student.ID {
		case f.kid.ID:
			if it.SP.AmountMinor != 6500 || it.SP.Label != "Wycieczka górska" {
				t.Fatalf("open obligation must follow the correction: %+v", it.SP)
			}
		case second.ID:
			if it.SP.AmountMinor != 5000 || it.SP.Status != SPPaid {
				t.Fatalf("paid obligation must keep its amount: %+v", it.SP)
			}
		}
	}
	got, _ := f.s.PaymentByID(f.ctx, f.class.ID, pay.ID)
	if got.AmountMinor != 6500 || len(got.Items) != 1 {
		t.Fatalf("open payment must be re-totalled from its remaining obligations: %+v", got)
	}
	if err := f.s.UpdateContribution(f.ctx, f.class.ID, cid, "", 100, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty title must be rejected")
	}
	if err := f.s.UpdateContribution(f.ctx, 9999, cid, "x", 100, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("another class must not reach the collection")
	}
}

func TestCloseContributionWithdrawsOnlyUnstartedObligations(t *testing.T) {
	f := newFixture(t)
	b := f.student(f.class.ID, "Bartek", "Nowak", f.parent)
	c := f.student(f.class.ID, "Celina", "Wiśniewska", f.parent)
	cid, err := f.s.CreateContribution(f.ctx, f.class.ID, "Rada rodziców", 5000, nil, []int64{f.kid.ID, b.ID, c.ID})
	f.must(err)
	byStudent := map[int64]StudentPayment{}
	for _, it := range f.views()[0].Items {
		byStudent[it.Student.ID] = *it.SP
	}
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 5000, byStudent[f.kid.ID].ID)) // paid
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 1000, byStudent[b.ID].ID))     // part-paid

	n, err := f.s.SetContributionClosed(f.ctx, f.class.ID, cid, true, true)
	f.must(err)
	if n != 1 {
		t.Fatalf("withdrew %d, want only Celina's untouched obligation", n)
	}
	v := f.views()[0]
	if !v.Closed || len(v.Items) != 2 {
		t.Fatalf("closed=%v items=%d", v.Closed, len(v.Items))
	}
	if v.Overdue {
		t.Fatal("a closed collection is never overdue")
	}
	if _, err := f.s.SetContributionClosed(f.ctx, f.class.ID, cid, false, false); err != nil || f.views()[0].Closed {
		t.Fatalf("reopen failed: %v", err)
	}
}

func TestDeleteContributionOnlyBeforeAnyPayment(t *testing.T) {
	f := newFixture(t)
	mistake, err := f.s.CreateContribution(f.ctx, f.class.ID, "Pomyłka", 100, nil, []int64{f.kid.ID})
	f.must(err)
	pay, err := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.must(err)
	f.must(f.s.DeleteContribution(f.ctx, f.class.ID, mistake))
	if len(f.views()) != 0 {
		t.Fatal("collection should be gone")
	}
	if sum, _ := f.s.Summary(f.ctx, f.class.ID); sum.OutstandingCount != 0 {
		t.Fatalf("obligations must go with it: %+v", sum)
	}
	if _, err := f.s.PaymentByID(f.ctx, f.class.ID, pay.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the payment that bundled only that obligation must be dropped, got %v", err)
	}

	real, err := f.s.CreateContribution(f.ctx, f.class.ID, "Prawdziwa", 5000, nil, []int64{f.kid.ID})
	f.must(err)
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 1000, f.views()[0].Items[0].SP.ID))
	if err := f.s.DeleteContribution(f.ctx, f.class.ID, real); !errors.Is(err, ErrConflict) {
		t.Fatalf("a collection with money in it must be closed, not deleted: %v", err)
	}
}

func TestReverseManualIncomeReopensObligationAndRebuildsLedger(t *testing.T) {
	f := newFixture(t)
	sps := f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 5000, sps[0].ID)) // against the obligation
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 2000, 0))         // general income
	if f.cashBalance() != 7000 {
		t.Fatalf("balance %d", f.cashBalance())
	}
	entries, err := f.s.ManualEntries(f.ctx, f.class.ID, 20)
	f.must(err)
	if len(entries) != 2 {
		t.Fatalf("entries %+v", entries)
	}
	var settle, general ManualEntry
	for _, e := range entries {
		if e.AmountMinor == 5000 {
			settle = e
		} else {
			general = e
		}
	}
	if settle.Description != "Jan Kowalski — Wycieczka" || general.Description != "Wpłata ogólna" {
		t.Fatalf("descriptions %q / %q", settle.Description, general.Description)
	}

	f.must(f.s.ReverseManualIncome(f.ctx, f.class.ID, settle.ID))
	if f.cashBalance() != 2000 {
		t.Fatalf("balance after reversal %d", f.cashBalance())
	}
	if sp := mustFindSP(f, "Wycieczka"); sp.Status != SPPending || sp.PaymentID != nil || sp.PaidAt != nil {
		t.Fatalf("obligation must reopen cleanly: %+v", sp)
	}
	// The reopened obligation can be settled again, by an open payment, exactly once.
	f.must(f.s.EnsureStudentPayment(f.ctx, f.class.ID, sps[0].ID, f.treas))
	if sp := mustFindSP(f, "Wycieczka"); sp.PaymentID == nil || sp.PaymentStatus != PaymentPending || sp.Code == "" {
		t.Fatalf("a fresh payment with a code was expected: %+v", sp)
	}
	entries, _ = f.s.ManualEntries(f.ctx, f.class.ID, 20)
	reversed := 0
	for _, e := range entries {
		if e.Reversed {
			reversed++
			if e.Description != "Jan Kowalski — Wycieczka" {
				t.Fatalf("a reversed entry keeps its description, got %q", e.Description)
			}
		}
	}
	if reversed != 1 {
		t.Fatalf("history must keep the reversed entry: %+v", entries)
	}
	if err := f.s.ReverseManualIncome(f.ctx, f.class.ID, settle.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("reversing twice must fail, got %v", err)
	}
	f.must(f.s.ReverseManualIncome(f.ctx, f.class.ID, general.ID))
	if f.cashBalance() != 0 {
		t.Fatalf("balance %d", f.cashBalance())
	}
}

func TestReverseRefusesBankPaymentsAndOtherClasses(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	pay, err := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.must(err)
	f.must(f.s.Transition(f.ctx, f.class.ID, pay.ID, "pay"))
	if err := f.s.ReverseManualIncome(f.ctx, f.class.ID, pay.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("online payments are refunded, not reversed: %v", err)
	}
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 100, 0))
	entries, _ := f.s.ManualEntries(f.ctx, f.class.ID, 10)
	other, err := f.s.CreateClass(f.ctx, "5B")
	f.must(err)
	if err := f.s.ReverseManualIncome(f.ctx, other.ID, entries[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another class must not reach the payment: %v", err)
	}
	if other2, _ := f.s.ManualEntries(f.ctx, other.ID, 10); len(other2) != 0 {
		t.Fatal("history must be class scoped")
	}
}

func TestMarkPaidByHandIsReversible(t *testing.T) {
	f := newFixture(t)
	sps := f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	f.must(f.s.MarkStudentPaymentPaid(f.ctx, f.class.ID, sps[0].ID, f.treas))
	entries, _ := f.s.ManualEntries(f.ctx, f.class.ID, 10)
	if len(entries) != 1 || entries[0].Description != "Jan Kowalski — Wycieczka" {
		t.Fatalf("cash settlement should appear in the manual history: %+v", entries)
	}
	f.must(f.s.ReverseManualIncome(f.ctx, f.class.ID, entries[0].ID))
	if f.cashBalance() != 0 || mustFindSP(f, "Wycieczka").Status != SPPending {
		t.Fatal("reversal must restore the open obligation and an empty till")
	}
}

func TestVoidExpenseKeepsHistoryAndFixesBalance(t *testing.T) {
	f := newFixture(t)
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 10000, 0))
	id, err := f.s.ManualExpense(f.ctx, f.class.ID, "Kwiaty", 25000, f.now) // typo: 250 instead of 25
	f.must(err)
	if f.cashBalance() != -15000 {
		t.Fatalf("balance %d", f.cashBalance())
	}
	f.must(f.s.VoidExpense(f.ctx, f.class.ID, id))
	f.must(f.s.VoidExpense(f.ctx, f.class.ID, id)) // idempotent
	if f.cashBalance() != 10000 {
		t.Fatalf("balance after void %d", f.cashBalance())
	}
	_, err = f.s.ManualExpense(f.ctx, f.class.ID, "Kwiaty", 2500, f.now)
	f.must(err)
	if f.cashBalance() != 7500 {
		t.Fatalf("balance after re-entry %d", f.cashBalance())
	}
	entries, _ := f.s.ManualEntries(f.ctx, f.class.ID, 10)
	var voided int
	for _, e := range entries {
		if e.Kind == "expense" && e.Reversed {
			voided++
		}
	}
	if voided != 1 || len(entries) != 3 {
		t.Fatalf("history should list income, voided and new expense: %+v", entries)
	}
	rows, _, _ := f.s.LedgerPage(f.ctx, f.class.ID, 1, 20)
	if len(rows) != 2 {
		t.Fatalf("voided expenses must leave the ledger, got %d rows", len(rows))
	}
	other, _ := f.s.CreateClass(f.ctx, "5B")
	if err := f.s.VoidExpense(f.ctx, other.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another class must not void it: %v", err)
	}
}

func TestIndividualAmountSurvivesCollectionEdits(t *testing.T) {
	f := newFixture(t)
	sibling := f.student(f.class.ID, "Ola", "Kowalska", f.parent)
	cid, err := f.s.CreateContribution(f.ctx, f.class.ID, "Wycieczka", 5000, nil, []int64{f.kid.ID, sibling.ID})
	f.must(err)
	pay, err := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.must(err)
	if pay.AmountMinor != 10000 {
		t.Fatalf("bundle %d", pay.AmountMinor)
	}
	var olaSP StudentPayment
	for _, it := range f.views()[0].Items {
		if it.Student.ID == sibling.ID {
			olaSP = *it.SP
		}
	}
	f.must(f.s.SetObligationAmount(f.ctx, f.class.ID, olaSP.ID, 3000)) // sibling discount
	got, _ := f.s.PaymentByID(f.ctx, f.class.ID, pay.ID)
	if got.AmountMinor != 8000 {
		t.Fatalf("the bundled payment must follow the discount, got %d", got.AmountMinor)
	}
	v := f.views()[0]
	if v.ExpectedTotal != 8000 {
		t.Fatalf("expected total must add the real obligations: %d", v.ExpectedTotal)
	}
	f.must(f.s.UpdateContribution(f.ctx, f.class.ID, cid, "Wycieczka", 6000, nil))
	for _, it := range f.views()[0].Items {
		want := int64(6000)
		if it.Student.ID == sibling.ID {
			want = 3000
			if !it.SP.Overridden {
				t.Fatal("the discount must stay flagged")
			}
		}
		if it.SP.AmountMinor != want {
			t.Fatalf("%s: %d, want %d", it.Student.FirstName, it.SP.AmountMinor, want)
		}
	}
	// A student who joins later pays the standard amount.
	late := f.student(f.class.ID, "Pi", "Późny")
	f.must(f.s.AddStudentToContribution(f.ctx, f.class.ID, cid, late.ID))
	for _, it := range f.views()[0].Items {
		if it.Student.ID == late.ID && it.SP.AmountMinor != 6000 {
			t.Fatalf("late student amount %d", it.SP.AmountMinor)
		}
	}
	f.must(f.s.ResetObligationAmount(f.ctx, f.class.ID, olaSP.ID))
	if sp := mustFindSPFor(f, sibling.ID); sp.AmountMinor != 6000 || sp.Overridden {
		t.Fatalf("reset: %+v", sp)
	}
}

func mustFindSPFor(f *fixture, studentID int64) StudentPayment {
	all, _ := f.s.StudentPaymentsByClass(f.ctx, f.class.ID)
	for _, sp := range all {
		if sp.StudentID == studentID {
			return sp
		}
	}
	f.t.Fatalf("no obligation for student %d", studentID)
	return StudentPayment{}
}

func TestIndividualAmountGuards(t *testing.T) {
	f := newFixture(t)
	sps := f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	other, _ := f.s.CreateClass(f.ctx, "5B")
	if err := f.s.SetObligationAmount(f.ctx, f.class.ID, sps[0].ID, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero must point to cancelling instead: %v", err)
	}
	if err := f.s.SetObligationAmount(f.ctx, other.ID, sps[0].ID, 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another class: %v", err)
	}
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 1000, sps[0].ID)) // part-paid
	if err := f.s.SetObligationAmount(f.ctx, f.class.ID, sps[0].ID, 100); !errors.Is(err, ErrConflict) {
		t.Fatalf("an obligation with payments must not change amount: %v", err)
	}
	if err := f.s.ResetObligationAmount(f.ctx, f.class.ID, sps[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("reset follows the same rule: %v", err)
	}
}
