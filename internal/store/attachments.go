package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// MaxAttachmentBytes is the largest receipt accepted (a phone photo or a scanned PDF fits comfortably).
const MaxAttachmentBytes = 5 << 20

// ErrAttachmentType means the upload is not a PDF, JPEG, PNG or WebP file (judged by content, not by name).
var ErrAttachmentType = errors.New("unsupported attachment type")

// ErrAttachmentSize means the upload exceeds MaxAttachmentBytes.
var ErrAttachmentSize = errors.New("attachment too large")

// ErrAttachmentsOff means no storage directory is configured (or the instance is a demo).
var ErrAttachmentsOff = errors.New("attachments are disabled")

// Attachment is a stored receipt, ready to be served.
type Attachment struct {
	Path        string // absolute path on disk
	ContentType string
	Inline      bool // images may be shown in the browser, PDFs are downloaded
}

var attachmentTypes = map[string]struct {
	ext    string
	inline bool
}{
	"application/pdf": {".pdf", false},
	"image/jpeg":      {".jpg", true},
	"image/png":       {".png", true},
	"image/webp":      {".webp", true},
}

func contentTypeForExt(ext string) (string, bool, bool) {
	for ct, v := range attachmentTypes {
		if v.ext == ext {
			return ct, v.inline, true
		}
	}
	return "", false, false
}

// SaveExpenseAttachment stores a receipt for an expense of the class, replacing a previous one. The type is
// decided by the file's content, the name is never used, and the file lands under a random name in a
// per-class directory, so nothing the client sends reaches the filesystem path.
func (s *Store) SaveExpenseAttachment(ctx context.Context, classID, expenseID int64, r io.Reader) error {
	if s.AttachDir == "" {
		return ErrAttachmentsOff
	}
	var old sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT attachment_path FROM expenses WHERE id = ? AND class_id = ?`, expenseID, classID).Scan(&old); err != nil {
		return wrapNotFound(err)
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}
	head = head[:n]
	ct := http.DetectContentType(head)
	kind, ok := attachmentTypes[ct]
	if !ok {
		return ErrAttachmentType
	}
	dir := filepath.Join(s.AttachDir, strconv.FormatInt(classID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	name := fmt.Sprintf("%d-%s%s", expenseID, hex.EncodeToString(rnd[:]), kind.ext)
	full := filepath.Join(dir, name)
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cleanup := func() { f.Close(); os.Remove(full) }
	if _, err := f.Write(head); err != nil {
		cleanup()
		return err
	}
	written, err := io.Copy(f, io.LimitReader(r, MaxAttachmentBytes-int64(len(head))+1))
	if err != nil {
		cleanup()
		return err
	}
	if written+int64(len(head)) > MaxAttachmentBytes {
		cleanup()
		return ErrAttachmentSize
	}
	if err := f.Close(); err != nil {
		os.Remove(full)
		return err
	}
	rel := filepath.ToSlash(filepath.Join(strconv.FormatInt(classID, 10), name))
	if _, err := s.DB.ExecContext(ctx, `UPDATE expenses SET attachment_path = ? WHERE id = ? AND class_id = ?`, rel, expenseID, classID); err != nil {
		os.Remove(full)
		return err
	}
	if old.Valid {
		s.removeAttachmentFile(old.String)
	}
	return nil
}

// removeAttachmentFile deletes a stored receipt, tolerating a path that is already gone or malformed.
func (s *Store) removeAttachmentFile(rel string) {
	if full, ok := s.attachmentFullPath(rel); ok {
		_ = os.Remove(full)
	}
}

// attachmentFullPath resolves a stored relative path and refuses anything outside AttachDir.
func (s *Store) attachmentFullPath(rel string) (string, bool) {
	if s.AttachDir == "" || rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	full := filepath.Join(s.AttachDir, filepath.FromSlash(rel))
	base, err := filepath.Abs(s.AttachDir)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(full)
	if err != nil || filepath.Dir(filepath.Dir(abs)) != base {
		return "", false
	}
	return abs, true
}

// ExpenseAttachment finds the receipt of an expense of the class.
func (s *Store) ExpenseAttachment(ctx context.Context, classID, expenseID int64) (Attachment, error) {
	var rel sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT attachment_path FROM expenses WHERE id = ? AND class_id = ?`, expenseID, classID).Scan(&rel); err != nil {
		return Attachment{}, wrapNotFound(err)
	}
	full, ok := s.attachmentFullPath(rel.String)
	if !rel.Valid || !ok {
		return Attachment{}, ErrNotFound
	}
	ct, inline, known := contentTypeForExt(filepath.Ext(full))
	if !known {
		return Attachment{}, ErrNotFound
	}
	if _, err := os.Stat(full); err != nil {
		return Attachment{}, ErrNotFound
	}
	return Attachment{Path: full, ContentType: ct, Inline: inline}, nil
}

// DeleteExpenseAttachment removes the receipt of an expense.
func (s *Store) DeleteExpenseAttachment(ctx context.Context, classID, expenseID int64) error {
	var rel sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT attachment_path FROM expenses WHERE id = ? AND class_id = ?`, expenseID, classID).Scan(&rel); err != nil {
		return wrapNotFound(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE expenses SET attachment_path = NULL WHERE id = ? AND class_id = ?`, expenseID, classID); err != nil {
		return err
	}
	if rel.Valid {
		s.removeAttachmentFile(rel.String)
	}
	return nil
}
