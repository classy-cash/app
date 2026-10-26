package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/classy-cash/app/internal/store"
)

func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/mail", s.needAdmin(s.handleMailList))
	mux.HandleFunc("GET /admin/mail/{id}", s.needAdmin(s.handleMailView))
	mux.HandleFunc("GET /admin/classes", s.needAdmin(s.handleClasses))
	mux.HandleFunc("POST /admin/classes", s.needAdmin(s.handleClassCreate))
}

func (s *Server) handleMailList(w http.ResponseWriter, r *http.Request) {
	list, _ := s.Store.RecentMail(r.Context(), 100)
	st, _ := s.Store.OutboxStats(r.Context())
	p := s.page(r, "Wiadomości wychodzące", "mail", map[string]any{"Mail": list, "Stats": st, "Driver": s.Cfg.Mail.Driver})
	p.Layout = "layout_treasurer"
	s.render(w, r, "admin_mail", p, http.StatusOK)
}

func (s *Server) handleMailView(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	m, err := s.Store.MailByID(r.Context(), id)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Nie znaleziono wiadomości.")
		return
	}
	p := s.page(r, m.Subject, "mail", map[string]any{"Mail": m})
	p.Layout = "layout_treasurer"
	s.render(w, r, "admin_mail_view", p, http.StatusOK)
}

func (s *Server) handleClasses(w http.ResponseWriter, r *http.Request) {
	classes, _ := s.Store.ClassesFor(r.Context(), *state(r).user)
	type row struct {
		Class     store.Class
		Treasurer string
	}
	var rows []row
	for _, c := range classes {
		members, _ := s.Store.Members(r.Context(), c.ID)
		t := ""
		for _, m := range members {
			if m.Role == store.RoleTreasurer {
				t = m.User.Name + " <" + m.User.Email + ">"
			}
		}
		rows = append(rows, row{c, t})
	}
	p := s.page(r, "Klasy", "classes", map[string]any{"Rows": rows})
	p.Layout = "layout_treasurer"
	s.render(w, r, "admin_classes", p, http.StatusOK)
}

func (s *Server) handleClassCreate(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.CreateClass(r.Context(), r.PostFormValue("name"))
	if err != nil {
		s.flashErr(w, r, "/admin/classes", err)
		return
	}
	if email := strings.TrimSpace(r.PostFormValue("treasurer_email")); email != "" {
		u, err := s.ensureParentFor(r, email, r.PostFormValue("treasurer_name"), c)
		if err == nil {
			err = s.Store.SetMember(r.Context(), c.ID, u.ID, store.RoleTreasurer)
		}
		if err != nil {
			s.flashErr(w, r, "/admin/classes", err)
			return
		}
	}
	s.setFlash(w, r, "success", "Klasa została utworzona.")
	s.redirect(w, r, "/admin/classes")
}

// ensureParentFor is ensureParent for a class that is not the session's current one.
func (s *Server) ensureParentFor(r *http.Request, email, name string, c store.Class) (store.User, error) {
	st := state(r)
	prev := st.class
	st.class = &c
	defer func() { st.class = prev }()
	return s.ensureParent(r, email, name)
}
