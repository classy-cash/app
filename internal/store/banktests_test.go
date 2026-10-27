package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/classy-cash/app/internal/alior"
)

func (f *fixture) bankTest(minor int64) BankTest {
	f.t.Helper()
	bt, err := f.s.CreateBankTest(f.ctx, f.class.ID, f.treas, minor)
	f.must(err)
	return bt
}

// deliver feeds one notification through the real ingest path. parse=false models a mail the parser rejected.
func (f *fixture) deliver(id, title, amount string, parse bool) IngestResult {
	f.t.Helper()
	ev := BankEventIn{ID: id, ReceivedAt: f.now, Recipient: "bank@x", MailFrom: "powiadomienia@alior.pl",
		Subject: "Uznanie rachunku 1 kwotą " + amount + " PLN", Body: "<br/>Nadawca: JAN<br/>Tytuł zlecenia: " + title + "<br/>Saldo: 1,00 PLN"}
	n := notif("1", "JAN", title, amount)
	if !parse {
		n = nil
	}
	res, err := f.s.IngestBankMail(f.ctx, ev, n)
	f.must(err)
	return res
}

func evid(n int) string { return fmt.Sprintf("%064x", n) }

func (f *fixture) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.s.DB.QueryRowContext(f.ctx, q, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// accounting is everything a notification could change that a diagnostic must leave alone.
type accounting struct{ transfers, ledger, paidPayments, paidObligations, outbox, balance int }

func (f *fixture) accounting() accounting {
	return accounting{
		f.count(`SELECT COUNT(*) FROM transfers`), f.count(`SELECT COUNT(*) FROM ledger`),
		f.count(`SELECT COUNT(*) FROM payments WHERE status = 'paid'`), f.count(`SELECT COUNT(*) FROM student_payments WHERE status = 'paid'`),
		f.count(`SELECT COUNT(*) FROM outbox`), int(f.cashBalance()),
	}
}

func TestBankTestMarkerShape(t *testing.T) {
	f := newFixture(t)
	bt := f.bankTest(100)
	if !strings.HasPrefix(bt.Marker, BankTestPrefix) || len(bt.Marker) != 14 {
		t.Fatalf("marker %q", bt.Marker)
	}
	if strings.ContainsAny(bt.Marker[len(BankTestPrefix):], "01OIL") {
		t.Fatalf("ambiguous characters in %q", bt.Marker)
	}
	if !strings.HasPrefix(bt.Title(), "CCTEST-") || strings.ReplaceAll(bt.Title(), "-", "") != bt.Marker {
		t.Fatalf("title %q", bt.Title())
	}
	if other := f.bankTest(100); other.Marker == bt.Marker {
		t.Fatal("markers must be unique")
	}
	if bt.State(f.now) != BankTestPending || !bt.Open(f.now) {
		t.Fatal("new reference is pending")
	}
}

func TestBankTestValidationAndLimits(t *testing.T) {
	f := newFixture(t)
	for _, bad := range []int64{0, -5, MaxBankTestMinor + 1} {
		if _, err := f.s.CreateBankTest(f.ctx, f.class.ID, f.treas, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("amount %d: %v", bad, err)
		}
	}
	for i := 0; i < MaxOpenBankTests; i++ {
		f.bankTest(100)
	}
	if _, err := f.s.CreateBankTest(f.ctx, f.class.ID, f.treas, 100); !errors.Is(err, ErrConflict) {
		t.Fatalf("limit of open references: %v", err)
	}
	f.now = f.now.Add(BankTestTTL + time.Minute) // expired ones no longer count as open
	f.bankTest(100)
}

// The decisive case: the title carries a LIVE payment code and the exact amount. Normal logic would settle the
// payment; the marker must make the whole delivery inert.
func TestBankTestNeverSettlesOrBooksAnything(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 1000, f.kid)
	p, err := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.must(err)
	bt := f.bankTest(1000)
	before := f.accounting()

	res := f.deliver(evid(1), p.Code+" "+bt.Title(), "10,00", true)
	if res.Outcome != OutcomeTest || res.TransferID != 0 {
		t.Fatalf("outcome %+v", res)
	}
	if after := f.accounting(); after != before {
		t.Fatalf("accounting changed: %+v -> %+v", before, after)
	}
	pay, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID)
	if pay.Status != PaymentPending {
		t.Fatalf("payment settled by a diagnostic: %s", pay.Status)
	}
	got, _ := f.s.BankTestByID(f.ctx, f.class.ID, bt.ID)
	if got.State(f.now) != BankTestSuccess || got.Hits != 1 || !got.Parsed || got.ObservedMinor == nil || *got.ObservedMinor != 1000 || got.AmountDiffers() {
		t.Fatalf("test row %+v", got)
	}
	var status, reason string
	f.must(f.s.DB.QueryRowContext(f.ctx, `SELECT status, reason FROM bank_events WHERE id = ?`, evid(1)).Scan(&status, &reason))
	if status != EventTest || reason == "" {
		t.Fatalf("event %q %q", status, reason)
	}
	if last := f.count(`SELECT COUNT(*) FROM bank_events WHERE status = 'parsed'`); last != 0 {
		t.Fatal("a diagnostic must not count as a parsed import")
	}
}

func TestBankTestRetriesDuplicatesAndLaterDeliveriesStayInert(t *testing.T) {
	f := newFixture(t)
	bt := f.bankTest(100)
	before := f.accounting()
	f.deliver(evid(1), bt.Title(), "1,00", true)
	if res := f.deliver(evid(1), bt.Title(), "1,00", true); res.Outcome != OutcomeDuplicate {
		t.Fatalf("same id is a transport duplicate: %+v", res)
	}
	got, _ := f.s.BankTestByID(f.ctx, f.class.ID, bt.ID)
	if got.Hits != 1 {
		t.Fatalf("redelivery of one id counted twice: %d", got.Hits)
	}
	// Same marker under new ids (a retry by the bank, a second mail): still no transfer, still swallowed.
	for i := 2; i <= 4; i++ {
		if res := f.deliver(evid(i), strings.ToLower(strings.ReplaceAll(bt.Title(), "-", " ")), "1,00", true); res.Outcome != OutcomeTest {
			t.Fatalf("delivery %d: %+v", i, res)
		}
	}
	got, _ = f.s.BankTestByID(f.ctx, f.class.ID, bt.ID)
	if got.Hits != 4 || got.State(f.now) != BankTestSuccess || got.EventID != evid(1) {
		t.Fatalf("hits=%d state=%s first=%s", got.Hits, got.State(f.now), got.EventID)
	}
	if after := f.accounting(); after != before {
		t.Fatalf("accounting changed: %+v -> %+v", before, after)
	}
}

func TestBankTestAfterExpiryIsInterceptedButNotASuccess(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 100, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	bt := f.bankTest(100)
	before := f.accounting()
	f.now = f.now.Add(BankTestTTL + time.Hour)
	if res := f.deliver(evid(1), p.Code+" "+bt.Marker, "1,00", true); res.Outcome != OutcomeTest {
		t.Fatalf("late delivery must still be swallowed: %+v", res)
	}
	got, _ := f.s.BankTestByID(f.ctx, f.class.ID, bt.ID)
	if got.State(f.now) != BankTestExpired || got.Stored != "pending" || !got.Seen() || !got.Parsed {
		t.Fatalf("late: %+v state=%s", got, got.State(f.now))
	}
	if after := f.accounting(); after != before {
		t.Fatalf("accounting changed after expiry: %+v -> %+v", before, after)
	}
	if pay, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID); pay.Status != PaymentPending {
		t.Fatal("payment settled by a late diagnostic")
	}
}

func TestBankTestCancelledStillSwallowsAndSuccessCannotBeCancelled(t *testing.T) {
	f := newFixture(t)
	a, b := f.bankTest(100), f.bankTest(100)
	f.must(f.s.CancelBankTest(f.ctx, f.class.ID, a.ID))
	if err := f.s.CancelBankTest(f.ctx, f.class.ID, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("double cancel: %v", err)
	}
	before := f.accounting()
	if res := f.deliver(evid(1), a.Title(), "1,00", true); res.Outcome != OutcomeTest {
		t.Fatalf("cancelled marker must be swallowed: %+v", res)
	}
	got, _ := f.s.BankTestByID(f.ctx, f.class.ID, a.ID)
	if got.State(f.now) != BankTestCancelled || !got.Seen() {
		t.Fatalf("cancelled+late: %+v", got)
	}
	f.deliver(evid(2), b.Title(), "1,00", true)
	if err := f.s.CancelBankTest(f.ctx, f.class.ID, b.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelling a success: %v", err)
	}
	if got, _ := f.s.BankTestByID(f.ctx, f.class.ID, b.ID); got.State(f.now) != BankTestSuccess {
		t.Fatal("success was rewritten")
	}
	if after := f.accounting(); after != before {
		t.Fatalf("accounting changed: %+v -> %+v", before, after)
	}
}

func TestBankTestUnreadableNotificationThenReadableOne(t *testing.T) {
	f := newFixture(t)
	bt := f.bankTest(100)
	before := f.accounting()
	if res := f.deliver(evid(1), bt.Title(), "1,00", false); res.Outcome != OutcomeTest {
		t.Fatalf("%+v", res)
	}
	got, _ := f.s.BankTestByID(f.ctx, f.class.ID, bt.ID)
	if got.State(f.now) != BankTestPending || !got.Seen() || got.Parsed {
		t.Fatalf("unparsed hit must not be a success: %+v", got)
	}
	var status string
	f.must(f.s.DB.QueryRowContext(f.ctx, `SELECT status FROM bank_events WHERE id = ?`, evid(1)).Scan(&status))
	if status != EventTestUnparsed {
		t.Fatalf("status %q", status)
	}
	if _, err := f.s.ReplayEvent(f.ctx, evid(1), nil); err != nil { // never re-processable: it is not 'unrecognized'
		t.Fatal(err)
	}
	f.deliver(evid(2), bt.Title(), "1,00", true)
	got, _ = f.s.BankTestByID(f.ctx, f.class.ID, bt.ID)
	if got.State(f.now) != BankTestSuccess || got.Hits != 2 || got.EventID != evid(2) {
		t.Fatalf("after a readable one: %+v", got)
	}
	if after := f.accounting(); after != before {
		t.Fatalf("accounting changed: %+v -> %+v", before, after)
	}
}

func TestBankTestDifferentAmountIsFlaggedNotBooked(t *testing.T) {
	f := newFixture(t)
	bt := f.bankTest(100)
	before := f.accounting()
	f.deliver(evid(1), bt.Title(), "7,00", true)
	got, _ := f.s.BankTestByID(f.ctx, f.class.ID, bt.ID)
	if got.State(f.now) != BankTestSuccess || !got.AmountDiffers() {
		t.Fatalf("%+v", got)
	}
	if after := f.accounting(); after != before {
		t.Fatalf("accounting changed: %+v -> %+v", before, after)
	}
}

// An unrecognised event stored before a reference existed must not become a booking when it is replayed.
func TestBankTestReplayOfStoredEventIsSwallowed(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 100, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	bt := f.bankTest(100)
	// Stored as unrecognised (the parser had failed), then replayed with a parser that now succeeds.
	ev := BankEventIn{ID: evid(9), ReceivedAt: f.now, Recipient: "bank@x", MailFrom: "m", Subject: "Newsletter", Body: "x"}
	if _, err := f.s.IngestBankMail(f.ctx, ev, nil); err != nil {
		t.Fatal(err)
	}
	before := f.accounting()
	res, err := f.s.ReplayEvent(f.ctx, evid(9), func(_, _ string) (n alior.Notification, ok bool) {
		return *notif("1", "JAN", p.Code+" "+bt.Title(), "1,00"), true
	})
	f.must(err)
	if res.Outcome != OutcomeTest {
		t.Fatalf("replay: %+v", res)
	}
	if after := f.accounting(); after != before {
		t.Fatalf("accounting changed: %+v -> %+v", before, after)
	}
	if pay, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID); pay.Status != PaymentPending {
		t.Fatal("replay settled a payment")
	}
}

func TestBankTestLookalikeAndNormalBehaviourUnchanged(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 5000, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	f.bankTest(100)

	// A marker-shaped string that was never issued is an ordinary (unmatched) transfer.
	if res := f.deliver(evid(1), "CCTEST-AAAA-AAAA", "1,00", true); res.Outcome != OutcomeUnmatched || res.TransferID == 0 {
		t.Fatalf("unknown marker: %+v", res)
	}
	// An ordinary payment still settles by its code…
	if res := f.deliver(evid(2), p.Code, "50,00", true); res.Outcome != OutcomeMatched {
		t.Fatalf("normal payment: %+v", res)
	}
	if pay, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID); pay.Status != PaymentPaid {
		t.Fatalf("payment %s", pay.Status)
	}
	// …and a text without any marker is unrecognised when unparseable.
	if res := f.deliver(evid(3), "x", "1,00", false); res.Outcome != OutcomeUnrecognized {
		t.Fatalf("unparseable: %+v", res)
	}
}

func TestBankTestClassIsolation(t *testing.T) {
	f := newFixture(t)
	other, err := f.s.CreateClass(f.ctx, "5B")
	f.must(err)
	bt := f.bankTest(100)
	if _, err := f.s.BankTestByID(f.ctx, other.ID, bt.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	if err := f.s.CancelBankTest(f.ctx, other.ID, bt.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign cancel: %v", err)
	}
	if list, _ := f.s.BankTests(f.ctx, other.ID, 10); len(list) != 0 {
		t.Fatal("foreign list")
	}
	// The interception is global on purpose: another class's marker is still swallowed wherever it arrives.
	if res := f.deliver(evid(1), bt.Title(), "1,00", true); res.Outcome != OutcomeTest {
		t.Fatalf("%+v", res)
	}
}

// Deliveries racing each other and a cancellation: whatever order SQLite serialises them in, nothing is booked,
// every distinct delivery is counted once and the state is either success or cancelled — never inconsistent.
func TestBankTestConcurrentDeliveriesAndCancel(t *testing.T) {
	f := newFixture(t)
	f.contribution(f.class.ID, "Wycieczka", 100, f.kid)
	p, _ := f.s.CreateFastPayment(f.ctx, f.parent, f.class.ID, nil)
	bt := f.bankTest(100)
	before := f.accounting()
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n+1)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := BankEventIn{ID: evid(100 + i%5), ReceivedAt: f.now, Recipient: "r", MailFrom: "m", Subject: "Uznanie", Body: bt.Title()} // ids repeat: 5 distinct
			_, err := f.s.IngestBankMail(f.ctx, ev, notif("1", "JAN", p.Code+" "+bt.Title(), "1,00"))
			errs <- err
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); err := f.s.CancelBankTest(f.ctx, f.class.ID, bt.ID); errs <- err }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	got, _ := f.s.BankTestByID(f.ctx, f.class.ID, bt.ID)
	st := got.State(f.now)
	if got.Hits != 5 || (st != BankTestSuccess && st != BankTestCancelled) {
		t.Fatalf("hits=%d state=%s", got.Hits, st)
	}
	if after := f.accounting(); after != before {
		t.Fatalf("accounting changed under concurrency: %+v -> %+v", before, after)
	}
	if pay, _ := f.s.PaymentByID(f.ctx, f.class.ID, p.ID); pay.Status != PaymentPending {
		t.Fatal("payment settled under concurrency")
	}
}
