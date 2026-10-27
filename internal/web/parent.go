package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/classy-cash/app/internal/auth"
	"github.com/classy-cash/app/internal/store"
)

func (s *Server) parentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /parent", s.needInstalled(s.needUser(s.handleParent)))
	mux.HandleFunc("POST /parent/pay", s.needInstalled(s.needUser(s.handlePay)))
	mux.HandleFunc("GET /parent/pay/{id}/status", s.needInstalled(s.needUser(s.handlePayStatus)))
	mux.HandleFunc("GET /profile", s.needInstalled(s.needUser(s.handleProfile)))
	mux.HandleFunc("POST /profile", s.needInstalled(s.needUser(s.handleProfileSave)))
	mux.HandleFunc("POST /profile/password", s.needInstalled(s.needUser(s.handlePasswordChange)))
	mux.HandleFunc("GET /profile/export", s.needInstalled(s.needUser(s.handleProfileExport)))
	mux.HandleFunc("POST /profile/delete", s.needInstalled(s.needUser(s.handleProfileErase)))
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	if st.class == nil {
		p := s.page(r, "Brak klasy", "", nil)
		p.Layout = "layout_parent"
		s.render(w, r, "no_class", p, http.StatusOK)
		return
	}
	if s.Store.CanManage(r.Context(), *st.user, st.class.ID) { // council members land on their own children
		s.redirect(w, r, "/treasurer/dashboard")
		return
	}
	s.redirect(w, r, "/parent")
}

func (s *Server) handleParent(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	if st.class == nil {
		s.handleHome(w, r)
		return
	}
	all, err := s.Store.StudentPaymentsForParent(r.Context(), st.user.ID, st.class.ID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się wczytać składek.")
		return
	}
	var open, paid []store.StudentPayment
	var total int64
	for _, sp := range all {
		if sp.Status == store.SPPaid {
			paid = append(paid, sp)
			continue
		}
		open = append(open, sp)
		total += sp.AmountMinor
	}
	kids, _ := s.Store.StudentsOfParent(r.Context(), st.user.ID, st.class.ID)
	p := s.page(r, "Konto rodzica", "home", map[string]any{"Open": open, "Paid": paid, "Total": total, "Kids": kids})
	p.Layout = "layout_parent"
	s.render(w, r, "parent_dashboard", p, http.StatusOK)
}

// handlePay creates (or returns) the parent's payment code. JSON in, JSON out; used by the dialog.
func (s *Server) handlePay(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	if st.class == nil {
		s.fail(w, r, http.StatusBadRequest, "Brak klasy.")
		return
	}
	if s.Store.IsArchived(r.Context(), st.class.ID) {
		s.fail(w, r, http.StatusForbidden, "Ta klasa jest zarchiwizowana — nie można już zakładać płatności.")
		return
	}
	var ids []int64
	for _, v := range r.PostForm["ids"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	pay, err := s.Store.CreateFastPayment(r.Context(), *st.user, st.class.ID, ids)
	if errors.Is(err, store.ErrNothingToPay) {
		s.json(w, http.StatusOK, map[string]any{"ok": true, "nothing": true})
		return
	}
	if err != nil {
		s.Log.Error("fast payment", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się przygotować płatności.")
		return
	}
	items := make([]map[string]any, 0, len(pay.Items))
	for _, it := range pay.Items {
		items = append(items, map[string]any{"student": it.StudentName, "label": it.Label, "amount": formatMoney(it.AmountMinor)})
	}
	s.json(w, http.StatusOK, map[string]any{
		"ok": true, "id": pay.ID, "code": pay.Code, "amount": formatMoney(pay.AmountMinor), "items": items,
		"blik_phone": st.class.BlikPhone, "account": st.class.TransferAccount, "payee": "skarbnik",
	})
}

func (s *Server) handlePayStatus(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if st.class == nil {
		s.fail(w, r, http.StatusNotFound, "Nie znaleziono płatności.")
		return
	}
	pay, err := s.Store.PaymentByID(r.Context(), st.class.ID, id)
	if err != nil || pay.UserID != st.user.ID { // a parent only ever sees their own payment
		s.fail(w, r, http.StatusNotFound, "Nie znaleziono płatności.")
		return
	}
	s.json(w, http.StatusOK, map[string]any{"ok": true, "status": pay.Status, "paid": pay.Status == store.PaymentPaid, "amount": formatMoney(pay.AmountMinor)})
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	p := s.page(r, "Mój profil", "profile", map[string]any{"HasPassword": state(r).user.PasswordHash != "", "DemoLocked": s.Cfg.Demo})
	p.Layout = "layout_parent"
	s.render(w, r, "profile", p, http.StatusOK)
}

func (s *Server) handleProfileSave(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.Demo {
		s.setFlash(w, r, "error", "W trybie demo nie można zmieniać danych konta.")
		s.redirect(w, r, "/profile")
		return
	}
	if err := s.Store.UpdateProfile(r.Context(), state(r).user.ID, r.PostFormValue("name"), r.PostFormValue("phone")); err != nil {
		s.setFlash(w, r, "error", "Podaj imię i nazwisko.")
	} else {
		s.setFlash(w, r, "success", "Profil został zapisany.")
	}
	s.redirect(w, r, "/profile")
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	u := state(r).user
	if s.Cfg.Demo {
		s.setFlash(w, r, "error", "W trybie demo nie można zmieniać haseł.")
		s.redirect(w, r, "/profile")
		return
	}
	cur, a, b := r.PostFormValue("current"), r.PostFormValue("password"), r.PostFormValue("password_confirm")
	switch {
	case u.PasswordHash != "" && !auth.VerifyPassword(u.PasswordHash, cur):
		s.setFlash(w, r, "error", "Obecne hasło jest nieprawidłowe.")
	case len(a) < auth.MinPasswordLen:
		s.setFlash(w, r, "error", "Nowe hasło musi mieć co najmniej 8 znaków.")
	case a != b:
		s.setFlash(w, r, "error", "Hasła nie są takie same.")
	default:
		hash, err := auth.HashPassword(a)
		if err == nil {
			err = s.Store.SetPassword(r.Context(), u.ID, hash) // revokes all sessions, including this one
		}
		if err != nil {
			s.setFlash(w, r, "error", "Nie udało się zmienić hasła.")
			break
		}
		s.setFlash(w, r, "success", "Hasło zostało zmienione. Zaloguj się ponownie.")
		http.SetCookie(w, s.cookie(cookieSession, "", -1))
		s.redirect(w, r, "/login?mode=password")
		return
	}
	s.redirect(w, r, "/profile")
}

// handleProfileExport lets a person download everything the application holds about them (GDPR art. 15).
func (s *Server) handleProfileExport(w http.ResponseWriter, r *http.Request) {
	ex, err := s.Store.ExportUser(r.Context(), state(r).user.ID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się przygotować eksportu.")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="moje-dane-classy-cash.json"`)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(ex)
}

// handleProfileErase anonymises the signed-in account after an explicit confirmation (GDPR art. 17).
func (s *Server) handleProfileErase(w http.ResponseWriter, r *http.Request) {
	u := state(r).user
	if s.Cfg.Demo {
		s.setFlash(w, r, "error", "W trybie demo nie można usuwać kont.")
		s.redirect(w, r, "/profile")
		return
	}
	if strings.TrimSpace(r.PostFormValue("confirm")) != "USUŃ" || (u.PasswordHash != "" && !auth.VerifyPassword(u.PasswordHash, r.PostFormValue("current"))) {
		s.setFlash(w, r, "error", "Aby usunąć konto, wpisz słowo USUŃ i podaj aktualne hasło.")
		s.redirect(w, r, "/profile")
		return
	}
	switch err := s.Store.EraseUser(r.Context(), u.ID); {
	case errors.Is(err, store.ErrIsTreasurer):
		s.setFlash(w, r, "error", "Jesteś skarbnikiem klasy. Poproś administratora o przekazanie roli, a potem usuń konto.")
	case errors.Is(err, store.ErrLastAdmin):
		s.setFlash(w, r, "error", "Jesteś jedynym administratorem. Najpierw ustanów drugiego.")
	case err != nil:
		s.Log.Error("erase user", "err", err)
		s.setFlash(w, r, "error", "Nie udało się usunąć konta. Spróbuj ponownie.")
	default:
		http.SetCookie(w, s.cookie(cookieSession, "", -1))
		s.setFlash(w, r, "success", "Konto zostało usunięte, a Twoje dane osobowe zanonimizowane.")
		s.redirect(w, r, "/login")
		return
	}
	s.redirect(w, r, "/profile")
}

func formatMoney(minor int64) string { return strings.ReplaceAll(moneyFmt(minor), " ", " ") }
