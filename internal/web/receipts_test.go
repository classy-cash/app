package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/classy-cash/app/internal/auth"
)

var (
	testPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 128)...)
	testPDF = append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("y"), 128)...)
)

// postFile sends a multipart form with one file part, like a browser would.
func (c *client) postFile(path string, fields url.Values, name string, data []byte, withToken bool) (int, string, http.Header) {
	c.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if withToken {
		mw.WriteField("_csrf", c.token())
	}
	for k, vs := range fields {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}
	if name != "" {
		fw, _ := mw.CreateFormFile("attachment", name)
		fw.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", c.h.ts.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestReceiptUploadAndDownloadOverHTTP(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	h.parent("anna@example.com", "Anna Kowalska", cls, "Jan", "Kowalski")

	_, page, _ := tr.get("/treasurer/manual-transactions")
	if !strings.Contains(page, `enctype="multipart/form-data"`) || !strings.Contains(page, "Paragon lub faktura") {
		t.Fatal("expense form must accept a receipt")
	}
	// Book an expense together with its receipt.
	code, _, _ := tr.postFile("/treasurer/manual-transactions", url.Values{"transaction_type": {"expense"}, "amount_pln": {"25,00"}, "expense_description": {"Kwiaty"}}, "paragon.png", testPNG, true)
	if code != 303 {
		t.Fatalf("expense with receipt: %d", code)
	}
	entries, _ := h.st.ManualEntries(ctx, cls.ID, 5)
	if len(entries) != 1 || !entries[0].HasReceipt {
		t.Fatalf("receipt not stored: %+v", entries)
	}
	eid := id(entries[0].ID)

	code, body, hd := tr.get("/treasurer/expenses/" + eid + "/attachment")
	if code != 200 || hd.Get("Content-Type") != "image/png" || !strings.Contains(hd.Get("Content-Security-Policy"), "sandbox") ||
		hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Content-Disposition") != "inline" || hd.Get("Cache-Control") != "private, no-store" || body != string(testPNG) {
		t.Fatalf("serve: %d %v", code, hd)
	}
	if _, page, _ = tr.get("/treasurer/manual-transactions"); !strings.Contains(page, "Usuń paragon") {
		t.Fatal("the row offers delete once a receipt exists")
	}
	// Replace it with a PDF: served as a download.
	if code, _, _ = tr.postFile("/treasurer/expenses/"+eid+"/attachment", nil, "faktura.pdf", testPDF, true); code != 303 {
		t.Fatal(code)
	}
	_, _, hd = tr.get("/treasurer/expenses/" + eid + "/attachment")
	if hd.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(hd.Get("Content-Disposition"), "attachment") {
		t.Fatalf("pdf must download: %v", hd)
	}
	// Anonymous and unrelated users get nothing.
	if code, _, _ := h.client().get("/treasurer/expenses/" + eid + "/attachment"); code == 200 {
		t.Fatal("anonymous visitors must not read receipts")
	}
	anna := h.client()
	anna.login("anna@example.com", "parentpass1")
	if code, _, _ := anna.get("/treasurer/expenses/" + eid + "/attachment"); code != 403 {
		t.Fatalf("parents must not read receipts: %d", code)
	}
	if code, _, _ := anna.postFile("/treasurer/expenses/"+eid+"/attachment", nil, "x.png", testPNG, true); code != 403 {
		t.Fatalf("parents must not upload: %d", code)
	}
	tr.post("/treasurer/expenses/"+eid+"/attachment/delete", url.Values{})
	if code, _, _ := tr.get("/treasurer/expenses/" + eid + "/attachment"); code != 404 {
		t.Fatalf("deleted receipt: %d", code)
	}
}

func TestReceiptCouncilCanReadButNotChange(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	eid, _ := h.st.ManualExpense(ctx, cls.ID, "Kwiaty", 2500, h.st.Now())
	h.st.SaveExpenseAttachment(ctx, cls.ID, eid, bytes.NewReader(testPNG))
	tr.post("/treasurer/members", url.Values{"email": {"ewa@example.com"}, "name": {"Ewa"}, "role": {"president"}})
	ewa, _ := h.st.UserByEmail(ctx, "ewa@example.com")
	hash, _ := auth.HashPassword("councilpass1")
	h.st.SetPassword(ctx, ewa.ID, hash)
	c := h.client()
	c.login("ewa@example.com", "councilpass1")
	if code, _, _ := c.get("/treasurer/expenses/" + id(eid) + "/attachment"); code != 200 {
		t.Fatalf("the council audits the books, including receipts: %d", code)
	}
	if code, _, _ := c.postFile("/treasurer/expenses/"+id(eid)+"/attachment", nil, "x.png", testPNG, true); code != 403 {
		t.Fatalf("council upload: %d", code)
	}
	if code, _, _ := c.post("/treasurer/expenses/"+id(eid)+"/attachment/delete", url.Values{}); code != 403 {
		t.Fatalf("council delete: %d", code)
	}
}

func TestReceiptUploadGuards(t *testing.T) {
	h := newHarness(t, nil)
	tr, cls := h.onboarded()
	ctx := context.Background()
	// A wrong type does not lose the expense, it only skips the receipt.
	code, _, _ := tr.postFile("/treasurer/manual-transactions", url.Values{"transaction_type": {"expense"}, "amount_pln": {"10"}, "expense_description": {"Napój"}}, "evil.jpg", []byte("<script>alert(1)</script>"), true)
	if code != 303 {
		t.Fatal(code)
	}
	entries, _ := h.st.ManualEntries(ctx, cls.ID, 5)
	if len(entries) != 1 || entries[0].HasReceipt {
		t.Fatalf("expense booked, receipt refused: %+v", entries)
	}
	if _, page, _ := tr.get("/treasurer/manual-transactions"); !strings.Contains(page, "dozwolone są pliki PDF") {
		t.Fatal("the treasurer must be told why the receipt was refused")
	}
	// Oversize bodies are cut off by the request limit.
	big := bytes.Repeat([]byte{0}, 7<<20)
	if code, _, _ := tr.postFile("/treasurer/expenses/"+id(entries[0].ID)+"/attachment", nil, "big.png", append(append([]byte{}, testPNG...), big...), true); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: %d, want 413", code)
	}
	// Multipart does not bypass CSRF.
	if code, _, _ := tr.postFile("/treasurer/expenses/"+id(entries[0].ID)+"/attachment", nil, "x.png", testPNG, false); code != 403 {
		t.Fatalf("missing token: %d, want 403", code)
	}
	// Multipart is not accepted outside the treasurer area (it would be parsed with the small limit).
	if code, _, _ := h.client().postFile("/login", url.Values{"email": {"a@b.c"}}, "", nil, false); code == 200 {
		t.Fatal("unexpected success")
	}
}
