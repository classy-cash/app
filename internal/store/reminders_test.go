package store

import (
	"strings"
	"testing"
	"time"
)

func (f *fixture) remindersTo(email string) []OutboxMail {
	f.t.Helper()
	all, _ := f.s.RecentMail(f.ctx, 100)
	var out []OutboxMail
	for _, m := range all {
		if m.ToEmail == email && strings.HasPrefix(m.Key, "remind-") {
			out = append(out, m)
		}
	}
	return out
}

func TestReminderGroupsChildrenPerParentAndRespectsScope(t *testing.T) {
	f := newFixture(t)
	ola := f.student(f.class.ID, "Ola", "Kowalska", f.parent) // second child of the same parent
	other := f.user("piotr@example.com", "Piotr Nowak")
	f.student(f.class.ID, "Igor", "Nowak", other)
	f.student(f.class.ID, "Sierota", "Bez") // no parent: nobody to remind
	past := f.now.Add(-48 * time.Hour)
	future := f.now.Add(72 * time.Hour)
	all := []int64{f.kid.ID, ola.ID}
	_, err := f.s.CreateContribution(f.ctx, f.class.ID, "Stara", 3000, &past, all)
	f.must(err)
	_, err = f.s.CreateContribution(f.ctx, f.class.ID, "Przyszła", 4000, &future, all)
	f.must(err)

	res, err := f.s.RemindParents(f.ctx, f.class.ID, 0, true, f.treas)
	f.must(err)
	if res.Sent != 1 || res.Skipped != 0 {
		t.Fatalf("overdue only: %+v", res)
	}
	m := f.remindersTo("anna@example.com")
	if len(m) != 1 {
		t.Fatalf("want one mail for Anna, got %d", len(m))
	}
	body := m[0].Text
	if !strings.Contains(body, "Stara") || strings.Contains(body, "Przyszła") {
		t.Fatalf("overdue scope must list only the late collection:\n%s", body)
	}
	if !strings.Contains(body, "Jan Kowalski") || !strings.Contains(body, "Ola Kowalska") || !strings.Contains(body, "Do zapłaty razem: 60") {
		t.Fatalf("both children and the total expected:\n%s", body)
	}
	if len(f.remindersTo("piotr@example.com")) != 0 {
		t.Fatal("a parent whose child owes nothing must not be reminded")
	}
	if !strings.Contains(m[0].Subject, "4A") || !strings.Contains(body, "Teresa Skarbnik") || !strings.Contains(body, f.s.BaseURL+"/parent") {
		t.Fatalf("subject/treasurer/link missing:\n%s %s", m[0].Subject, body)
	}
}

func TestReminderCooldownDoesNotSpamAndCanBeScopedToOneCollection(t *testing.T) {
	f := newFixture(t)
	c1, err := f.s.CreateContribution(f.ctx, f.class.ID, "Jedna", 1000, nil, []int64{f.kid.ID})
	f.must(err)
	_, err = f.s.CreateContribution(f.ctx, f.class.ID, "Druga", 2000, nil, []int64{f.kid.ID})
	f.must(err)

	res, _ := f.s.RemindParents(f.ctx, f.class.ID, c1, false, f.treas)
	if res.Sent != 1 {
		t.Fatalf("%+v", res)
	}
	if body := f.remindersTo("anna@example.com")[0].Text; !strings.Contains(body, "Jedna") || strings.Contains(body, "Druga") {
		t.Fatalf("scoped to one collection:\n%s", body)
	}
	if res, _ = f.s.RemindParents(f.ctx, f.class.ID, 0, false, f.treas); res.Sent != 0 || res.Skipped != 1 {
		t.Fatalf("a second click within the cooldown must be skipped: %+v", res)
	}
	f.now = f.now.Add(ReminderCooldown + time.Hour)
	if res, _ = f.s.RemindParents(f.ctx, f.class.ID, 0, false, f.treas); res.Sent != 1 {
		t.Fatalf("after the cooldown the parent can be reminded again: %+v", res)
	}
	if n := len(f.remindersTo("anna@example.com")); n != 2 {
		t.Fatalf("mails %d", n)
	}
}

func TestReminderSkipsPaidClosedErasedAndOtherClasses(t *testing.T) {
	f := newFixture(t)
	sps := f.contribution(f.class.ID, "Opłacona", 1000, f.kid)
	f.must(f.s.MarkStudentPaymentPaid(f.ctx, f.class.ID, sps[0].ID, f.treas))
	if res, _ := f.s.RemindParents(f.ctx, f.class.ID, 0, false, f.treas); res.Sent != 0 {
		t.Fatalf("nothing is owed: %+v", res)
	}
	gone := f.user("gone@example.com", "Gone Parent")
	kid2 := f.student(f.class.ID, "Zed", "Gone", gone)
	f.contribution(f.class.ID, "Dług", 500, kid2)
	f.must(f.s.EraseUser(f.ctx, gone.ID))
	if res, _ := f.s.RemindParents(f.ctx, f.class.ID, 0, false, f.treas); res.Sent != 0 {
		t.Fatalf("an erased parent must never be mailed: %+v", res)
	}
	other, _ := f.s.CreateClass(f.ctx, "5B")
	f.contribution(f.class.ID, "Jeszcze", 700, f.kid)
	if res, _ := f.s.RemindParents(f.ctx, other.ID, 0, false, f.treas); res.Sent != 0 {
		t.Fatalf("another class's reminders must not reach this class's parents: %+v", res)
	}
}
