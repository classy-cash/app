package store

import "testing"

func TestArchivedClassIsReadOnlyButReadable(t *testing.T) {
	f := newFixture(t)
	admin, err := f.s.CreateUser(f.ctx, "admin@example.com", "Admin", "", true)
	f.must(err)
	f.must(f.s.SetMember(f.ctx, f.class.ID, f.parent.ID, RoleVicePresident))
	if !f.s.CanManage(f.ctx, f.treas, f.class.ID) || !f.s.CanManage(f.ctx, admin, f.class.ID) {
		t.Fatal("precondition: both can manage a live class")
	}
	f.must(f.s.ArchiveClass(f.ctx, f.class.ID, true))
	if f.s.CanManage(f.ctx, f.treas, f.class.ID) || f.s.CanManage(f.ctx, admin, f.class.ID) {
		t.Fatal("nobody may change an archived class, administrators included")
	}
	for name, u := range map[string]User{"treasurer": f.treas, "admin": admin, "council": f.parent} {
		if !f.s.CanView(f.ctx, u, f.class.ID) {
			t.Fatalf("%s must still read the archive", name)
		}
	}
	if classes, _ := f.s.ClassesFor(f.ctx, f.treas); len(classes) != 1 || classes[0].ArchivedAt == nil {
		t.Fatalf("the archive stays listed and is flagged: %+v", classes)
	}
	f.must(f.s.ArchiveClass(f.ctx, f.class.ID, false))
	if !f.s.CanManage(f.ctx, f.treas, f.class.ID) {
		t.Fatal("restore must give the treasurer the class back")
	}
	if err := f.s.ArchiveClass(f.ctx, 999, true); err == nil {
		t.Fatal("unknown class")
	}
}

func TestArchivedClassesSortAfterLiveOnes(t *testing.T) {
	f := newFixture(t)
	f.must(f.s.ArchiveClass(f.ctx, f.class.ID, true))
	next, err := f.s.CreateClass(f.ctx, "5A")
	f.must(err)
	f.must(f.s.SetMember(f.ctx, next.ID, f.treas.ID, RoleParent))
	classes, _ := f.s.ClassesFor(f.ctx, f.treas)
	if len(classes) != 2 || classes[0].ID != next.ID || classes[1].ID != f.class.ID {
		t.Fatalf("the live class must be the default: %+v", classes)
	}
}

func TestRolloverCarriesPeopleButNotMoney(t *testing.T) {
	f := newFixture(t)
	f.must(f.s.UpdateClass(f.ctx, Class{ID: f.class.ID, Name: "4A", SchoolName: "SP 1", BlikPhone: "+48 111", TransferAccount: "PL 11"}))
	kid2 := f.student(f.class.ID, "Ewa", "Kowalska", f.parent) // sibling, same parent
	gone := f.student(f.class.ID, "Odeszła", "Uczennica")
	f.must(f.s.MergeStudents(f.ctx, f.class.ID, f.kid.ID, gone.ID)) // soft-deleted students stay behind
	sps := f.contribution(f.class.ID, "Wycieczka", 5000, f.kid, kid2)
	f.must(f.s.MarkStudentPaymentPaid(f.ctx, f.class.ID, sps[0].ID, f.treas))
	f.must(f.s.SetMember(f.ctx, f.class.ID, f.user("ewa@example.com", "Ewa Przewodnicząca").ID, RolePresident))
	oldCash := f.cashBalance()

	next, n, err := f.s.RolloverClass(f.ctx, f.class.ID, "5A", true)
	f.must(err)
	if n != 2 || next.Name != "5A" || next.BlikPhone != "+48 111" || next.TransferAccount != "PL 11" || next.SchoolName != "SP 1" {
		t.Fatalf("copied %d: %+v", n, next)
	}
	students, _ := f.s.ListStudents(f.ctx, next.ID)
	if len(students) != 2 {
		t.Fatalf("roster %+v", students)
	}
	for _, st := range students {
		if len(st.Parents) != 1 || st.Parents[0].ID != f.parent.ID {
			t.Fatalf("parent links must be copied: %+v", st)
		}
	}
	if role, _ := f.s.RoleIn(f.ctx, f.parent, next.ID); role != RoleParent {
		t.Fatalf("parent membership %q", role)
	}
	if role, _ := f.s.RoleIn(f.ctx, f.treas, next.ID); role != RoleTreasurer {
		t.Fatalf("the treasurer carries on, got %q", role)
	}
	if members, _ := f.s.Members(f.ctx, next.ID); func() bool {
		for _, m := range members {
			if m.Role == RolePresident {
				return true
			}
		}
		return false
	}() {
		t.Fatal("the council is elected anew")
	}
	if v, _ := f.s.ContributionViews(f.ctx, next.ID); len(v) != 0 {
		t.Fatal("contributions stay in the old year")
	}
	if sum, _ := f.s.Summary(f.ctx, next.ID); sum.TotalMinor != 0 || sum.OutstandingCount != 0 {
		t.Fatalf("a fresh class starts with an empty till: %+v", sum)
	}
	if f.cashBalance() != oldCash {
		t.Fatal("the old class keeps its books")
	}
	if !f.s.IsArchived(f.ctx, f.class.ID) {
		t.Fatal("the old class is archived on request")
	}
	if got := f.s.Setting(f.ctx, "bank_class_id", ""); got != itoa(next.ID) {
		t.Fatalf("bank notifications must follow the live class, got %q", got)
	}
}

func TestRolloverWithoutArchivingLeavesTheOldClassLive(t *testing.T) {
	f := newFixture(t)
	next, _, err := f.s.RolloverClass(f.ctx, f.class.ID, "5A", false)
	f.must(err)
	if f.s.IsArchived(f.ctx, f.class.ID) || f.s.Setting(f.ctx, "bank_class_id", "") != itoa(f.class.ID) {
		t.Fatal("nothing may change in the old class")
	}
	if _, _, err := f.s.RolloverClass(f.ctx, f.class.ID, "  ", false); err == nil {
		t.Fatal("a name is required")
	}
	if _, _, err := f.s.RolloverClass(f.ctx, 999, "X", false); err == nil {
		t.Fatal("unknown class")
	}
	_ = next
}
