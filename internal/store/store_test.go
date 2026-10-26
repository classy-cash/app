package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/classy-cash/app/internal/alior"
)

func notif(account, sender, title, amount string) *alior.Notification {
	return &alior.Notification{AccountNumber: account, Sender: sender, Title: title, Amount: amount}
}

func (f *fixture) ingest(id, account, sender, title, amount string) IngestResult {
	f.t.Helper()
	res, err := f.s.IngestBankMail(f.ctx, BankEventIn{ID: id, ReceivedAt: f.now, Recipient: "bank@x", MailFrom: "powiadomienia@alior.pl", Subject: "Uznanie rachunku 1 kwotą " + amount + " PLN", Body: "b"}, notif(account, sender, title, amount))
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func TestOnboardOnlyOnce(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.s.Onboard(f.ctx, OnboardInput{FirstName: "Eve", LastName: "X", Email: "eve@example.com", Password: "password123", ClassName: "Z"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second onboarding must be refused, got %v", err)
	}
	if !f.treas.IsAdmin || f.treas.ConfirmedAt == nil {
		t.Fatal("first user must be a confirmed administrator")
	}
	role, _ := f.s.RoleIn(f.ctx, f.treas, f.class.ID)
	if role != RoleTreasurer {
		t.Fatalf("role %q", role)
	}
}

func TestPermissionsAndClassIsolation(t *testing.T) {
	f := newFixture(t)
	other, err := f.s.CreateClass(f.ctx, "5B")
	f.must(err)
	tr2 := f.user("t2@example.com", "Druga Skarbnik")
	f.must(f.s.SetMember(f.ctx, other.ID, tr2.ID, RoleTreasurer))
	if f.s.CanManage(f.ctx, tr2, f.class.ID) {
		t.Fatal("treasurer of 5B must not manage 4A")
	}
	if !f.s.CanManage(f.ctx, tr2, other.ID) || !f.s.CanManage(f.ctx, f.treas, other.ID) {
		t.Fatal("treasurer manages own class; admin manages every class")
	}
	if f.s.CanManage(f.ctx, f.parent, f.class.ID) {
		t.Fatal("parent must not manage")
	}
	if err := f.s.SetMember(f.ctx, other.ID, f.parent.ID, RoleTreasurer); !errors.Is(err, ErrConflict) {
		t.Fatalf("one treasurer per class: %v", err)
	}
	// A student of another class cannot be pulled into a contribution of this class.
	foreign := f.student(other.ID, "Obca", "Uczennica")
	f.contribution(f.class.ID, "Wycieczka", 5000, f.kid, foreign)
	sps, _ := f.s.StudentPaymentsByClass(f.ctx, f.class.ID)
	if len(sps) != 1 || sps[0].StudentID != f.kid.ID {
		t.Fatalf("foreign student leaked into contribution: %+v", sps)
	}
	if _, err := f.s.StudentByID(f.ctx, f.class.ID, foreign.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-class student lookup must fail")
	}
	// Parents only see their own children's obligations.
	stranger := f.user("x@example.com", "Obcy Rodzic")
	own, _ := f.s.StudentPaymentsForParent(f.ctx, f.parent.ID, f.class.ID)
	none, _ := f.s.StudentPaymentsForParent(f.ctx, stranger.ID, f.class.ID)
	if len(own) != 1 || len(none) != 0 {
		t.Fatalf("family isolation broken: %d / %d", len(own), len(none))
	}
	if _, err := f.s.CreateFastPayment(f.ctx, stranger, f.class.ID, nil); !errors.Is(err, ErrNothingToPay) {
		t.Fatalf("stranger fast payment: %v", err)
	}
}

func TestSessionRevokedOnPasswordChange(t *testing.T) {
	f := newFixture(t)
	raw, err := f.s.CreateSession(f.ctx, f.treas)
	f.must(err)
	if _, _, err := f.s.SessionUser(f.ctx, raw); err != nil {
		t.Fatal(err)
	}
	f.must(f.s.SetPassword(f.ctx, f.treas.ID, "$argon2id$fake"))
	if _, _, err := f.s.SessionUser(f.ctx, raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session must be revoked after password reset: %v", err)
	}
}

func TestAuthTokensAreSingleUseAndExpire(t *testing.T) {
	f := newFixture(t)
	raw, _ := f.s.CreateAuthToken(f.ctx, f.parent.ID, TokenReset, time.Hour)
	if _, err := f.s.ConsumeAuthToken(f.ctx, raw, TokenLogin); err == nil {
		t.Fatal("token of another kind must not work")
	}
	if id, err := f.s.ConsumeAuthToken(f.ctx, raw, TokenReset); err != nil || id != f.parent.ID {
		t.Fatalf("first use: %v", err)
	}
	if _, err := f.s.ConsumeAuthToken(f.ctx, raw, TokenReset); err == nil {
		t.Fatal("token reuse must fail")
	}
	raw2, _ := f.s.CreateAuthToken(f.ctx, f.parent.ID, TokenLogin, time.Minute)
	f.now = f.now.Add(2 * time.Minute)
	if _, err := f.s.ConsumeAuthToken(f.ctx, raw2, TokenLogin); err == nil {
		t.Fatal("expired token must fail")
	}
}

func TestFastPaymentBundlesAndIsStable(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	f.contribution(f.class.ID, "Obiad", 2500, f.kid)
	p, err := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.must(err)
	if p.AmountMinor != 7500 || len(p.Code) != 4 || p.Status != PaymentPending || len(p.Items) != 2 {
		t.Fatalf("unexpected payment %+v", p)
	}
	again, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	if again.ID != p.ID || again.Code != p.Code {
		t.Fatal("reopening the dialog must not mint a new code")
	}
	one, err := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, []int64{p.Items[0].ID})
	f.must(err)
	if one.AmountMinor != p.Items[0].AmountMinor || one.ID == p.ID {
		t.Fatalf("selected-only payment wrong: %+v", one)
	}
	rest, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID)
	if rest.AmountMinor != 7500-one.AmountMinor || len(rest.Items) != 1 {
		t.Fatalf("original payment must shrink: %+v", rest)
	}
}

func TestMatchByCodeSettlesAndBooksCash(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 10000, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	res := f.ingest(strings.Repeat("a", 64), "91...6978", "ANNA KOWALSKA", "wycieczka "+strings.ToLower(p.Code), "100,00")
	if res.Outcome != OutcomeMatched {
		t.Fatalf("outcome %s", res.Outcome)
	}
	got, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID)
	if got.Status != PaymentPaid || got.Code != "" || got.PaidAt == nil || got.ReceivedMinor != 10000 {
		t.Fatalf("payment not settled: %+v", got)
	}
	if got.Items[0].Status != SPPaid {
		t.Fatal("obligation must be paid")
	}
	sum, _ := f.s.Summary(f.ctx, f.class.ID)
	if sum.TotalMinor != 10000 || sum.OutstandingMinor != 0 || sum.MonthCollected != 10000 {
		t.Fatalf("summary %+v", sum)
	}
	mails, _ := f.s.DueMail(f.ctx, 10)
	if len(mails) != 2 {
		t.Fatalf("expected payer + treasurer mail, got %d", len(mails))
	}
	// Paid codes are retired: a later transfer quoting the same code does not match.
	res2 := f.ingest(strings.Repeat("b", 64), "91...6978", "ANNA KOWALSKA", p.Code, "100,00")
	if res2.Outcome == OutcomeMatched {
		t.Fatal("a retired code must not match again")
	}
}

func TestPartialThenCompletePayment(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 10000, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.ingest(strings.Repeat("1", 64), "acc", "ANNA", p.Code, "40,00")
	mid, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID)
	if mid.Status != PaymentPending || mid.ReceivedMinor != 4000 || mid.Items[0].Status != SPPartial {
		t.Fatalf("partial state wrong: %+v", mid)
	}
	f.ingest(strings.Repeat("2", 64), "acc", "ANNA", p.Code, "60,00")
	end, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID)
	if end.Status != PaymentPaid || end.Items[0].Status != SPPaid || end.ReceivedMinor != 10000 {
		t.Fatalf("complete state wrong: %+v", end)
	}
}

func TestWebhookRedeliveryIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 10000, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	id := strings.Repeat("c", 64)
	a := f.ingest(id, "acc", "ANNA", p.Code, "100,00")
	b := f.ingest(id, "acc", "ANNA", p.Code, "100,00")
	if a.Outcome != OutcomeMatched || b.Outcome != OutcomeDuplicate || b.TransferID != a.TransferID {
		t.Fatalf("redelivery: %+v %+v", a, b)
	}
	ts, _ := f.s.Transfers(f.ctx, f.class.ID)
	sum, _ := f.s.Summary(f.ctx, f.class.ID)
	if len(ts) != 1 || sum.TotalMinor != 10000 {
		t.Fatalf("financial effect applied twice: %d transfers, cash %d", len(ts), sum.TotalMinor)
	}
}

func TestDuplicateLookingTransferGoesToReview(t *testing.T) {
	f := newFixture(t)
	f.ingest(strings.Repeat("d", 64), "acc", "ANNA", "bez kodu", "50,00")
	res := f.ingest(strings.Repeat("e", 64), "acc", "ANNA", "bez kodu", "50,00")
	if res.Outcome != OutcomeReview {
		t.Fatalf("outcome %s", res.Outcome)
	}
	// Held transfers are not auto-matched even by the periodic retry.
	if n, _ := f.s.RematchRecent(f.ctx); n != 0 {
		t.Fatal("review transfers must not be rematched")
	}
	f.must(f.s.ReviewTransfer(f.ctx, f.class.ID, res.TransferID, false))
	list, _ := f.s.Transfers(f.ctx, f.class.ID)
	if len(list) != 1 {
		t.Fatalf("dismissed duplicate must disappear, have %d", len(list))
	}
}

func TestUnrecognizedEventIsQuarantinedAndReplayable(t *testing.T) {
	f := newFixture(t)
	res, err := f.s.IngestBankMail(f.ctx, BankEventIn{ID: strings.Repeat("f", 64), ReceivedAt: f.now, Recipient: "r", MailFrom: "m", Subject: "Newsletter", Body: "x"}, nil)
	f.must(err)
	if res.Outcome != OutcomeUnrecognized {
		t.Fatal(res.Outcome)
	}
	evs, _ := f.s.BankEvents(f.ctx, "unrecognized", 10)
	if len(evs) != 1 {
		t.Fatal("event must be visible for review")
	}
	r, err := f.s.ReplayEvent(f.ctx, evs[0].ID, func(subject, body string) (alior.Notification, bool) {
		return alior.Notification{AccountNumber: "a", Sender: "S", Title: "T", Amount: "10,00"}, true
	})
	f.must(err)
	if r.Outcome != OutcomeUnmatched || r.TransferID == 0 {
		t.Fatalf("replay %+v", r)
	}
	r2, _ := f.s.ReplayEvent(f.ctx, evs[0].ID, func(string, string) (alior.Notification, bool) { return alior.Notification{}, false })
	if r2.Outcome != OutcomeDuplicate {
		t.Fatal("replay of a processed event must be idempotent")
	}
}

// Ports TransferPaymentMatcherTest: history ranking and the single-candidate rule.
func (f *fixture) historyScenario() (hist, pending Payment, kid2 Student) {
	f.contribution(f.class.ID, "Wycieczka", 10000, f.kid)
	hist, _ = f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.ingest(strings.Repeat("9", 64), "PL61109010140000071219812874", "Anna Kowalska", hist.Code, "100,00")
	f.now = f.now.Add(time.Hour)
	f.contribution(f.class.ID, "Obiad", 10000, f.kid)
	pending, _ = f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	return hist, pending, f.kid
}

func TestHistoricalMatchWhenSingleCandidate(t *testing.T) {
	f := newFixture(t)
	_, pending, _ := f.historyScenario()
	res := f.ingest(strings.Repeat("8", 64), "PL61109010140000071219812874", "Anna Kowalska", "Brak kodu", "100,00")
	if res.Outcome != OutcomeMatched {
		t.Fatalf("expected history match, got %s", res.Outcome)
	}
	got, _ := f.s.PaymentByID(f.ctx, f.class.ID, pending.ID)
	if got.Status != PaymentPaid {
		t.Fatal("pending payment should be settled by history match")
	}
}

func TestHistoricalMatchRefusesAmbiguity(t *testing.T) {
	f := newFixture(t)
	f.historyScenario()
	f.contribution(f.class.ID, "Książki", 10000, f.kid)
	f.must(f.s.CancelStudentPayment(f.ctx, f.class.ID, mustFindSP(f, "Obiad").ID)) // keep state simple
	f.contribution(f.class.ID, "Obiad2", 10000, f.kid)
	// Two pending 100 zł payments for the same student → ambiguous.
	a, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, []int64{mustFindSP(f, "Książki").ID})
	b, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, []int64{mustFindSP(f, "Obiad2").ID})
	if a.ID == b.ID {
		t.Fatal("setup")
	}
	res := f.ingest(strings.Repeat("7", 64), "PL61109010140000071219812874", "Anna Kowalska", "bez kodu", "100,00")
	if res.Outcome != OutcomeUnmatched {
		t.Fatalf("ambiguous history must not auto-match, got %s", res.Outcome)
	}
}

func mustFindSP(f *fixture, label string) StudentPayment {
	all, _ := f.s.StudentPaymentsByClass(f.ctx, f.class.ID)
	for _, sp := range all {
		if sp.Label == label {
			return sp
		}
	}
	f.t.Fatalf("no obligation %q", label)
	return StudentPayment{}
}

func TestSuggestionsRankHistoricalStudentFirst(t *testing.T) {
	f := newFixture(t)
	_, pending, _ := f.historyScenario()
	other := f.user("inny@example.com", "Inny Rodzic")
	kid2 := f.student(f.class.ID, "Ola", "Nowak", other)
	f.contribution(f.class.ID, "Zeszyty", 10000, kid2)
	otherPay, _ := f.s.CreateFastPayment(f.ctx, other, f.class.ID, nil)
	res := f.ingest(strings.Repeat("6", 64), "PL61109010140000071219812874", "Anna Kowalska", "Brak kodu", "100,00")
	if res.Outcome == OutcomeMatched { // history would auto-match; force the manual path
		f.must(f.s.UnassignTransfer(f.ctx, f.class.ID, res.TransferID))
	}
	sg, top, err := f.s.Suggestions(f.ctx, f.class.ID, res.TransferID, "")
	f.must(err)
	if len(sg) < 2 || sg[0].Payment.ID != pending.ID || sg[0].Hint == nil || !strings.Contains(sg[0].Hint.Description(), "Jan Kowalski") {
		t.Fatalf("history candidate must rank first: %+v", sg)
	}
	if top == nil || otherPay.ID == sg[0].Payment.ID {
		t.Fatal("top hint missing")
	}
}

func TestManualAssignReassignUnassign(t *testing.T) {
	f := newFixture(t)
	sps := f.contribution(f.class.ID, "Wycieczka", 10000, f.kid)
	p1, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	other := f.user("o@example.com", "Olga")
	kid2 := f.student(f.class.ID, "Ola", "Nowak", other)
	f.contribution(f.class.ID, "Obiad", 10000, kid2)
	p2, _ := f.s.CreateFastPayment(f.ctx, other, f.class.ID, nil)
	res := f.ingest(strings.Repeat("5", 64), "acc", "NIKT", "przelew", "100,00")
	if res.Outcome != OutcomeUnmatched {
		t.Fatal(res.Outcome)
	}
	f.must(f.s.AssignTransfer(f.ctx, f.class.ID, res.TransferID, p1.ID))
	if g, _ := f.s.PaymentByID(f.ctx, f.class.ID, p1.ID); g.Status != PaymentPaid {
		t.Fatal("manual assignment must settle")
	}
	// Reassign to the other payment: the first one reopens, the second settles.
	f.must(f.s.AssignTransfer(f.ctx, f.class.ID, res.TransferID, p2.ID))
	g1, _ := f.s.PaymentByID(f.ctx, f.class.ID, p1.ID)
	g2, _ := f.s.PaymentByID(f.ctx, f.class.ID, p2.ID)
	if g1.Status != PaymentPending || g1.Code == "" || g1.Items[0].Status != SPPending || g2.Status != PaymentPaid {
		t.Fatalf("reassign wrong: %+v / %+v", g1, g2)
	}
	if sum, _ := f.s.Summary(f.ctx, f.class.ID); sum.TotalMinor != 10000 {
		t.Fatalf("ledger must follow the reassignment, got %d", sum.TotalMinor)
	}
	f.must(f.s.UnassignTransfer(f.ctx, f.class.ID, res.TransferID))
	if sum, _ := f.s.Summary(f.ctx, f.class.ID); sum.TotalMinor != 0 {
		t.Fatalf("ledger must be empty after unassign, got %d", sum.TotalMinor)
	}
	_ = sps
}

func TestAssignTransferToObligationWithoutPayment(t *testing.T) {
	f := newFixture(t)
	sps := f.contribution(f.class.ID, "Wycieczka", 10000, f.kid)
	res := f.ingest(strings.Repeat("4", 64), "acc", "NIKT", "przelew", "100,00")
	f.must(f.s.AssignTransferToObligation(f.ctx, f.class.ID, res.TransferID, sps[0].ID, f.treas))
	got, _ := f.s.StudentPaymentsByClass(f.ctx, f.class.ID)
	if got[0].Status != SPPaid || got[0].PaymentID == nil {
		t.Fatalf("obligation not settled: %+v", got[0])
	}
}

func TestLedgerRunningBalanceAndManualMoney(t *testing.T) {
	f := newFixture(t)
	sps := f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 5000, sps[0].ID))
	f.now = f.now.Add(time.Hour)
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 2000, 0)) // general income
	f.now = f.now.Add(time.Hour)
	_, err := f.s.ManualExpense(f.ctx, f.class.ID, "Kwiaty", 1500, f.now)
	f.must(err)
	rows, more, err := f.s.LedgerPage(f.ctx, f.class.ID, 1, 10)
	f.must(err)
	if more || len(rows) != 3 || rows[0].Kind != "expense" || rows[0].BalanceAfter != 5500 || rows[1].Description != "Wpłata ogólna" || rows[2].StudentName != "Jan Kowalski" {
		t.Fatalf("ledger rows wrong: %+v", rows)
	}
	if rows[2].Method != "Ręcznie" || rows[0].Method != "Wydatek" {
		t.Fatal("methods wrong")
	}
	if _, err := f.s.ManualExpense(f.ctx, f.class.ID, "", 100, f.now); !errors.Is(err, ErrInvalid) {
		t.Fatal("expense description is required")
	}
	// Rebuilding is idempotent.
	f.must(f.s.RecalculateLedger(f.ctx))
	if sum, _ := f.s.Summary(f.ctx, f.class.ID); sum.TotalMinor != 5500 {
		t.Fatalf("total %d", sum.TotalMinor)
	}
}

func TestPartialCashLeavesObligationOpen(t *testing.T) {
	f := newFixture(t)
	sps := f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	f.must(f.s.ManualIncome(f.ctx, f.class.ID, f.treas, 2000, sps[0].ID))
	got := mustFindSP(f, "Wycieczka")
	if got.Status != SPPartial {
		t.Fatalf("status %s", got.Status)
	}
	if sum, _ := f.s.Summary(f.ctx, f.class.ID); sum.TotalMinor != 2000 || sum.OutstandingCount != 1 {
		t.Fatalf("summary %+v", sum)
	}
}

func TestPaymentStateMachine(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	if err := f.s.Transition(f.ctx, f.class.ID, p.ID, "refund"); !errors.Is(err, ErrConflict) {
		t.Fatal("cannot refund a pending payment")
	}
	f.now = f.now.Add(25 * time.Hour)
	if n, _ := f.s.ExpirePending(f.ctx, 24*time.Hour); n != 1 {
		t.Fatalf("expired %d", n)
	}
	f.must(f.s.Transition(f.ctx, f.class.ID, p.ID, "pay")) // expired payments stay payable
	f.must(f.s.Transition(f.ctx, f.class.ID, p.ID, "refund"))
	g, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID)
	if g.Status != PaymentRefunded || g.Items[0].Status != SPPending {
		t.Fatalf("refund state %+v", g)
	}
	if sum, _ := f.s.Summary(f.ctx, f.class.ID); sum.TotalMinor != 0 {
		t.Fatal("refund must leave the ledger")
	}
	f.contribution(f.class.ID, "Obiad", 1000, f.kid)
	q, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, []int64{mustFindSP(f, "Obiad").ID})
	f.must(f.s.Transition(f.ctx, f.class.ID, q.ID, "fail"))
	if g, _ := f.s.PaymentByID(f.ctx, f.class.ID, q.ID); g.Status != PaymentFailed || g.Code != "" {
		t.Fatalf("fail state %+v", g)
	}
}

func TestCancelObligation(t *testing.T) {
	f := newFixture(t)
	sps := f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.must(f.s.CancelStudentPayment(f.ctx, f.class.ID, sps[0].ID))
	if _, err := f.s.PaymentByID(f.ctx, f.class.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("empty unpaid payment must be removed")
	}
	views, _ := f.s.ContributionViews(f.ctx, f.class.ID)
	if len(views) != 1 || len(views[0].Items) != 0 {
		t.Fatalf("student must leave the contribution: %+v", views)
	}
	sps2 := f.contribution(f.class.ID, "Obiad", 1000, f.kid)
	f.must(f.s.MarkStudentPaymentPaid(f.ctx, f.class.ID, sps2[0].ID, f.treas))
	if err := f.s.CancelStudentPayment(f.ctx, f.class.ID, sps2[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatal("paid obligations cannot be cancelled")
	}
}

func TestMergeStudents(t *testing.T) {
	f := newFixture(t)
	second := f.user("ojciec@example.com", "Piotr Kowalski")
	dup := f.student(f.class.ID, "Janek", "Kowalski", second)
	f.contribution(f.class.ID, "Wycieczka", 5000, f.kid, dup)
	f.contribution(f.class.ID, "Obiad", 1000, dup)
	f.must(f.s.MergeStudents(f.ctx, f.class.ID, f.kid.ID, dup.ID))
	roster, _ := f.s.ListStudents(f.ctx, f.class.ID)
	if len(roster) != 1 || roster[0].ID != f.kid.ID || len(roster[0].Parents) != 2 {
		t.Fatalf("roster after merge: %+v", roster)
	}
	sps, _ := f.s.StudentPaymentsByClass(f.ctx, f.class.ID)
	if len(sps) != 2 {
		t.Fatalf("duplicate obligation must collapse: got %d", len(sps))
	}
	for _, sp := range sps {
		if sp.StudentID != f.kid.ID {
			t.Fatal("obligation not moved")
		}
	}
	if err := f.s.MergeStudents(f.ctx, f.class.ID, f.kid.ID, f.kid.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("cannot merge a student into itself")
	}
}

func TestOutboxIdempotencyAndBackoff(t *testing.T) {
	f := newFixture(t)
	m := struct{ k string }{"same"}
	for i := 0; i < 3; i++ {
		f.must(f.s.Enqueue(f.ctx, mailMsg(m.k)))
	}
	due, _ := f.s.DueMail(f.ctx, 10)
	if len(due) != 1 {
		t.Fatalf("idempotency key must dedupe, got %d", len(due))
	}
	f.must(f.s.MarkMailError(f.ctx, due[0].ID, errors.New("boom"), false))
	if again, _ := f.s.DueMail(f.ctx, 10); len(again) != 0 {
		t.Fatal("failed attempt must back off")
	}
	f.now = f.now.Add(time.Minute)
	if again, _ := f.s.DueMail(f.ctx, 10); len(again) != 1 {
		t.Fatal("retry should be due after the backoff")
	}
	f.must(f.s.MarkMailError(f.ctx, due[0].ID, errors.New("rejected"), true))
	st, _ := f.s.OutboxStats(f.ctx)
	if st.Failed != 1 || st.Pending != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestParentRegistrationFlagAndSettings(t *testing.T) {
	f := newFixture(t)
	if !f.s.ParentRegistrationEnabled(f.ctx) {
		t.Fatal("onboarding enables parent registration (legacy)")
	}
	f.must(f.s.SetSetting(f.ctx, "parent_registration_enabled", "0"))
	if f.s.ParentRegistrationEnabled(f.ctx) {
		t.Fatal("flag not persisted")
	}
	if !strings.HasPrefix(f.s.AppName(f.ctx), "Classy Cash") {
		t.Fatal("app name")
	}
}
