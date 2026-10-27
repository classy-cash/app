package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/auth"
	mailpkg "github.com/classy-cash/app/internal/mail"
	"github.com/classy-cash/app/internal/store"
)

func (s *Server) authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginPassword)
	mux.HandleFunc("POST /login/link", s.handleLoginLinkRequest)
	mux.HandleFunc("GET /login/link/{token}", s.handleLoginLinkConfirm)
	mux.HandleFunc("POST /login/link/{token}", s.handleLoginLinkConsume)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /register", s.handleRegisterForm)
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("GET /reset-password", s.handleResetRequestForm)
	mux.HandleFunc("POST /reset-password", s.handleResetRequest)
	mux.HandleFunc("GET /reset-password/check-email", s.handleResetCheckEmail)
	mux.HandleFunc("GET /reset-password/reset/{token}", s.handleResetForm)
	mux.HandleFunc("POST /reset-password/reset/{token}", s.handleReset)
	mux.HandleFunc("GET /onboarding", s.handleOnboardingForm)
	mux.HandleFunc("POST /onboarding", s.handleOnboarding)
	mux.HandleFunc("POST /demo/login", s.handleDemoLogin)
	mux.HandleFunc("POST /class/switch", s.needUser(s.handleClassSwitch))
}

type demoAccount struct {
	Label string `json:"label"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// demoAccounts reads the quick-login list the demo wrapper seeds into the settings table.
func (s *Server) demoAccounts(r *http.Request) []demoAccount {
	if !s.Cfg.Demo {
		return nil
	}
	var list []demoAccount
	_ = json.Unmarshal([]byte(s.Store.Setting(r.Context(), "demo_accounts", "[]")), &list)
	return list
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if state(r).user != nil {
		s.redirect(w, r, "/")
		return
	}
	if n, _ := s.Store.CountUsers(r.Context()); n == 0 && !s.Cfg.Demo {
		s.redirect(w, r, "/onboarding")
		return
	}
	s.renderLogin(w, r, r.URL.Query().Get("mode") == "password", "", "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, password bool, email, errMsg string) {
	p := s.page(r, "Zaloguj się", "", map[string]any{
		"Password": password, "Email": email, "Error": errMsg, "Demo": s.demoAccounts(r),
		"Registration": s.Store.ParentRegistrationEnabled(r.Context()),
	})
	p.Layout = "layout_auth"
	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusUnprocessableEntity
	}
	s.render(w, r, "login", p, status)
}

func validEmail(e string) bool {
	a, err := mail.ParseAddress(e)
	return err == nil && a.Address == e && strings.Contains(e, ".") && len(e) <= 254
}

func (s *Server) handleLoginPassword(w http.ResponseWriter, r *http.Request) {
	email := store.NormalizeEmail(r.PostFormValue("email"))
	password := r.PostFormValue("password")
	if !validEmail(email) || password == "" {
		s.renderLogin(w, r, true, email, "Podaj poprawny adres e-mail i hasło.")
		return
	}
	if !s.limiter.allow("login:"+s.clientIP(r), 20, 15*time.Minute) || !s.limiter.allow("login:"+email, 8, 15*time.Minute) {
		s.renderLogin(w, r, true, email, "Zbyt wiele prób logowania. Odczekaj kilka minut albo zaloguj się linkiem e-mail.")
		return
	}
	u, err := s.Store.UserByEmail(r.Context(), email)
	if err != nil || u.PasswordHash == "" || !auth.VerifyPassword(u.PasswordHash, password) {
		if err != nil || u.PasswordHash == "" {
			auth.DummyVerify()
		}
		s.renderLogin(w, r, true, email, "Nieprawidłowy adres e-mail lub hasło.")
		return
	}
	if u.ConfirmedAt == nil {
		_ = s.Store.ConfirmUser(r.Context(), u.ID)
	}
	s.startSession(w, r, u)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User) {
	raw, err := s.Store.CreateSession(r.Context(), u)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się zalogować. Spróbuj ponownie.")
		return
	}
	http.SetCookie(w, s.cookie(cookieSession, raw, int(store.SessionTTL.Seconds())))
	s.redirect(w, r, "/")
}

// sendLoginLink emails a one-time sign-in link. Unknown addresses get nothing (no enumeration).
func (s *Server) sendLoginLink(r *http.Request, email string) {
	u, err := s.Store.UserByEmail(r.Context(), email)
	if err != nil {
		return
	}
	raw, err := s.Store.CreateAuthToken(r.Context(), u.ID, store.TokenLogin, time.Hour)
	if err != nil {
		s.Log.Error("login token", "err", err)
		return
	}
	link := s.Cfg.BaseURL + "/login/link/" + raw
	m := mailpkg.LoginLink(s.Store.Branding(r.Context()), u.Name, link, u.ConfirmedAt == nil)
	_ = s.Store.Enqueue(r.Context(), mailpkg.Message{Key: "login-" + auth.HashToken(raw)[:24], ToEmail: u.Email, ToName: u.Name, Subject: m.Subject, HTML: m.HTML, Text: m.Text})
}

func (s *Server) handleLoginLinkRequest(w http.ResponseWriter, r *http.Request) {
	email := store.NormalizeEmail(r.PostFormValue("email"))
	if !validEmail(email) {
		s.renderLogin(w, r, false, email, "Podaj poprawny adres e-mail.")
		return
	}
	if s.limiter.allow("link:"+s.clientIP(r), 10, 15*time.Minute) && s.limiter.allow("link:"+email, 3, 15*time.Minute) {
		s.sendLoginLink(r, email)
	}
	s.renderLinkSent(w, r, email)
}

func (s *Server) renderLinkSent(w http.ResponseWriter, r *http.Request, email string) {
	name, url := webmail(email)
	if url == "" && s.Cfg.Env == "prod" && !s.Cfg.Demo {
		name, url = s.webmailMX(r.Context(), email)
	}
	p := s.page(r, "Sprawdź skrzynkę", "", map[string]any{"Email": email, "WebmailName": name, "WebmailURL": url, "Capture": s.Sender != nil && s.Sender.Captures()})
	p.Layout = "layout_auth"
	s.render(w, r, "link_sent", p, http.StatusOK)
}

func (s *Server) handleLoginLinkConfirm(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if _, err := s.Store.PeekAuthToken(r.Context(), token, store.TokenLogin); err != nil {
		p := s.page(r, "Link wygasł", "", map[string]any{"Kind": "login"})
		p.Layout = "layout_auth"
		s.render(w, r, "link_invalid", p, http.StatusGone)
		return
	}
	// A GET never spends the token: mail scanners and link previews would otherwise burn it.
	p := s.page(r, "Potwierdź logowanie", "", map[string]any{"Token": token})
	p.Layout = "layout_auth"
	s.render(w, r, "link_confirm", p, http.StatusOK)
}

func (s *Server) handleLoginLinkConsume(w http.ResponseWriter, r *http.Request) {
	id, err := s.Store.ConsumeAuthToken(r.Context(), r.PathValue("token"), store.TokenLogin)
	if err != nil {
		s.fail(w, r, http.StatusGone, "Ten link wygasł albo został już użyty. Poproś o nowy.")
		return
	}
	u, err := s.Store.UserByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, http.StatusGone, "Konto nie istnieje.")
		return
	}
	_ = s.Store.ConfirmUser(r.Context(), u.ID)
	s.startSession(w, r, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if raw := state(r).sessionRaw; raw != "" {
		_ = s.Store.DeleteSession(r.Context(), raw)
	}
	http.SetCookie(w, s.cookie(cookieSession, "", -1))
	s.redirect(w, r, "/login")
}

func (s *Server) handleClassSwitch(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	var id int64
	for _, c := range st.classes {
		if r.PostFormValue("class_id") == itoa(c.ID) {
			id = c.ID
		}
	}
	if id != 0 {
		_ = s.Store.SetSessionClass(r.Context(), st.sessionRaw, id)
	}
	s.redirect(w, r, "/")
}

// ---- registration ----

func (s *Server) handleRegisterForm(w http.ResponseWriter, r *http.Request) {
	p := s.page(r, "Dołącz jako rodzic", "", map[string]any{"Enabled": s.Store.ParentRegistrationEnabled(r.Context()) && !s.Cfg.Demo, "Form": map[string]string{}})
	p.Layout = "layout_auth"
	s.render(w, r, "register", p, http.StatusOK)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	enabled := s.Store.ParentRegistrationEnabled(r.Context()) && !s.Cfg.Demo
	first, last := strings.TrimSpace(r.PostFormValue("first_name")), strings.TrimSpace(r.PostFormValue("last_name"))
	email := store.NormalizeEmail(r.PostFormValue("email"))
	errs := map[string]string{}
	if len([]rune(first)) < 2 || len([]rune(first)) > 100 {
		errs["first_name"] = "Imię musi mieć od 2 do 100 znaków."
	}
	if len([]rune(last)) < 2 || len([]rune(last)) > 100 {
		errs["last_name"] = "Nazwisko musi mieć od 2 do 100 znaków."
	}
	if !validEmail(email) {
		errs["email"] = "Podaj poprawny adres e-mail."
	}
	if !enabled || len(errs) > 0 || !s.limiter.allow("register:"+s.clientIP(r), 10, time.Hour) {
		p := s.page(r, "Dołącz jako rodzic", "", map[string]any{"Enabled": enabled, "Errors": errs, "Form": map[string]string{"first_name": first, "last_name": last, "email": email}})
		p.Layout = "layout_auth"
		s.render(w, r, "register", p, http.StatusUnprocessableEntity)
		return
	}
	// The same screen follows for new and existing addresses, so registration reveals nothing.
	if _, err := s.Store.UserByEmail(r.Context(), email); errors.Is(err, store.ErrNotFound) {
		if _, err := s.Store.CreateUser(r.Context(), email, first+" "+last, "", false); err != nil {
			s.Log.Error("register", "err", err)
		}
	}
	s.sendLoginLink(r, email)
	s.renderLinkSent(w, r, email)
}

// ---- password reset ----

func (s *Server) handleResetRequestForm(w http.ResponseWriter, r *http.Request) {
	if state(r).user != nil {
		s.redirect(w, r, "/")
		return
	}
	p := s.page(r, "Resetuj hasło", "", map[string]any{"Disabled": s.Cfg.Demo})
	p.Layout = "layout_auth"
	s.render(w, r, "reset_request", p, http.StatusOK)
}

func (s *Server) handleResetRequest(w http.ResponseWriter, r *http.Request) {
	email := store.NormalizeEmail(r.PostFormValue("email"))
	if !s.Cfg.Demo && validEmail(email) && s.limiter.allow("reset:"+s.clientIP(r), 10, 15*time.Minute) && s.limiter.allow("reset:"+email, 3, 15*time.Minute) {
		if u, err := s.Store.UserByEmail(r.Context(), email); err == nil {
			if raw, err := s.Store.CreateAuthToken(r.Context(), u.ID, store.TokenReset, time.Hour); err == nil {
				m := mailpkg.PasswordReset(s.Store.Branding(r.Context()), u.Name, s.Cfg.BaseURL+"/reset-password/reset/"+raw)
				_ = s.Store.Enqueue(r.Context(), mailpkg.Message{Key: "reset-" + auth.HashToken(raw)[:24], ToEmail: u.Email, ToName: u.Name, Subject: m.Subject, HTML: m.HTML, Text: m.Text})
			}
		}
	}
	s.redirect(w, r, "/reset-password/check-email")
}

func (s *Server) handleResetCheckEmail(w http.ResponseWriter, r *http.Request) {
	p := s.page(r, "Sprawdź e-mail", "", nil)
	p.Layout = "layout_auth"
	s.render(w, r, "reset_check", p, http.StatusOK)
}

func (s *Server) handleResetForm(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if _, err := s.Store.PeekAuthToken(r.Context(), token, store.TokenReset); err != nil {
		p := s.page(r, "Link wygasł", "", map[string]any{"Kind": "reset"})
		p.Layout = "layout_auth"
		s.render(w, r, "link_invalid", p, http.StatusGone)
		return
	}
	p := s.page(r, "Ustaw nowe hasło", "", map[string]any{"Token": token})
	p.Layout = "layout_auth"
	s.render(w, r, "reset_form", p, http.StatusOK)
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	a, b := r.PostFormValue("password"), r.PostFormValue("password_confirm")
	msg := ""
	switch {
	case len(a) < auth.MinPasswordLen:
		msg = "Hasło musi mieć co najmniej 8 znaków."
	case a != b:
		msg = "Hasła nie są takie same."
	}
	if msg != "" {
		p := s.page(r, "Ustaw nowe hasło", "", map[string]any{"Token": token, "Error": msg})
		p.Layout = "layout_auth"
		s.render(w, r, "reset_form", p, http.StatusUnprocessableEntity)
		return
	}
	id, err := s.Store.ConsumeAuthToken(r.Context(), token, store.TokenReset)
	if err != nil {
		s.fail(w, r, http.StatusGone, "Ten link wygasł albo został już użyty. Poproś o nowy.")
		return
	}
	hash, err := auth.HashPassword(a)
	if err == nil {
		err = s.Store.SetPassword(r.Context(), id, hash) // also signs the user out everywhere
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Nie udało się zmienić hasła.")
		return
	}
	s.setFlash(w, r, "success", "Hasło zostało zmienione. Zaloguj się nowym hasłem.")
	s.redirect(w, r, "/login?mode=password")
}

// ---- onboarding (first-run claim) ----

func (s *Server) onboardingAllowed(r *http.Request) (bool, string) {
	if s.Cfg.Demo {
		return false, "Ta instalacja działa w trybie demo."
	}
	if n, err := s.Store.CountUsers(r.Context()); err != nil || n > 0 {
		return false, "Ta instalacja jest już skonfigurowana."
	}
	if s.Cfg.Env == "prod" && s.Cfg.ClaimToken == "" {
		return false, "Instalacja czeka na kod aktywacyjny (CLASSYCASH_CLAIM_TOKEN)."
	}
	return true, ""
}

func (s *Server) handleOnboardingForm(w http.ResponseWriter, r *http.Request) {
	ok, why := s.onboardingAllowed(r)
	if !ok {
		if why == "Ta instalacja jest już skonfigurowana." {
			s.redirect(w, r, "/login")
			return
		}
		s.fail(w, r, http.StatusServiceUnavailable, why)
		return
	}
	s.renderOnboarding(w, r, map[string]string{"claim": r.URL.Query().Get("claim")}, nil, http.StatusOK)
}

func (s *Server) renderOnboarding(w http.ResponseWriter, r *http.Request, form map[string]string, errs map[string]string, status int) {
	p := s.page(r, "Nowa instalacja", "", map[string]any{"Form": form, "Errors": errs, "NeedClaim": s.Cfg.ClaimToken != "", "BankAddress": s.Cfg.Bank.Address})
	p.Layout = "layout_auth"
	s.render(w, r, "onboarding", p, status)
}

func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	ok, why := s.onboardingAllowed(r)
	if !ok {
		s.fail(w, r, http.StatusForbidden, why)
		return
	}
	f := map[string]string{}
	for _, k := range []string{"first_name", "last_name", "email", "class_name", "school_name", "blik_phone", "account", "claim"} {
		f[k] = strings.TrimSpace(r.PostFormValue(k))
	}
	f["email"] = store.NormalizeEmail(f["email"])
	password := r.PostFormValue("password")
	errs := map[string]string{}
	if s.Cfg.ClaimToken != "" && !constEq(f["claim"], s.Cfg.ClaimToken) {
		errs["claim"] = "Nieprawidłowy kod aktywacyjny."
	}
	if f["first_name"] == "" {
		errs["first_name"] = "Imię jest wymagane."
	}
	if f["last_name"] == "" {
		errs["last_name"] = "Nazwisko jest wymagane."
	}
	if !validEmail(f["email"]) {
		errs["email"] = "Podaj poprawny adres e-mail."
	}
	if len(password) < auth.MinPasswordLen {
		errs["password"] = "Hasło musi mieć co najmniej 8 znaków."
	}
	if f["class_name"] == "" {
		errs["class_name"] = "Nazwa klasy jest wymagana."
	}
	if len(errs) > 0 || !s.limiter.allow("onboard:"+s.clientIP(r), 10, time.Hour) {
		s.renderOnboarding(w, r, f, errs, http.StatusUnprocessableEntity)
		return
	}
	u, _, err := s.Store.Onboard(r.Context(), store.OnboardInput{FirstName: f["first_name"], LastName: f["last_name"], Email: f["email"], Password: password, ClassName: f["class_name"], SchoolName: f["school_name"], BlikPhone: f["blik_phone"], Account: f["account"]})
	if err != nil {
		s.fail(w, r, http.StatusConflict, "Nie udało się utworzyć instalacji: ta instalacja mogła zostać już skonfigurowana.")
		return
	}
	s.setFlash(w, r, "success", "Instalacja gotowa. Poniżej znajdziesz adres do powiadomień bankowych.")
	raw, err := s.Store.CreateSession(r.Context(), u)
	if err == nil {
		http.SetCookie(w, s.cookie(cookieSession, raw, int(store.SessionTTL.Seconds())))
	}
	s.redirect(w, r, "/treasurer/payments")
}

// ---- demo quick login (only meaningful with DEMO_MODE and wrapper-seeded accounts) ----

func (s *Server) handleDemoLogin(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.Demo {
		s.fail(w, r, http.StatusNotFound, "Nie znaleziono strony.")
		return
	}
	email := store.NormalizeEmail(r.PostFormValue("email"))
	for _, a := range s.demoAccounts(r) {
		if store.NormalizeEmail(a.Email) == email {
			if u, err := s.Store.UserByEmail(r.Context(), email); err == nil {
				s.startSession(w, r, u)
				return
			}
		}
	}
	s.fail(w, r, http.StatusForbidden, "To konto nie jest dostępne w demo.")
}
