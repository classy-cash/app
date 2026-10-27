package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/alior"
	"github.com/classy-cash/app/internal/auth"
	mailpkg "github.com/classy-cash/app/internal/mail"
	"github.com/classy-cash/app/internal/money"
	"github.com/classy-cash/app/internal/store"
)

func (s *Server) treasurerRoutes(mux *http.ServeMux) {
	m := s.needManager // can change data: treasurer and administrators
	v := s.needViewer  // can look: the above plus the class council (przewodniczący, wiceprzewodniczący)
	mux.HandleFunc("GET /treasurer", v(s.handleDashboard))
	mux.HandleFunc("GET /treasurer/{$}", v(s.handleDashboard))
	mux.HandleFunc("GET /treasurer/dashboard", v(s.handleDashboard))
	mux.HandleFunc("GET /treasurer/dashboard/cards", v(s.handleDashboardCards))
	mux.HandleFunc("GET /treasurer/ledger", v(s.handleLedgerRows))

	mux.HandleFunc("GET /treasurer/payments", v(s.handlePayments))
	mux.HandleFunc("GET /treasurer/payments/{id}/assign", m(s.handleAssignForm))
	mux.HandleFunc("POST /treasurer/payments/{id}/assign", m(s.handleAssign))
	mux.HandleFunc("POST /treasurer/payments/{id}/unassign", m(s.handleUnassign))
	mux.HandleFunc("POST /treasurer/payments/{id}/review", m(s.handleReview))
	mux.HandleFunc("POST /treasurer/payments/{id}/notify", m(s.handleNotifyParent))
	mux.HandleFunc("POST /treasurer/bank-events/{eid}/replay", m(s.handleReplay))
	mux.HandleFunc("POST /treasurer/payment/{id}/transition", m(s.handleTransition))

	mux.HandleFunc("GET /treasurer/contributions", v(s.handleContributions))
	mux.HandleFunc("POST /treasurer/contributions", m(s.handleContributionCreate))
	mux.HandleFunc("POST /treasurer/contributions/{id}/edit", m(s.handleContributionEdit))
	mux.HandleFunc("POST /treasurer/contributions/{id}/students", m(s.handleContributionAddStudent))
	mux.HandleFunc("POST /treasurer/contributions/{id}/close", m(s.handleContributionClose))
	mux.HandleFunc("POST /treasurer/contributions/{id}/reopen", m(s.handleContributionReopen))
	mux.HandleFunc("POST /treasurer/contributions/{id}/delete", m(s.handleContributionDelete))
	mux.HandleFunc("POST /treasurer/reminders", m(s.handleReminders))
	mux.HandleFunc("POST /treasurer/obligations/{id}/payment", m(s.handleObligationPayment))
	mux.HandleFunc("POST /treasurer/obligations/{id}/paid", m(s.handleObligationPaid))
	mux.HandleFunc("POST /treasurer/obligations/{id}/cancel", m(s.handleObligationCancel))
	mux.HandleFunc("POST /treasurer/obligations/{id}/amount", m(s.handleObligationAmount))

	mux.HandleFunc("GET /treasurer/students", v(s.handleStudents))
	mux.HandleFunc("POST /treasurer/students", m(s.handleStudentAdd))
	mux.HandleFunc("POST /treasurer/students/merge", m(s.handleStudentMerge))
	mux.HandleFunc("POST /treasurer/students/{id}/edit", m(s.handleStudentEdit))
	mux.HandleFunc("POST /treasurer/students/{id}/parents", m(s.handleParentAdd))
	mux.HandleFunc("POST /treasurer/students/{id}/parents/{uid}/remove", m(s.handleParentRemove))
	mux.HandleFunc("GET /treasurer/users/search", m(s.handleUserSearch))

	mux.HandleFunc("GET /treasurer/manual-transactions", m(s.handleManual))
	mux.HandleFunc("POST /treasurer/manual-transactions", m(s.handleManualPost))
	mux.HandleFunc("POST /treasurer/manual-transactions/{kind}/{id}/reverse", m(s.handleManualReverse))
	mux.HandleFunc("GET /treasurer/expenses/{id}/attachment", v(s.handleAttachmentGet))
	mux.HandleFunc("POST /treasurer/expenses/{id}/attachment", m(s.handleAttachmentUpload))
	mux.HandleFunc("POST /treasurer/expenses/{id}/attachment/delete", m(s.handleAttachmentDelete))
	mux.HandleFunc("GET /treasurer/settings", m(s.handleSettings))
	mux.HandleFunc("POST /treasurer/settings", m(s.handleSettingsSave))
	mux.HandleFunc("GET /treasurer/members", v(s.handleMembers))
	mux.HandleFunc("POST /treasurer/members", m(s.handleMemberAdd))
	mux.HandleFunc("POST /treasurer/members/{uid}/role", m(s.handleMemberRole))
	mux.HandleFunc("GET /admin/settings", s.needAdmin(func(w http.ResponseWriter, r *http.Request) { s.redirect(w, r, "/treasurer/settings") }))
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

func (s *Server) classID(r *http.Request) int64 { return state(r).class.ID }

// fail translates store errors to friendly flashes + redirect.
func (s *Server) flashErr(w http.ResponseWriter, r *http.Request, to string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "error", "Nie znaleziono wskazanej pozycji.")
	case errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrConflict):
		s.setFlash(w, r, "error", friendly(err))
	default:
		s.Log.Error("treasurer action", "path", r.URL.Path, "err", err)
		s.setFlash(w, r, "error", "Nie udało się wykonać operacji. Spróbuj ponownie.")
	}
	s.redirect(w, r, to)
}

func friendly(err error) string {
	m := err.Error()
	if i := strings.Index(m, ": "); i >= 0 {
		m = m[i+2:]
	}
	switch {
	case strings.Contains(m, "paid obligation"):
		return "Nie można anulować opłaconej składki."
	case strings.Contains(m, "already has a treasurer"):
		return "Ta klasa ma już skarbnika."
	case strings.Contains(m, "already has a president"):
		return "Ta klasa ma już przewodniczącego."
	case strings.Contains(m, "already has a vice_president"):
		return "Ta klasa ma już wiceprzewodniczącego."
	case strings.Contains(m, "already paid"):
		return "Ta składka jest już opłacona."
	case strings.Contains(m, "different students"):
		return "Wybierz dwóch różnych uczniów."
	case strings.Contains(m, "description is required"):
		return "Opis wydatku jest wymagany."
	case strings.Contains(m, "amount must be positive"):
		return "Kwota musi być większa od zera."
	case strings.Contains(m, "title and a positive amount"):
		return "Tytuł i kwota składki są wymagane."
	case strings.Contains(m, "2-100"), strings.Contains(m, "at least 2"):
		return "Imię i nazwisko muszą mieć od 2 do 100 znaków."
	}
	return "Operacja nie jest teraz możliwa."
}

// ---- dashboard ----

func (s *Server) dashboardData(r *http.Request) (map[string]any, error) {
	cid := s.classID(r)
	sum, err := s.Store.Summary(r.Context(), cid)
	if err != nil {
		return nil, err
	}
	rows, more, err := s.Store.LedgerPage(r.Context(), cid, 1, 10)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Sum": sum, "Rows": rows, "More": more, "Page": 1, "Month": time.Now()}, nil
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.dashboardData(r)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się wczytać panelu.")
		return
	}
	p := s.page(r, "Panel główny", "dashboard", d)
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_dashboard", p, http.StatusOK)
}

func (s *Server) handleDashboardCards(w http.ResponseWriter, r *http.Request) {
	d, err := s.dashboardData(r)
	if err != nil {
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	s.fragment(w, r, "t_dashboard", "cards", d)
}

func (s *Server) handleLedgerRows(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	rows, more, err := s.Store.LedgerPage(r.Context(), s.classID(r), page, 10)
	if err != nil {
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	if more {
		w.Header().Set("X-Has-More", "1")
	}
	s.fragment(w, r, "t_dashboard", "ledger_rows", map[string]any{"Rows": rows})
}

// ---- bank operations ----

func (s *Server) handlePayments(w http.ResponseWriter, r *http.Request) {
	cid := s.classID(r)
	transfers, err := s.Store.Transfers(r.Context(), cid)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się wczytać operacji.")
		return
	}
	// Quarantined deliveries are instance-wide (one bank inbox), so only administrators see them.
	var events []store.BankEvent
	if state(r).user.IsAdmin {
		events, _ = s.Store.BankEvents(r.Context(), "unrecognized", 20)
		rejected, _ := s.Store.BankEvents(r.Context(), "rejected_sender", 20)
		events = append(events, rejected...)
	}
	parsed, _ := s.Store.BankEvents(r.Context(), "parsed", 1)
	payments, _ := s.Store.RecentPayments(r.Context(), cid, 50)
	p := s.page(r, "Operacje bankowe", "payments", map[string]any{
		"Transfers": transfers, "Last": s.Store.LastBankEvent(r.Context()), "Events": events,
		"BankAddress": s.Cfg.Bank.Address, "BankEnabled": s.Cfg.Bank.Enabled(), "FirstParsed": len(parsed) > 0, "Payments": payments,
	})
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_payments", p, http.StatusOK)
}

func (s *Server) handleAssignForm(w http.ResponseWriter, r *http.Request) {
	cid, tid := s.classID(r), pathID(r, "id")
	t, err := s.Store.TransferByID(r.Context(), cid, tid)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Nie znaleziono przelewu.")
		return
	}
	q := r.URL.Query().Get("q")
	sugg, top, err := s.Store.Suggestions(r.Context(), cid, tid, q)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się wczytać płatności.")
		return
	}
	open, _ := s.Store.OpenStudentPayments(r.Context(), cid)
	var bare []store.StudentPayment
	for _, sp := range open {
		if sp.PaymentID == nil {
			bare = append(bare, sp)
		}
	}
	p := s.page(r, "Przypisz przelew", "payments", map[string]any{"Transfer": t, "Suggestions": sugg, "Top": top, "Query": q, "Bare": bare})
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_assign", p, http.StatusOK)
}

func (s *Server) handleAssign(w http.ResponseWriter, r *http.Request) {
	cid, tid := s.classID(r), pathID(r, "id")
	var err error
	switch {
	case r.PostFormValue("payment_id") != "":
		pid, _ := strconv.ParseInt(r.PostFormValue("payment_id"), 10, 64)
		err = s.Store.AssignTransfer(r.Context(), cid, tid, pid)
	case r.PostFormValue("student_payment_id") != "":
		sid, _ := strconv.ParseInt(r.PostFormValue("student_payment_id"), 10, 64)
		err = s.Store.AssignTransferToObligation(r.Context(), cid, tid, sid, *state(r).user)
	default:
		s.setFlash(w, r, "error", "Wybierz płatność, do której przypisać przelew.")
		s.redirect(w, r, "/treasurer/payments/"+itoa(tid)+"/assign")
		return
	}
	if err != nil {
		s.flashErr(w, r, "/treasurer/payments/"+itoa(tid)+"/assign", err)
		return
	}
	s.setFlash(w, r, "success", "Przelew został przypisany pomyślnie.")
	s.redirect(w, r, "/treasurer/payments")
}

func (s *Server) handleUnassign(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.UnassignTransfer(r.Context(), s.classID(r), pathID(r, "id")); err != nil {
		s.flashErr(w, r, "/treasurer/payments", err)
		return
	}
	s.setFlash(w, r, "success", "Przypisanie zostało cofnięte. Przelew czeka na ponowne dopasowanie.")
	s.redirect(w, r, "/treasurer/payments")
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	approve := r.PostFormValue("action") == "approve"
	if err := s.Store.ReviewTransfer(r.Context(), s.classID(r), pathID(r, "id"), approve); err != nil {
		s.flashErr(w, r, "/treasurer/payments", err)
		return
	}
	if approve {
		s.setFlash(w, r, "success", "Przelew zatwierdzono jako nową wpłatę.")
	} else {
		s.setFlash(w, r, "success", "Duplikat został odrzucony.")
	}
	s.redirect(w, r, "/treasurer/payments")
}

func (s *Server) handleNotifyParent(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.NotifyParentAboutDifference(r.Context(), s.classID(r), pathID(r, "id")); err != nil {
		s.flashErr(w, r, "/treasurer/payments", err)
		return
	}
	s.setFlash(w, r, "success", "Wiadomość do rodzica została zaplanowana do wysłania.")
	s.redirect(w, r, "/treasurer/payments")
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	if !state(r).user.IsAdmin {
		s.fail(w, r, http.StatusForbidden, "Ta operacja jest dostępna tylko dla administratora.")
		return
	}
	res, err := s.Store.ReplayEvent(r.Context(), r.PathValue("eid"), alior.Parse)
	switch {
	case err != nil:
		s.flashErr(w, r, "/treasurer/payments", err)
		return
	case res.Outcome == store.OutcomeUnrecognized:
		s.setFlash(w, r, "error", "Wiadomość nadal nie jest rozpoznawana jako uznanie rachunku.")
	default:
		s.setFlash(w, r, "success", "Wiadomość została przetworzona ponownie.")
	}
	s.redirect(w, r, "/treasurer/payments")
}

func (s *Server) handleTransition(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Transition(r.Context(), s.classID(r), pathID(r, "id"), r.PostFormValue("action")); err != nil {
		s.flashErr(w, r, "/treasurer/payments", err)
		return
	}
	s.setFlash(w, r, "success", "Status płatności został zmieniony.")
	s.redirect(w, r, "/treasurer/payments")
}

// ---- contributions ----

func (s *Server) handleContributions(w http.ResponseWriter, r *http.Request) {
	cid := s.classID(r)
	views, err := s.Store.ContributionViews(r.Context(), cid)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się wczytać składek.")
		return
	}
	students, _ := s.Store.ListStudents(r.Context(), cid)
	p := s.page(r, "Zarządzanie składkami", "contributions", map[string]any{"Contributions": views, "Students": students, "Form": map[string]string{}})
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_contributions", p, http.StatusOK)
}

func (s *Server) parseDue(v string) (*time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	d, err := time.ParseInLocation("2006-01-02", v, s.Cfg.Location)
	if err != nil {
		return nil, err
	}
	end := d.Add(23*time.Hour + 59*time.Minute + 59*time.Second) // due until the end of that day
	return &end, nil
}

func (s *Server) handleContributionCreate(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.PostFormValue("title"))
	amount, aerr := money.Parse(r.PostFormValue("amount_pln"))
	due, derr := s.parseDue(r.PostFormValue("due_at"))
	if title == "" || aerr != nil || amount <= 0 || derr != nil {
		s.setFlash(w, r, "error", "Tytuł i poprawna kwota składki są wymagane.")
		s.redirect(w, r, "/treasurer/contributions")
		return
	}
	var ids []int64
	for _, v := range r.PostForm["students"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	if _, err := s.Store.CreateContribution(r.Context(), s.classID(r), title, amount, due, ids); err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	s.setFlash(w, r, "success", "Składka została utworzona.")
	s.redirect(w, r, "/treasurer/contributions")
}

func (s *Server) handleContributionEdit(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.PostFormValue("title"))
	amount, aerr := money.Parse(r.PostFormValue("amount_pln"))
	due, derr := s.parseDue(r.PostFormValue("due_at"))
	if title == "" || aerr != nil || amount <= 0 || derr != nil {
		s.setFlash(w, r, "error", "Tytuł i poprawna kwota składki są wymagane.")
		s.redirect(w, r, "/treasurer/contributions")
		return
	}
	if err := s.Store.UpdateContribution(r.Context(), s.classID(r), pathID(r, "id"), title, amount, due); err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	s.setFlash(w, r, "success", "Zbiórka została zaktualizowana.")
	s.redirect(w, r, "/treasurer/contributions")
}

func (s *Server) handleContributionAddStudent(w http.ResponseWriter, r *http.Request) {
	sid, _ := strconv.ParseInt(r.PostFormValue("student_id"), 10, 64)
	if err := s.Store.AddStudentToContribution(r.Context(), s.classID(r), pathID(r, "id"), sid); err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	s.setFlash(w, r, "success", "Uczeń został dopisany do zbiórki.")
	s.redirect(w, r, "/treasurer/contributions")
}

func (s *Server) handleContributionClose(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.SetContributionClosed(r.Context(), s.classID(r), pathID(r, "id"), true, r.PostFormValue("cancel_unpaid") != "")
	if err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	msg := "Zbiórka została zakończona."
	if n > 0 {
		msg += " Anulowano obowiązki: " + strconv.Itoa(n) + "."
	}
	s.setFlash(w, r, "success", msg)
	s.redirect(w, r, "/treasurer/contributions")
}

func (s *Server) handleContributionReopen(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Store.SetContributionClosed(r.Context(), s.classID(r), pathID(r, "id"), false, false); err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	s.setFlash(w, r, "success", "Zbiórka została wznowiona.")
	s.redirect(w, r, "/treasurer/contributions")
}

func (s *Server) handleContributionDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteContribution(r.Context(), s.classID(r), pathID(r, "id")); err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	s.setFlash(w, r, "success", "Zbiórka została usunięta.")
	s.redirect(w, r, "/treasurer/contributions")
}

// handleReminders mails the parents of unpaid obligations; a deliberate action, never scheduled.
func (s *Server) handleReminders(w http.ResponseWriter, r *http.Request) {
	cid, _ := strconv.ParseInt(r.PostFormValue("contribution_id"), 10, 64)
	res, err := s.Store.RemindParents(r.Context(), s.classID(r), cid, r.PostFormValue("scope") == "overdue", *state(r).user)
	if err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	switch {
	case res.Sent == 0 && res.Skipped == 0:
		s.setFlash(w, r, "info", "Nie ma do kogo wysłać przypomnienia: brak nieopłaconych składek z przypisanym rodzicem.")
	default:
		msg := "Przypomnienia zaplanowano dla rodziców: " + strconv.Itoa(res.Sent) + "."
		if res.Skipped > 0 {
			msg += " Pominięto " + strconv.Itoa(res.Skipped) + " — dostali już przypomnienie w ciągu ostatnich 3 dni."
		}
		s.setFlash(w, r, "success", msg)
	}
	s.redirect(w, r, "/treasurer/contributions")
}

func (s *Server) handleObligationPayment(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.EnsureStudentPayment(r.Context(), s.classID(r), pathID(r, "id"), *state(r).user); err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	s.setFlash(w, r, "success", "Płatność została utworzona.")
	s.redirect(w, r, "/treasurer/contributions")
}

func (s *Server) handleObligationPaid(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.MarkStudentPaymentPaid(r.Context(), s.classID(r), pathID(r, "id"), *state(r).user); err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	s.setFlash(w, r, "success", "Płatność została oznaczona jako opłacona.")
	s.redirect(w, r, "/treasurer/contributions")
}

func (s *Server) handleObligationAmount(w http.ResponseWriter, r *http.Request) {
	back := "/treasurer/contributions"
	if r.PostFormValue("reset") != "" {
		if err := s.Store.ResetObligationAmount(r.Context(), s.classID(r), pathID(r, "id")); err != nil {
			s.flashErr(w, r, back, err)
			return
		}
		s.setFlash(w, r, "success", "Przywrócono kwotę zbiórki.")
		s.redirect(w, r, back)
		return
	}
	amount, err := money.Parse(r.PostFormValue("amount_pln"))
	if err != nil || amount <= 0 {
		s.setFlash(w, r, "error", "Podaj poprawną, dodatnią kwotę. Aby całkiem zwolnić ucznia ze składki, anuluj obowiązek.")
		s.redirect(w, r, back)
		return
	}
	if err := s.Store.SetObligationAmount(r.Context(), s.classID(r), pathID(r, "id"), amount); err != nil {
		s.flashErr(w, r, back, err)
		return
	}
	s.setFlash(w, r, "success", "Kwota indywidualna została zapisana.")
	s.redirect(w, r, back)
}

func (s *Server) handleObligationCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.CancelStudentPayment(r.Context(), s.classID(r), pathID(r, "id")); err != nil {
		s.flashErr(w, r, "/treasurer/contributions", err)
		return
	}
	s.setFlash(w, r, "success", "Obowiązek zapłaty został anulowany.")
	s.redirect(w, r, "/treasurer/contributions")
}

// ---- students & parents ----

func (s *Server) handleStudents(w http.ResponseWriter, r *http.Request) {
	students, err := s.Store.ListStudents(r.Context(), s.classID(r))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się wczytać uczniów.")
		return
	}
	p := s.page(r, "Uczniowie i rodzice", "students", map[string]any{"Students": students})
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_students", p, http.StatusOK)
}

// ensureParent finds or creates an account by e-mail and mails an invitation to new ones.
func (s *Server) ensureParent(r *http.Request, email, name string) (store.User, error) {
	email = store.NormalizeEmail(email)
	if !validEmail(email) {
		return store.User{}, store.ErrInvalid
	}
	if u, err := s.Store.UserByEmail(r.Context(), email); err == nil {
		return u, nil
	}
	if strings.TrimSpace(name) == "" {
		name = strings.Split(email, "@")[0]
	}
	u, err := s.Store.CreateUser(r.Context(), email, name, "", false)
	if err != nil {
		return u, err
	}
	if raw, err := s.Store.CreateAuthToken(r.Context(), u.ID, store.TokenLogin, 72*time.Hour); err == nil {
		cl := state(r).class
		m := mailpkg.Invitation(s.Store.Branding(r.Context()), u.Name, cl.Name, s.Cfg.BaseURL+"/login/link/"+raw)
		_ = s.Store.Enqueue(r.Context(), mailpkg.Message{Key: "invite-" + auth.HashToken(raw)[:24], ToEmail: u.Email, ToName: u.Name, Subject: m.Subject, HTML: m.HTML, Text: m.Text})
	}
	return u, nil
}

func (s *Server) handleStudentAdd(w http.ResponseWriter, r *http.Request) {
	var parents []int64
	for _, e := range strings.FieldsFunc(r.PostFormValue("parent_emails"), func(c rune) bool { return c == ',' || c == ';' || c == '\n' || c == ' ' }) {
		u, err := s.ensureParent(r, e, "")
		if err != nil {
			s.setFlash(w, r, "error", "Nieprawidłowy adres e-mail rodzica: "+e)
			s.redirect(w, r, "/treasurer/students")
			return
		}
		parents = append(parents, u.ID)
	}
	st, err := s.Store.AddStudent(r.Context(), s.classID(r), r.PostFormValue("first_name"), r.PostFormValue("last_name"), parents)
	if err != nil {
		s.flashErr(w, r, "/treasurer/students", err)
		return
	}
	msg := "Uczeń został dodany."
	if r.PostFormValue("join_open") != "" {
		if titles, err := s.Store.EnrolInOpenContributions(r.Context(), s.classID(r), st.ID); err != nil {
			s.Log.Error("enrol late student", "err", err)
		} else if len(titles) > 0 {
			msg += " Dopisano do zbiórek: " + strings.Join(titles, ", ") + "."
		}
	}
	s.setFlash(w, r, "success", msg)
	s.redirect(w, r, "/treasurer/students")
}

func (s *Server) handleStudentEdit(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.UpdateStudent(r.Context(), s.classID(r), pathID(r, "id"), r.PostFormValue("first_name"), r.PostFormValue("last_name")); err != nil {
		s.flashErr(w, r, "/treasurer/students", err)
		return
	}
	s.setFlash(w, r, "success", "Dane ucznia zostały zapisane.")
	s.redirect(w, r, "/treasurer/students")
}

func (s *Server) handleStudentMerge(w http.ResponseWriter, r *http.Request) {
	keep, _ := strconv.ParseInt(r.PostFormValue("keep_student_id"), 10, 64)
	del, _ := strconv.ParseInt(r.PostFormValue("delete_student_id"), 10, 64)
	if keep == 0 || del == 0 {
		s.setFlash(w, r, "error", "Wybierz ucznia do zachowania i duplikat.")
		s.redirect(w, r, "/treasurer/students")
		return
	}
	if err := s.Store.MergeStudents(r.Context(), s.classID(r), keep, del); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.setFlash(w, r, "error", "Nie znaleziono uczniów do połączenia.")
			s.redirect(w, r, "/treasurer/students")
			return
		}
		s.flashErr(w, r, "/treasurer/students", err)
		return
	}
	s.setFlash(w, r, "success", "Uczniowie zostali połączeni.")
	s.redirect(w, r, "/treasurer/students")
}

func (s *Server) handleParentAdd(w http.ResponseWriter, r *http.Request) {
	u, err := s.ensureParent(r, r.PostFormValue("email"), r.PostFormValue("name"))
	if err == nil {
		err = s.Store.LinkParent(r.Context(), s.classID(r), pathID(r, "id"), u.ID)
	}
	if err != nil {
		s.flashErr(w, r, "/treasurer/students", err)
		return
	}
	s.setFlash(w, r, "success", "Rodzic został przypisany do ucznia.")
	s.redirect(w, r, "/treasurer/students")
}

func (s *Server) handleParentRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.UnlinkParent(r.Context(), s.classID(r), pathID(r, "id"), pathID(r, "uid")); err != nil {
		s.flashErr(w, r, "/treasurer/students", err)
		return
	}
	s.setFlash(w, r, "success", "Rodzic został odłączony od ucznia.")
	s.redirect(w, r, "/treasurer/students")
}

// handleUserSearch suggests existing accounts of the current class only — never other classes' parents.
func (s *Server) handleUserSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	out := []map[string]any{}
	if len(q) >= 3 {
		members, _ := s.Store.Members(r.Context(), s.classID(r))
		for _, m := range members {
			if strings.Contains(strings.ToLower(m.User.Name), q) || strings.Contains(strings.ToLower(m.User.Email), q) {
				out = append(out, map[string]any{"name": m.User.Name, "email": m.User.Email})
			}
		}
	}
	s.json(w, http.StatusOK, out)
}

// ---- manual cash ----

func (s *Server) handleManual(w http.ResponseWriter, r *http.Request) {
	open, _ := s.Store.OpenStudentPayments(r.Context(), s.classID(r))
	entries, _ := s.Store.ManualEntries(r.Context(), s.classID(r), 30)
	p := s.page(r, "Kasa ręczna", "manual", map[string]any{"Open": open, "Entries": entries, "Receipts": s.Store.AttachDir != ""})
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_manual", p, http.StatusOK)
}

func (s *Server) handleManualPost(w http.ResponseWriter, r *http.Request) {
	back := "/treasurer/manual-transactions"
	amount, err := money.Parse(r.PostFormValue("amount_pln"))
	if strings.TrimSpace(r.PostFormValue("amount_pln")) == "" || err != nil || amount <= 0 {
		s.setFlash(w, r, "error", "Podaj poprawną kwotę.")
		s.redirect(w, r, back)
		return
	}
	cid := s.classID(r)
	switch r.PostFormValue("transaction_type") {
	case "income":
		spID, _ := strconv.ParseInt(r.PostFormValue("student_payment_id"), 10, 64)
		if err := s.Store.ManualIncome(r.Context(), cid, *state(r).user, amount, spID); err != nil {
			s.flashErr(w, r, back, err)
			return
		}
		if spID != 0 {
			s.setFlash(w, r, "success", "Wpłata została zaksięgowana.")
		} else {
			s.setFlash(w, r, "success", "Przychód został zaksięgowany.")
		}
	case "expense":
		eid, err := s.Store.ManualExpense(r.Context(), cid, r.PostFormValue("expense_description"), amount, time.Now())
		if err != nil {
			s.flashErr(w, r, back, err)
			return
		}
		s.setFlash(w, r, "success", "Wydatek został zaksięgowany.")
		if f, _, ferr := r.FormFile("attachment"); ferr == nil {
			defer f.Close()
			if aerr := s.Store.SaveExpenseAttachment(r.Context(), cid, eid, f); aerr != nil {
				s.setFlash(w, r, "error", "Wydatek zapisano, ale paragonu nie dodano: "+attachmentError(aerr))
			}
		}
	default:
		s.setFlash(w, r, "error", "Wybierz rodzaj operacji.")
	}
	s.redirect(w, r, back)
}

func (s *Server) handleManualReverse(w http.ResponseWriter, r *http.Request) {
	back := "/treasurer/manual-transactions"
	var err error
	switch r.PathValue("kind") {
	case "income":
		err = s.Store.ReverseManualIncome(r.Context(), s.classID(r), pathID(r, "id"))
	case "expense":
		err = s.Store.VoidExpense(r.Context(), s.classID(r), pathID(r, "id"))
	default:
		err = store.ErrNotFound
	}
	if err != nil {
		s.flashErr(w, r, back, err)
		return
	}
	s.setFlash(w, r, "success", "Wpis został cofnięty, saldo kasy zostało przeliczone.")
	s.redirect(w, r, back)
}

// attachmentError turns a store error into a message for the treasurer.
func attachmentError(err error) string {
	switch {
	case errors.Is(err, store.ErrAttachmentType):
		return "dozwolone są pliki PDF, JPG, PNG i WebP."
	case errors.Is(err, store.ErrAttachmentSize):
		return "plik jest większy niż 5 MB."
	case errors.Is(err, store.ErrAttachmentsOff):
		return "załączniki są wyłączone na tej instalacji."
	default:
		return "nie udało się zapisać pliku."
	}
}

func (s *Server) handleAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	back := "/treasurer/manual-transactions"
	f, _, err := r.FormFile("attachment")
	if err != nil {
		s.setFlash(w, r, "error", "Wybierz plik z paragonem.")
		s.redirect(w, r, back)
		return
	}
	defer f.Close()
	switch err := s.Store.SaveExpenseAttachment(r.Context(), s.classID(r), pathID(r, "id"), f); {
	case err == nil:
		s.setFlash(w, r, "success", "Paragon został dodany.")
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "error", "Nie znaleziono wydatku.")
	default:
		s.setFlash(w, r, "error", "Nie dodano paragonu: "+attachmentError(err))
	}
	s.redirect(w, r, back)
}

func (s *Server) handleAttachmentDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteExpenseAttachment(r.Context(), s.classID(r), pathID(r, "id")); err != nil {
		s.flashErr(w, r, "/treasurer/manual-transactions", err)
		return
	}
	s.setFlash(w, r, "success", "Paragon został usunięty.")
	s.redirect(w, r, "/treasurer/manual-transactions")
}

// handleAttachmentGet serves a receipt to the people who may see the class books. The response is locked
// down: it is never interpreted as a page, cannot run script and PDFs are offered as downloads.
func (s *Server) handleAttachmentGet(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.ExpenseAttachment(r.Context(), s.classID(r), pathID(r, "id"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Nie znaleziono paragonu.")
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ContentType)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	if a.Inline {
		h.Set("Content-Disposition", "inline")
	} else {
		h.Set("Content-Disposition", `attachment; filename="paragon.pdf"`)
	}
	http.ServeFile(w, r, a.Path)
}

// ---- settings ----

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, nil, http.StatusOK)
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, errs map[string]string, status int) {
	ctx := r.Context()
	cl := state(r).class
	form := map[string]string{
		"app_name": s.Store.AppName(ctx), "email_from": s.Store.Setting(ctx, "email_from", ""),
		"class_name": cl.Name, "school_name": cl.SchoolName, "blik_phone": cl.BlikPhone, "transfer_account": cl.TransferAccount,
		"parent_registration": s.Store.Setting(ctx, "parent_registration_enabled", "0"),
	}
	if r.Method == http.MethodPost {
		for k := range form {
			if v := r.PostFormValue(k); k != "parent_registration" {
				form[k] = v
			}
		}
		form["parent_registration"] = map[bool]string{true: "1", false: "0"}[r.PostFormValue("parent_registration") != ""]
	}
	p := s.page(r, "Ustawienia", "settings", map[string]any{
		"Form": form, "Errors": errs, "BankAddress": s.Cfg.Bank.Address, "BankEnabled": s.Cfg.Bank.Enabled(),
		"MailDriver": s.Cfg.Mail.Driver, "Last": s.Store.LastBankEvent(ctx), "Instance": s.Cfg.InstanceID, "IsAdmin": state(r).user.IsAdmin,
	})
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_settings", p, status)
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	errs := map[string]string{}
	name := strings.TrimSpace(r.PostFormValue("app_name"))
	className := strings.TrimSpace(r.PostFormValue("class_name"))
	email := strings.TrimSpace(r.PostFormValue("email_from"))
	if name == "" || len(name) > 80 {
		errs["app_name"] = "Podaj nazwę aplikacji (do 80 znaków)."
	}
	if className == "" {
		errs["class_name"] = "Nazwa klasy jest wymagana."
	}
	if email != "" && !validEmail(email) {
		errs["email_from"] = "Podaj poprawny adres e-mail."
	}
	if len(errs) > 0 {
		s.renderSettings(w, r, errs, http.StatusUnprocessableEntity)
		return
	}
	ctx := r.Context()
	cl := *state(r).class
	cl.Name, cl.SchoolName = className, strings.TrimSpace(r.PostFormValue("school_name"))
	cl.BlikPhone, cl.TransferAccount = strings.TrimSpace(r.PostFormValue("blik_phone")), strings.TrimSpace(r.PostFormValue("transfer_account"))
	if err := s.Store.UpdateClass(ctx, cl); err != nil {
		s.flashErr(w, r, "/treasurer/settings", err)
		return
	}
	_ = s.Store.SetSetting(ctx, "app_name", name)
	_ = s.Store.SetSetting(ctx, "parent_registration_enabled", map[bool]string{true: "1", false: "0"}[r.PostFormValue("parent_registration") != ""])
	if !s.Cfg.Demo { // the reply-to address is an integration setting: locked in the demo
		_ = s.Store.SetSetting(ctx, "email_from", email)
	}
	s.setFlash(w, r, "success", "Ustawienia zostały zapisane.")
	s.redirect(w, r, "/treasurer/settings")
}

// ---- class council ("rada klasowa") ----

var assignableRoles = map[string]bool{store.RoleParent: true, store.RolePresident: true, store.RoleVicePresident: true}

func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) {
	members, err := s.Store.Members(r.Context(), s.classID(r))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się wczytać członków klasy.")
		return
	}
	p := s.page(r, "Rada klasowa", "members", map[string]any{"Members": members})
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_members", p, http.StatusOK)
}

func (s *Server) handleMemberAdd(w http.ResponseWriter, r *http.Request) {
	role := r.PostFormValue("role")
	if !assignableRoles[role] {
		s.setFlash(w, r, "error", "Wybierz rolę: rodzic, przewodniczący lub wiceprzewodniczący.")
		s.redirect(w, r, "/treasurer/members")
		return
	}
	u, err := s.ensureParent(r, r.PostFormValue("email"), r.PostFormValue("name"))
	if err == nil {
		var cur string
		if cur, err = s.Store.RoleIn(r.Context(), u, s.classID(r)); err == nil && cur == store.RoleTreasurer {
			s.setFlash(w, r, "error", "Ta osoba jest skarbnikiem klasy.")
			s.redirect(w, r, "/treasurer/members")
			return
		}
		if err == nil {
			err = s.Store.SetMember(r.Context(), s.classID(r), u.ID, role)
		}
	}
	if err != nil {
		s.flashErr(w, r, "/treasurer/members", err)
		return
	}
	s.setFlash(w, r, "success", "Rola została zapisana.")
	s.redirect(w, r, "/treasurer/members")
}

func (s *Server) handleMemberRole(w http.ResponseWriter, r *http.Request) {
	role := r.PostFormValue("role")
	uid := pathID(r, "uid")
	if !assignableRoles[role] {
		s.setFlash(w, r, "error", "Nieznana rola.")
		s.redirect(w, r, "/treasurer/members")
		return
	}
	members, _ := s.Store.Members(r.Context(), s.classID(r))
	for _, m := range members {
		if m.User.ID != uid {
			continue
		}
		if m.Role == store.RoleTreasurer {
			s.setFlash(w, r, "error", "Rolę skarbnika zmienia administrator w sekcji „Klasy”.")
			s.redirect(w, r, "/treasurer/members")
			return
		}
		if err := s.Store.SetMember(r.Context(), s.classID(r), uid, role); err != nil {
			s.flashErr(w, r, "/treasurer/members", err)
			return
		}
		s.setFlash(w, r, "success", "Rola została zapisana.")
		s.redirect(w, r, "/treasurer/members")
		return
	}
	s.setFlash(w, r, "error", "Nie znaleziono członka klasy.")
	s.redirect(w, r, "/treasurer/members")
}
