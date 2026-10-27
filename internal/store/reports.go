package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Cell is one report value. Money cells carry grosze so HTML and CSV can format them differently.
type Cell struct {
	Text    string
	Minor   int64
	IsMoney bool
}

// Money builds an amount cell.
func Money(minor int64) Cell { return Cell{Minor: minor, IsMoney: true} }

// Text builds a text cell.
func Text(s string) Cell { return Cell{Text: s} }

// Column describes a report column; numeric columns are right-aligned.
type Column struct {
	Name string
	Num  bool
}

// Report is a generic, printable table with a short summary. The same value drives the HTML,
// print and CSV views, so the three always agree.
type Report struct {
	Kind      string
	Title     string
	Subtitle  string
	ClassName string
	Columns   []Column
	Rows      [][]Cell
	Summary   []SummaryLine
}

// SummaryLine is a label/value pair shown under the table.
type SummaryLine struct {
	Label string
	Value Cell
}

// ReportKinds lists the available reports in display order.
var ReportKinds = []struct{ Kind, Title, Description string }{
	{"contributions", "Rozliczenie składek", "Kto zapłacił, a kto jeszcze nie — dla wszystkich lub wybranej składki."},
	{"outstanding", "Zaległości", "Uczniowie z nieopłaconymi składkami, kwoty i liczba dni po terminie."},
	{"cash", "Raport kasowy", "Wpłaty i wydatki w wybranym okresie z saldem początkowym i końcowym."},
	{"parents", "Rozliczenie rodziców", "Należności, wpłaty i saldo każdego rodzica z podziałem na dzieci."},
	{"transfers", "Przelewy bankowe", "Przelewy z banku w okresie: rozpoznane, nieprzypisane i do sprawdzenia."},
}

func (s *Store) className(ctx context.Context, classID int64) string {
	var n string
	_ = s.DB.QueryRowContext(ctx, `SELECT name FROM classes WHERE id = ?`, classID).Scan(&n)
	return n
}

func (s *Store) day(t time.Time) string { return t.In(s.Loc).Format("02.01.2006") }

func (s *Store) dayp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return s.day(*t)
}

// ReportContributions settles every obligation of the class, or of one contribution (id != 0).
func (s *Store) ReportContributions(ctx context.Context, classID, contributionID int64) (Report, error) {
	r := Report{Kind: "contributions", Title: "Rozliczenie składek", ClassName: s.className(ctx, classID),
		Columns: []Column{{"Składka", false}, {"Termin", false}, {"Uczeń", false}, {"Kwota", true}, {"Status", false}, {"Data wpłaty", false}, {"Kod płatności", false}}}
	q := `WHERE sp.class_id = ? AND st.deleted_at IS NULL`
	args := []any{classID}
	if contributionID != 0 {
		q += ` AND sp.contribution_id = ?`
		args = append(args, contributionID)
		var title string
		if err := s.DB.QueryRowContext(ctx, `SELECT title FROM contributions WHERE id = ? AND class_id = ?`, contributionID, classID).Scan(&title); err != nil {
			return r, wrapNotFound(err)
		}
		r.Subtitle = "Składka: " + title
	}
	sps, err := querySPs(ctx, s.DB, q+` ORDER BY sp.label COLLATE NOCASE, st.last_name COLLATE NOCASE, st.first_name COLLATE NOCASE`, args...)
	if err != nil {
		return r, err
	}
	var due, paid int64
	var nPaid int
	for _, sp := range sps {
		r.Rows = append(r.Rows, []Cell{Text(sp.Label), Text(s.dayp(sp.DueAt)), Text(sp.StudentName), Money(sp.AmountMinor), Text(spStatusPL(sp.Status)), Text(s.dayp(sp.PaidAt)), Text(sp.Code)})
		due += sp.AmountMinor
		if sp.Status == SPPaid {
			paid += sp.AmountMinor
			nPaid++
		}
	}
	r.Summary = []SummaryLine{{"Pozycji", Text(fmt.Sprint(len(sps)))}, {"Opłaconych", Text(fmt.Sprint(nPaid))}, {"Należności razem", Money(due)}, {"Wpłacono", Money(paid)}, {"Pozostało do zapłaty", Money(due - paid)}}
	return r, nil
}

func spStatusPL(s string) string {
	switch s {
	case SPPaid:
		return "Zapłacono"
	case SPPartial:
		return "Częściowo"
	}
	return "Do zapłaty"
}

// ReportOutstanding lists students with unpaid obligations.
func (s *Store) ReportOutstanding(ctx context.Context, classID int64) (Report, error) {
	r := Report{Kind: "outstanding", Title: "Zaległości", Subtitle: "Stan na " + s.day(s.Now()), ClassName: s.className(ctx, classID),
		Columns: []Column{{"Uczeń", false}, {"Rodzice", false}, {"Nieopłacone pozycje", true}, {"Kwota zaległa", true}, {"W tym po terminie", true}, {"Najstarszy termin", false}, {"Dni po terminie", true}}}
	rows, err := s.DB.QueryContext(ctx, `SELECT st.id, st.first_name || ' ' || st.last_name, COUNT(sp.id), SUM(sp.amount_minor),
		COALESCE(SUM(CASE WHEN sp.due_at IS NOT NULL AND sp.due_at < ? THEN sp.amount_minor END),0), MIN(sp.due_at)
		FROM student_payments sp JOIN students st ON st.id = sp.student_id
		WHERE sp.class_id = ? AND sp.status <> 'paid' AND st.deleted_at IS NULL GROUP BY st.id ORDER BY 4 DESC, 2`, ts(s.Now()), classID)
	if err != nil {
		return r, err
	}
	type line struct {
		id        int64
		name      string
		n         int
		sum, over int64
		oldest    sql.NullString
	}
	var lines []line
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.id, &l.name, &l.n, &l.sum, &l.over, &l.oldest); err != nil {
			rows.Close()
			return r, err
		}
		lines = append(lines, l)
	}
	rows.Close()
	parents, err := s.parentNames(ctx, classID)
	if err != nil {
		return r, err
	}
	var total, overdue int64
	var count int
	for _, l := range lines {
		oldest := parseNullTime(l.oldest)
		days := ""
		if oldest != nil && oldest.Before(s.Now()) {
			days = fmt.Sprint(int(s.Now().Sub(*oldest).Hours() / 24))
		}
		r.Rows = append(r.Rows, []Cell{Text(l.name), Text(strings.Join(parents[l.id], ", ")), Text(fmt.Sprint(l.n)), Money(l.sum), Money(l.over), Text(s.dayp(oldest)), Text(days)})
		total += l.sum
		overdue += l.over
		count += l.n
	}
	r.Summary = []SummaryLine{{"Uczniów z zaległościami", Text(fmt.Sprint(len(lines)))}, {"Nieopłaconych pozycji", Text(fmt.Sprint(count))}, {"Zaległości razem", Money(total)}, {"W tym po terminie", Money(overdue)}}
	return r, nil
}

func (s *Store) parentNames(ctx context.Context, classID int64) (map[int64][]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT sp.student_id, u.name FROM student_parents sp JOIN users u ON u.id = sp.user_id JOIN students st ON st.id = sp.student_id WHERE st.class_id = ? ORDER BY u.name`, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = append(out[id], n)
	}
	return out, rows.Err()
}

// ReportCash is a cash statement for [from, to] (inclusive days, local time) with opening and closing balance.
func (s *Store) ReportCash(ctx context.Context, classID int64, from, to time.Time) (Report, error) {
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, s.Loc)
	end := time.Date(to.Year(), to.Month(), to.Day(), 23, 59, 59, 0, s.Loc)
	r := Report{Kind: "cash", Title: "Raport kasowy", Subtitle: "Okres: " + s.day(start) + " – " + s.day(end), ClassName: s.className(ctx, classID),
		Columns: []Column{{"Data", false}, {"Rodzaj", false}, {"Opis", false}, {"Uczeń", false}, {"Sposób", false}, {"Kwota", true}, {"Saldo po", true}}}
	var opening int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT balance_after_minor FROM ledger WHERE class_id = ? AND tx_date < ? ORDER BY tx_date DESC, id DESC LIMIT 1),0)`, classID, ts(start)).Scan(&opening); err != nil {
		return r, err
	}
	rows, err := s.ledgerRows(ctx, `WHERE l.class_id = ? AND l.tx_date >= ? AND l.tx_date <= ? ORDER BY l.tx_date ASC, l.id ASC`, classID, ts(start), ts(end))
	if err != nil {
		return r, err
	}
	var in, out int64
	closing := opening
	for _, l := range rows {
		kind, amount := "Wpłata", l.AmountMinor
		if l.Kind == "expense" {
			kind, amount = "Wydatek", -l.AmountMinor
			out += l.AmountMinor
		} else {
			in += l.AmountMinor
		}
		closing = l.BalanceAfter
		r.Rows = append(r.Rows, []Cell{Text(s.day(l.Date)), Text(kind), Text(l.Description), Text(l.StudentName), Text(l.Method), Money(amount), Money(l.BalanceAfter)})
	}
	r.Summary = []SummaryLine{{"Saldo początkowe", Money(opening)}, {"Wpłaty", Money(in)}, {"Wydatki", Money(out)}, {"Saldo końcowe", Money(closing)}}
	return r, nil
}

// ReportParents settles each parent's children: due, paid and open amounts. A child with two parents
// appears under both, so the summary is computed per obligation, not per parent.
func (s *Store) ReportParents(ctx context.Context, classID int64) (Report, error) {
	r := Report{Kind: "parents", Title: "Rozliczenie rodziców", Subtitle: "Stan na " + s.day(s.Now()), ClassName: s.className(ctx, classID),
		Columns: []Column{{"Rodzic", false}, {"E-mail", false}, {"Dzieci", false}, {"Należności razem", true}, {"Wpłacono", true}, {"Do zapłaty", true}}}
	rows, err := s.DB.QueryContext(ctx, `SELECT u.name, u.email, GROUP_CONCAT(DISTINCT st.first_name || ' ' || st.last_name),
		COALESCE(SUM(sp.amount_minor),0), COALESCE(SUM(CASE WHEN sp.status = 'paid' THEN sp.amount_minor END),0)
		FROM student_parents x JOIN users u ON u.id = x.user_id JOIN students st ON st.id = x.student_id AND st.class_id = ? AND st.deleted_at IS NULL
		LEFT JOIN student_payments sp ON sp.student_id = st.id GROUP BY u.id ORDER BY u.name COLLATE NOCASE`, classID)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, email, kids string
		var due, paid int64
		if err := rows.Scan(&name, &email, &kids, &due, &paid); err != nil {
			return r, err
		}
		r.Rows = append(r.Rows, []Cell{Text(name), Text(email), Text(kids), Money(due), Money(paid), Money(due - paid)})
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	var due, paid int64
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_minor),0), COALESCE(SUM(CASE WHEN status='paid' THEN amount_minor END),0) FROM student_payments WHERE class_id = ?`, classID).Scan(&due, &paid)
	r.Summary = []SummaryLine{{"Rodziców", Text(fmt.Sprint(len(r.Rows)))}, {"Należności w klasie", Money(due)}, {"Wpłacono", Money(paid)}, {"Do zapłaty", Money(due - paid)}}
	return r, nil
}

// ReportTransfers lists bank transfers announced in [from, to].
func (s *Store) ReportTransfers(ctx context.Context, classID int64, from, to time.Time) (Report, error) {
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, s.Loc)
	end := time.Date(to.Year(), to.Month(), to.Day(), 23, 59, 59, 0, s.Loc)
	r := Report{Kind: "transfers", Title: "Przelewy bankowe", Subtitle: "Okres: " + s.day(start) + " – " + s.day(end), ClassName: s.className(ctx, classID),
		Columns: []Column{{"Data", false}, {"Nadawca", false}, {"Tytuł", false}, {"Kwota", true}, {"Status", false}, {"Cel", false}}}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+transferCols+` FROM transfers WHERE deleted_at IS NULL AND (class_id = ? OR class_id IS NULL) AND transferred_at >= ? AND transferred_at <= ? ORDER BY transferred_at, id`, classID, ts(start), ts(end))
	if err != nil {
		return r, err
	}
	var list []Transfer
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			rows.Close()
			return r, err
		}
		list = append(list, t)
	}
	rows.Close()
	var total, matched int64
	var unmatched int
	for _, t := range list {
		status, target := "Nieprzypisana", ""
		switch {
		case t.ReviewReason != "":
			status = "Do sprawdzenia"
		case t.PaymentID != nil:
			status = "Rozpoznano"
			if p, err := s.PaymentByID(ctx, classID, *t.PaymentID); err == nil {
				target = p.Label()
				if p.AmountMinor != t.AmountMinor {
					status = "Rozpoznano (kwota inna niż należna)"
				}
			}
			matched += t.AmountMinor
		default:
			unmatched++
		}
		total += t.AmountMinor
		r.Rows = append(r.Rows, []Cell{Text(s.day(t.TransferredAt)), Text(t.Sender), Text(t.Title), Money(t.AmountMinor), Text(status), Text(target)})
	}
	r.Summary = []SummaryLine{{"Przelewów", Text(fmt.Sprint(len(list)))}, {"Kwota razem", Money(total)}, {"W tym rozpoznane", Money(matched)}, {"Nieprzypisanych", Text(fmt.Sprint(unmatched))}}
	return r, nil
}
