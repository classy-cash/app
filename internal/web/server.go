// Package web is the HTTP layer: routing, sessions, CSRF, templates and the page handlers.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/classy-cash/app/assets"
	"github.com/classy-cash/app/internal/auth"
	"github.com/classy-cash/app/internal/bankmail"
	"github.com/classy-cash/app/internal/config"
	"github.com/classy-cash/app/internal/db"
	"github.com/classy-cash/app/internal/mail"
	"github.com/classy-cash/app/internal/store"
	"github.com/classy-cash/app/internal/version"
)

//go:embed templates static
var embedded embed.FS

const (
	cookieSession = "cc_session"
	cookieCSRF    = "cc_csrf"
	cookieFlash   = "cc_flash"
)

// Server wires the application together.
type Server struct {
	Cfg        *config.Config
	Store      *store.Store
	Log        *slog.Logger
	Sender     mail.Sender
	Migrations fs.FS

	mxMu    sync.Mutex
	mxCache map[string]mxEntry

	tpl     map[string]*template.Template
	limiter *limiter
	started time.Time
}

// New parses templates and builds a server.
func New(cfg *config.Config, st *store.Store, log *slog.Logger, sender mail.Sender, migrations fs.FS) (*Server, error) {
	s := &Server{Cfg: cfg, Store: st, Log: log, Sender: sender, Migrations: migrations, limiter: newLimiter(), started: time.Now()}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

type ctxKey int

const stateKey ctxKey = 1

// reqState is what the middleware resolved for one request.
type reqState struct {
	user       *store.User
	sessionRaw string
	class      *store.Class
	classes    []store.Class
	role       string
	csrfCookie string
}

func state(r *http.Request) *reqState {
	if v, ok := r.Context().Value(stateKey).(*reqState); ok {
		return v
	}
	return &reqState{}
}

// Handler builds the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	var h http.Handler = mux
	h = s.csrf(h)
	h = s.loadState(h)
	h = s.accessLog(h)
	h = s.secureHeaders(h)
	h = s.recoverer(h)
	return h
}

// ---- middleware ----

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("panic", "path", r.URL.Path, "panic", fmt.Sprint(rec))
				s.fail(w, r, http.StatusInternalServerError, "Coś poszło nie tak. Spróbuj ponownie za chwilę.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if s.Cfg.Secure() {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/health" || strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		// Paths and statuses only: tokens live in paths, so route templates are logged for those.
		p := r.URL.Path
		if strings.HasPrefix(p, "/login/link/") {
			p = "/login/link/{token}"
		} else if strings.HasPrefix(p, "/reset-password/reset/") {
			p = "/reset-password/reset/{token}"
		}
		s.Log.Info("request", "method", r.Method, "path", p, "status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) loadState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/health" || r.URL.Path == "/ping" || r.URL.Path == "/ready" || r.URL.Path == "/api/bank-mail" {
			next.ServeHTTP(w, r)
			return
		}
		st := &reqState{}
		if c, err := r.Cookie(cookieCSRF); err == nil && len(c.Value) >= 32 {
			st.csrfCookie = c.Value
		} else {
			st.csrfCookie = randomToken()
			http.SetCookie(w, s.cookie(cookieCSRF, st.csrfCookie, 0))
		}
		if c, err := r.Cookie(cookieSession); err == nil && c.Value != "" {
			u, sess, err := s.Store.SessionUser(r.Context(), c.Value)
			if err == nil {
				st.user, st.sessionRaw = &u, c.Value
				st.classes, _ = s.Store.ClassesFor(r.Context(), u)
				st.class = pickClass(st.classes, sess.ClassID.Int64)
				if st.class != nil {
					st.role, _ = s.Store.RoleIn(r.Context(), u, st.class.ID)
				}
			} else if errors.Is(err, store.ErrNotFound) {
				http.SetCookie(w, s.cookie(cookieSession, "", -1))
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), stateKey, st)))
	})
}

func pickClass(classes []store.Class, wanted int64) *store.Class {
	for i := range classes {
		if classes[i].ID == wanted {
			return &classes[i]
		}
	}
	if len(classes) > 0 {
		return &classes[0]
	}
	return nil
}

// csrf rejects state-changing requests without a valid token or with a foreign Origin.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/bank-mail" {
			next.ServeHTTP(w, r) // authenticated by HMAC, not by cookies
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || !s.sameHost(u.Host, r.Host) {
				s.fail(w, r, http.StatusForbidden, "Żądanie odrzucone (nieprawidłowe źródło).")
				return
			}
		}
		tok := r.Header.Get("X-CSRF-Token")
		if tok == "" {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") && strings.HasPrefix(r.URL.Path, "/treasurer/") {
				// Receipt uploads: bounded, and only the first MB is held in memory (the rest spills to a temp file).
				r.Body = http.MaxBytesReader(w, r.Body, store.MaxAttachmentBytes+(1<<20))
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					s.fail(w, r, http.StatusRequestEntityTooLarge, "Plik jest zbyt duży (limit 5 MB) albo formularz jest nieprawidłowy.")
					return
				}
			} else {
				r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
				if err := r.ParseForm(); err != nil {
					s.fail(w, r, http.StatusRequestEntityTooLarge, "Formularz jest zbyt duży.")
					return
				}
			}
			tok = r.PostFormValue("_csrf")
		}
		if !auth.CheckCSRF(s.Cfg.SessionSecret, state(r).csrfCookie, tok) {
			s.fail(w, r, http.StatusForbidden, "Sesja formularza wygasła. Odśwież stronę i spróbuj ponownie.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sameHost(origin, host string) bool {
	if strings.EqualFold(origin, host) {
		return true
	}
	if u, err := url.Parse(s.Cfg.BaseURL); err == nil {
		return strings.EqualFold(origin, u.Host)
	}
	return false
}

func (s *Server) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.Cfg.Secure(), SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ---- guards ----

type handler = func(w http.ResponseWriter, r *http.Request)

// needUser redirects anonymous visitors to the login page.
func (s *Server) needUser(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if state(r).user == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// needInstalled sends everybody to onboarding while the instance is unclaimed.
func (s *Server) needInstalled(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if n, err := s.Store.CountUsers(r.Context()); err == nil && n == 0 && !s.Cfg.Demo {
			http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// needManager requires treasurer-level access to the current class.
func (s *Server) needManager(h handler) http.HandlerFunc {
	return s.needInstalled(s.needUser(func(w http.ResponseWriter, r *http.Request) {
		st := state(r)
		if st.class == nil {
			s.fail(w, r, http.StatusForbidden, "Nie należysz do żadnej klasy.")
			return
		}
		if !s.Store.CanManage(r.Context(), *st.user, st.class.ID) {
			if s.Store.IsArchived(r.Context(), st.class.ID) {
				s.fail(w, r, http.StatusForbidden, "Ta klasa jest zarchiwizowana i dostępna tylko do odczytu.")
				return
			}
			s.fail(w, r, http.StatusForbidden, "Ta sekcja jest dostępna tylko dla skarbnika klasy.")
			return
		}
		h(w, r)
	}))
}

// needViewer allows read access to the class panel: treasurer, administrator and class council.
func (s *Server) needViewer(h handler) http.HandlerFunc {
	return s.needInstalled(s.needUser(func(w http.ResponseWriter, r *http.Request) {
		st := state(r)
		if st.class == nil || !s.Store.CanView(r.Context(), *st.user, st.class.ID) {
			s.fail(w, r, http.StatusForbidden, "Ta sekcja jest dostępna tylko dla skarbnika i rady klasowej.")
			return
		}
		h(w, r)
	}))
}

func roleLabel(admin bool, role string) string {
	switch {
	case admin:
		return "Administrator"
	case role == store.RoleTreasurer:
		return "Skarbnik"
	case role == store.RolePresident:
		return "Przewodniczący rady klasowej"
	case role == store.RoleVicePresident:
		return "Wiceprzewodniczący rady klasowej"
	}
	return "Rodzic"
}

func (s *Server) needAdmin(h handler) http.HandlerFunc {
	return s.needInstalled(s.needUser(func(w http.ResponseWriter, r *http.Request) {
		if !state(r).user.IsAdmin {
			s.fail(w, r, http.StatusForbidden, "Ta sekcja jest dostępna tylko dla administratora.")
			return
		}
		h(w, r)
	}))
}

// ---- flash ----

type flash struct {
	Kind string `json:"k"`
	Text string `json:"t"`
}

func (s *Server) setFlash(w http.ResponseWriter, r *http.Request, kind, text string) {
	var list []flash
	if c, err := r.Cookie(cookieFlash); err == nil {
		list = decodeFlash(c.Value)
	}
	list = append(list, flash{kind, text})
	b, _ := json.Marshal(list)
	http.SetCookie(w, s.cookie(cookieFlash, base64.RawURLEncoding.EncodeToString(b), 60))
}

func decodeFlash(v string) []flash {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return nil
	}
	var list []flash
	_ = json.Unmarshal(raw, &list)
	if len(list) > 5 {
		list = list[:5]
	}
	return list
}

// ---- rendering ----

type pageData struct {
	Title     string
	AppName   string
	User      *store.User
	Role      string
	Class     *store.Class
	Classes   []store.Class
	IsManager bool
	IsViewer  bool
	RoleLabel string
	Flashes   []flash
	CSRF      string
	Demo      bool
	NextAt    int64
	Active    string
	Version   string
	Path      string
	Layout    string
	Data      any
}

func (s *Server) page(r *http.Request, title, active string, data any) *pageData {
	st := state(r)
	p := &pageData{Title: title, AppName: s.Store.AppName(r.Context()), User: st.user, Role: st.role, Class: st.class, Classes: st.classes,
		CSRF: auth.CSRF(s.Cfg.SessionSecret, st.csrfCookie), Demo: s.Cfg.Demo, Active: active, Version: version.Version, Path: r.URL.Path, Data: data}
	if st.user != nil && st.class != nil {
		p.IsManager = s.Store.CanManage(r.Context(), *st.user, st.class.ID)
		p.IsViewer = s.Store.CanView(r.Context(), *st.user, st.class.ID)
	}
	if st.user != nil {
		p.RoleLabel = roleLabel(st.user.IsAdmin, st.role)
	}
	if s.Cfg.Demo {
		p.NextAt = nextQuarter(time.Now()).Unix()
	}
	return p
}

// nextQuarter is the next wall-clock :00/:15/:30/:45 — only for the demo banner countdown. The
// reset itself is performed by the external demo wrapper.
func nextQuarter(t time.Time) time.Time {
	return t.Truncate(15 * time.Minute).Add(15 * time.Minute)
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, p *pageData, status int) {
	t, ok := s.tpl[name]
	if !ok {
		s.Log.Error("missing template", "name", name)
		http.Error(w, "template missing", http.StatusInternalServerError)
		return
	}
	if c, err := r.Cookie(cookieFlash); err == nil {
		p.Flashes = decodeFlash(c.Value)
		http.SetCookie(w, s.cookie(cookieFlash, "", -1))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	entry := p.Layout
	if entry == "" {
		entry = "layout_plain"
	}
	if err := t.ExecuteTemplate(w, entry, p); err != nil {
		s.Log.Error("render", "template", name, "err", err)
	}
}

// fragment renders a layout-free template.
func (s *Server) fragment(w http.ResponseWriter, r *http.Request, name, define string, data any) {
	t, ok := s.tpl[name]
	if !ok {
		http.Error(w, "template missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := t.ExecuteTemplate(w, define, data); err != nil {
		s.Log.Error("fragment", "template", name, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-CSRF-Token") != "" {
		s.json(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	p := s.page(r, "Błąd", "", map[string]any{"Status": status, "Message": msg})
	s.render(w, r, "error", p, status)
}

func (s *Server) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) parseTemplates() error {
	s.tpl = map[string]*template.Template{}
	funcs := s.funcs()
	layouts, err := fs.Glob(embedded, "templates/layout_*.html")
	if err != nil {
		return err
	}
	partials, _ := fs.Glob(embedded, "templates/partials/*.html")
	pages, err := fs.Glob(embedded, "templates/pages/*.html")
	if err != nil {
		return err
	}
	for _, p := range pages {
		name := strings.TrimSuffix(path.Base(p), ".html")
		files := append(append([]string{}, layouts...), partials...)
		files = append(files, p)
		t, err := template.New(name).Funcs(funcs).ParseFS(embedded, files...)
		if err != nil {
			return fmt.Errorf("template %s: %w", name, err)
		}
		s.tpl[name] = t
	}
	return nil
}

// ---- routing ----

func (s *Server) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(embedded, "static")
	mux.Handle("GET /assets/app/", http.StripPrefix("/assets/app/", cacheStatic(http.FileServerFS(static))))
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", cacheStatic(http.FileServerFS(assets.FS))))
	mux.HandleFunc("GET /site.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		b, _ := assets.FS.ReadFile("site.webmanifest")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/assets/images/favicon-32x32.png", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ping", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.HandleFunc("GET /version", s.handleVersion)

	mux.Handle("/api/bank-mail", &bankmail.Handler{Cfg: s.Cfg.Bank, Store: s.Store, Log: s.Log})

	s.authRoutes(mux)
	s.parentRoutes(mux)
	s.treasurerRoutes(mux)
	s.reportRoutes(mux)
	s.adminRoutes(mux)

	mux.HandleFunc("GET /{$}", s.needInstalled(s.needUser(s.handleHome)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, http.StatusNotFound, "Nie znaleziono strony.")
	})
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

// ---- health ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok\n"))
}

// handleReady verifies the database answers, the schema is current and the outbox is not stuck.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	res := map[string]any{"version": version.Version, "mode": map[bool]string{true: "demo", false: "normal"}[s.Cfg.Demo]}
	ok := true
	if err := s.Store.DB.PingContext(ctx); err != nil {
		res["database"], ok = "unavailable", false
	} else {
		res["database"] = "ok"
	}
	if pending, err := db.Pending(s.Store.DB, s.Migrations); err != nil || len(pending) > 0 {
		res["migrations"], ok = "pending", false
	} else {
		res["migrations"] = "current"
	}
	if st, err := s.Store.OutboxStats(ctx); err == nil {
		res["mail_pending"], res["mail_failed"] = st.Pending, st.Failed
		res["mail_oldest_pending_seconds"] = int(st.OldestPending.Seconds())
		res["mail_driver"] = s.Cfg.Mail.Driver
	}
	res["bank_webhook"] = map[bool]string{true: "enabled", false: "disabled"}[s.Cfg.Bank.Enabled()]
	if last := s.Store.LastBankEvent(ctx); last != nil {
		res["bank_last_delivery"] = last.UTC().Format(time.RFC3339)
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	res["ok"] = ok
	s.json(w, status, res)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	s.json(w, 200, map[string]any{"version": version.Version, "commit": version.Commit, "built": version.Date, "config_version": version.ConfigVersion})
}

// ---- rate limiting ----

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

// allow records an attempt and reports whether it is within max attempts per window.
func (l *limiter) allow(key string, max int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cut := now.Add(-window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	if len(l.hits) > 5000 { // bound memory under scanning
		for k, v := range l.hits {
			if len(v) == 0 || v[len(v)-1].Before(cut) {
				delete(l.hits, k)
			}
		}
	}
	return true
}

// clientIP returns the caller address, honouring the trusted proxy's X-Forwarded-For when enabled.
func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustProx {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			parts := strings.Split(xf, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
