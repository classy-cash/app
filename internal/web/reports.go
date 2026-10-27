package web

import (
	"encoding/csv"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/money"
	"github.com/classy-cash/app/internal/store"
)

func (s *Server) reportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /treasurer/reports", s.needViewer(s.handleReportsIndex))
	mux.HandleFunc("GET /treasurer/reports/{kind}", s.needViewer(s.handleReport))
}

// schoolYearStart is 1 September of the current school year.
func (s *Server) schoolYearStart() time.Time {
	n := s.Store.Now().In(s.Cfg.Location)
	y := n.Year()
	if n.Month() < time.September {
		y--
	}
	return time.Date(y, time.September, 1, 0, 0, 0, 0, s.Cfg.Location)
}

func (s *Server) parseDay(v string, def time.Time) time.Time {
	if t, err := time.ParseInLocation("2006-01-02", v, s.Cfg.Location); err == nil {
		return t
	}
	return def
}

func (s *Server) handleReportsIndex(w http.ResponseWriter, r *http.Request) {
	views, _ := s.Store.ContributionViews(r.Context(), s.classID(r))
	today := s.Store.Now().In(s.Cfg.Location)
	p := s.page(r, "Raporty", "reports", map[string]any{
		"Kinds": store.ReportKinds, "Contributions": views,
		"From": s.schoolYearStart().Format("2006-01-02"), "To": today.Format("2006-01-02"),
	})
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_reports", p, http.StatusOK)
}

func (s *Server) buildReport(r *http.Request, kind string) (store.Report, error) {
	ctx, cid := r.Context(), s.classID(r)
	q := r.URL.Query()
	today := s.Store.Now().In(s.Cfg.Location)
	from := s.parseDay(q.Get("from"), s.schoolYearStart())
	to := s.parseDay(q.Get("to"), today)
	if to.Before(from) {
		from, to = to, from
	}
	switch kind {
	case "contributions":
		id, _ := strconv.ParseInt(q.Get("contribution"), 10, 64)
		return s.Store.ReportContributions(ctx, cid, id)
	case "outstanding":
		return s.Store.ReportOutstanding(ctx, cid)
	case "cash":
		return s.Store.ReportCash(ctx, cid, from, to)
	case "parents":
		return s.Store.ReportParents(ctx, cid)
	case "transfers":
		return s.Store.ReportTransfers(ctx, cid, from, to)
	}
	return store.Report{}, store.ErrNotFound
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	rep, err := s.buildReport(r, r.PathValue("kind"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Nie znaleziono raportu.")
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		s.writeCSV(w, rep)
		return
	}
	csvQuery := r.URL.Query()
	csvQuery.Set("format", "csv")
	p := s.page(r, rep.Title, "reports", map[string]any{"Report": rep, "CSVURL": r.URL.Path + "?" + csvQuery.Encode(), "Generated": s.Store.Now()})
	p.Layout = "layout_treasurer"
	s.render(w, r, "t_report", p, http.StatusOK)
}

var plainNumber = regexp.MustCompile(`^-?\d+([.,]\d+)?$`)

// csvSafe neutralises spreadsheet formulas: a cell that starts with = + - or @ would be executed by
// Excel/LibreOffice, so it is prefixed with an apostrophe (numbers are left alone).
func csvSafe(v string) string {
	if v == "" || plainNumber.MatchString(v) {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

// writeCSV streams UTF-8 CSV with a BOM and semicolons, which is what Polish Excel opens correctly;
// amounts use a decimal comma.
func (s *Server) writeCSV(w http.ResponseWriter, rep store.Report) {
	name := "raport-" + rep.Kind + "-" + s.Store.Now().In(s.Cfg.Location).Format("2006-01-02") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("\xef\xbb\xbf"))
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.UseCRLF = true
	cell := func(c store.Cell) string {
		if c.IsMoney {
			return strings.Replace(money.Plain(c.Minor), ".", ",", 1)
		}
		return csvSafe(c.Text)
	}
	_ = cw.Write([]string{csvSafe(rep.Title + " — " + rep.ClassName)})
	if rep.Subtitle != "" {
		_ = cw.Write([]string{csvSafe(rep.Subtitle)})
	}
	head := make([]string, len(rep.Columns))
	for i, c := range rep.Columns {
		head[i] = c.Name
	}
	_ = cw.Write(head)
	for _, row := range rep.Rows {
		out := make([]string, len(row))
		for i, c := range row {
			out[i] = cell(c)
		}
		_ = cw.Write(out)
	}
	_ = cw.Write(nil)
	for _, l := range rep.Summary {
		_ = cw.Write([]string{csvSafe(l.Label), cell(l.Value)})
	}
	cw.Flush()
}
