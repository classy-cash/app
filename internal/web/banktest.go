package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/classy-cash/app/internal/money"
	"github.com/classy-cash/app/internal/store"
)

// Bank-pipeline test (settings page). The treasurer opens a diagnostic reference, makes a small REAL transfer
// herself with the marker in the title, and watches the status. Nothing here moves money or books anything: see
// store/banktests.go for why a notification with the marker can never reach the accounting logic.

const bankTestBack = "/treasurer/settings#bank-test"

// bankTestAvailable reports whether a test makes sense on this instance and, if not, why.
func (s *Server) bankTestAvailable() (bool, string) {
	switch {
	case s.Cfg.Demo:
		return false, "W trybie demo odbiór powiadomień bankowych jest wyłączony, więc prawdziwego webhooka nie da się tu sprawdzić. Test działa tylko w instalacji produkcyjnej."
	case !s.Cfg.Bank.Enabled() || s.Cfg.Bank.Address == "":
		return false, "Odbiór powiadomień bankowych nie jest skonfigurowany w tej instalacji (adres i sekret ustawia operator)."
	}
	return true, ""
}

func (s *Server) handleBankTestCreate(w http.ResponseWriter, r *http.Request) {
	if ok, why := s.bankTestAvailable(); !ok {
		s.setFlash(w, r, "error", why)
		s.redirect(w, r, bankTestBack)
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("amount_pln"))
	if raw == "" {
		raw = "1,00"
	}
	amount, err := money.Parse(raw)
	if err != nil || amount <= 0 || amount > store.MaxBankTestMinor {
		s.setFlash(w, r, "error", "Podaj małą kwotę testową, od 0,01 do "+money.Format(store.MaxBankTestMinor)+".")
		s.redirect(w, r, bankTestBack)
		return
	}
	t, err := s.Store.CreateBankTest(r.Context(), s.classID(r), *state(r).user, amount)
	switch {
	case errors.Is(err, store.ErrConflict):
		s.setFlash(w, r, "error", "Masz już "+strconv.Itoa(store.MaxOpenBankTests)+" otwarte testy. Poczekaj na wynik albo anuluj któryś.")
	case err != nil:
		s.flashErr(w, r, bankTestBack, err)
		return
	default:
		s.setFlash(w, r, "success", "Utworzono test. Zrób teraz przelew z tytułem "+t.Title()+" — aplikacja niczego nie wysyła.")
	}
	s.redirect(w, r, bankTestBack)
}

func (s *Server) handleBankTestCancel(w http.ResponseWriter, r *http.Request) {
	err := s.Store.CancelBankTest(r.Context(), s.classID(r), pathID(r, "id"))
	switch {
	case errors.Is(err, store.ErrConflict):
		s.setFlash(w, r, "error", "Ten test jest już zamknięty.")
	case err != nil:
		s.flashErr(w, r, bankTestBack, err)
		return
	default:
		s.setFlash(w, r, "success", "Test został anulowany. Ewentualny późniejszy przelew z tym tytułem i tak nie zostanie zaksięgowany.")
	}
	s.redirect(w, r, bankTestBack)
}
