package store

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	pdfBytes = append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), 64)...)
)

func (f *fixture) withReceipts() int64 {
	f.t.Helper()
	f.s.AttachDir = filepath.Join(f.t.TempDir(), "attachments")
	id, err := f.s.ManualExpense(f.ctx, f.class.ID, "Kwiaty", 2500, f.now)
	f.must(err)
	return id
}

func TestReceiptIsStoredByContentServedAndReplaced(t *testing.T) {
	f := newFixture(t)
	id := f.withReceipts()
	f.must(f.s.SaveExpenseAttachment(f.ctx, f.class.ID, id, bytes.NewReader(pngBytes)))
	a, err := f.s.ExpenseAttachment(f.ctx, f.class.ID, id)
	f.must(err)
	if a.ContentType != "image/png" || !a.Inline || !strings.HasSuffix(a.Path, ".png") {
		t.Fatalf("%+v", a)
	}
	if st, err := os.Stat(a.Path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("receipts must be private files: %v %v", st, err)
	}
	if entries, _ := f.s.ManualEntries(f.ctx, f.class.ID, 5); !entries[0].HasReceipt {
		t.Fatal("the history must know about the receipt")
	}
	// A PDF replaces the image; the old file is removed and PDFs are downloads, not inline.
	f.must(f.s.SaveExpenseAttachment(f.ctx, f.class.ID, id, bytes.NewReader(pdfBytes)))
	b, _ := f.s.ExpenseAttachment(f.ctx, f.class.ID, id)
	if b.ContentType != "application/pdf" || b.Inline {
		t.Fatalf("%+v", b)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatal("the replaced file must be deleted")
	}
	data, _ := os.ReadFile(b.Path)
	if !bytes.Equal(data, pdfBytes) {
		t.Fatal("stored bytes differ from the upload")
	}
	f.must(f.s.DeleteExpenseAttachment(f.ctx, f.class.ID, id))
	if _, err := f.s.ExpenseAttachment(f.ctx, f.class.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if _, err := os.Stat(b.Path); !os.IsNotExist(err) {
		t.Fatal("the file must be removed with the receipt")
	}
}

func TestReceiptRejectsWrongTypesOversizeAndOtherClasses(t *testing.T) {
	f := newFixture(t)
	id := f.withReceipts()
	for name, body := range map[string][]byte{
		"html pretending to be an image": []byte("<html><script>alert(1)</script></html>"),
		"plain text":                     []byte("just text"),
		"executable":                     []byte("MZ\x90\x00\x03\x00"),
		"empty":                          {},
	} {
		if err := f.s.SaveExpenseAttachment(f.ctx, f.class.ID, id, bytes.NewReader(body)); !errors.Is(err, ErrAttachmentType) {
			t.Errorf("%s: %v, want ErrAttachmentType", name, err)
		}
	}
	big := io.MultiReader(bytes.NewReader(pngBytes), bytes.NewReader(make([]byte, MaxAttachmentBytes)))
	if err := f.s.SaveExpenseAttachment(f.ctx, f.class.ID, id, big); !errors.Is(err, ErrAttachmentSize) {
		t.Fatalf("oversize: %v", err)
	}
	exact := io.MultiReader(bytes.NewReader(pngBytes), bytes.NewReader(make([]byte, MaxAttachmentBytes-len(pngBytes))))
	f.must(f.s.SaveExpenseAttachment(f.ctx, f.class.ID, id, exact)) // exactly at the limit is fine
	files, _ := filepath.Glob(filepath.Join(f.s.AttachDir, "*", "*"))
	if len(files) != 1 {
		t.Fatalf("rejected uploads must leave no files behind: %v", files)
	}
	other, _ := f.s.CreateClass(f.ctx, "5B")
	if err := f.s.SaveExpenseAttachment(f.ctx, other.ID, id, bytes.NewReader(pngBytes)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another class must not attach to this expense: %v", err)
	}
	if _, err := f.s.ExpenseAttachment(f.ctx, other.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another class must not read it: %v", err)
	}
	if err := f.s.DeleteExpenseAttachment(f.ctx, other.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another class must not delete it: %v", err)
	}
}

func TestReceiptPathsCannotEscapeTheStorageDirectory(t *testing.T) {
	f := newFixture(t)
	id := f.withReceipts()
	secret := filepath.Join(filepath.Dir(f.s.AttachDir), "secret.png")
	f.must(os.WriteFile(secret, pngBytes, 0o600))
	for _, evil := range []string{"../secret.png", "1/../../secret.png", secret, "", "1/x.exe", "a/b/c.png"} {
		_, err := f.s.DB.Exec(`UPDATE expenses SET attachment_path = ? WHERE id = ?`, evil, id)
		f.must(err)
		if a, err := f.s.ExpenseAttachment(f.ctx, f.class.ID, id); err == nil {
			t.Errorf("path %q must not be served, got %+v", evil, a)
		}
	}
}

func TestReceiptsAreOffWithoutAStorageDirectory(t *testing.T) {
	f := newFixture(t)
	id, err := f.s.ManualExpense(f.ctx, f.class.ID, "Kwiaty", 2500, f.now)
	f.must(err)
	if err := f.s.SaveExpenseAttachment(f.ctx, f.class.ID, id, bytes.NewReader(pngBytes)); !errors.Is(err, ErrAttachmentsOff) {
		t.Fatalf("%v", err)
	}
}
