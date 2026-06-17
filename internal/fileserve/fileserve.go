// Package fileserve holds small, dependency-free helpers for safely serving
// files stored on disk: opening a stored path confined to a configured root
// (defeating symlink and ".." escapes) and formatting Content-Disposition
// header values that can't be broken by hostile filenames.
//
// It is shared by the cookie-auth handlers (internal/handlers) and the
// bearer-token v1 handlers (internal/restapi/v1/handlers) so both surfaces
// resolve attachment paths and headers identically.
package fileserve

import (
	"errors"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// ErrOutsideRoot is returned when a stored path cannot be resolved to a
// location inside the configured storage root.
var ErrOutsideRoot = errors.New("path is outside the configured storage root")

// OpenUnderRoot opens storedPath confined to root using os.OpenRoot (Go 1.24+).
// os.Root rejects parent-directory traversal and refuses to follow symlinks
// that escape the root, so even a malicious row in the database or a symlink
// planted in the storage volume cannot read outside root.
//
// storedPath may be:
//   - absolute and inside root (legacy rows written when the root was an
//     absolute path), or
//   - relative to the current working directory but inside root (the default /
//     e2e setup where the root itself is a relative path), or
//   - relative to root (e.g. "items/123/file.png", as written by email
//     ingestion).
//
// The returned *os.File is independent of the underlying os.Root, which is
// closed before returning. Callers own closing the file. Returns ErrOutsideRoot
// when the path escapes root, or the underlying os error (e.g. os.ErrNotExist)
// when the open itself fails.
func OpenUnderRoot(root, storedPath string) (*os.File, error) {
	if root == "" {
		return nil, ErrOutsideRoot
	}
	rel, err := relWithinRoot(root, storedPath)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return r.Open(rel)
}

// RemoveUnderRoot removes storedPath confined to root, mirroring OpenUnderRoot's
// resolution and confinement: a stored path that escapes root is refused with
// ErrOutsideRoot rather than followed (defense against a malicious row or a
// planted symlink), and a missing file surfaces as os.ErrNotExist. Only the
// named file is removed — side files such as thumbnails are left untouched.
func RemoveUnderRoot(root, storedPath string) error {
	if root == "" {
		return ErrOutsideRoot
	}
	rel, err := relWithinRoot(root, storedPath)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return r.Remove(rel)
}

// relWithinRoot resolves storedPath to a path relative to root, rejecting any
// candidate that lands outside root. It mirrors the historical resolution
// order (try the path as written first, then joined under root) so existing
// rows keep resolving, while guaranteeing the result is a clean root-relative
// path safe to hand to os.Root.Open.
func relWithinRoot(root, storedPath string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	var candidates []string
	if filepath.IsAbs(storedPath) {
		candidates = []string{storedPath}
	} else {
		// As-written (relative to CWD) covers rows stored when the root was a
		// relative path; joined-under-root covers truly root-relative rows.
		candidates = []string{storedPath, filepath.Join(root, storedPath)}
	}

	for _, candidate := range candidates {
		absCandidate, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, absCandidate)
		if err != nil {
			continue
		}
		if rel == "." || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(os.PathSeparator)) ||
			filepath.IsAbs(rel) {
			continue
		}
		return rel, nil
	}
	return "", ErrOutsideRoot
}

// ContentDisposition builds a safe Content-Disposition header value for the
// given disposition ("inline" or "attachment") and filename.
//
// The filename is formatted with mime.FormatMediaType, which quotes/escapes
// special characters (quotes, semicolons, backslashes, spaces) and emits an
// RFC 2231 filename* parameter for non-ASCII names — so a filename cannot
// inject extra parameters or break out of the quoted string. Control
// characters (including CR/LF) are stripped first so the value can never
// contribute to header splitting.
//
// If formatting fails (e.g. an unexpected disposition), the bare disposition is
// returned without a filename rather than an attacker-influenced string.
func ContentDisposition(disposition, filename string) string {
	clean := sanitizeFilename(filename)
	if v := mime.FormatMediaType(disposition, map[string]string{"filename": clean}); v != "" {
		return v
	}
	return disposition
}

// sanitizeFilename drops control characters (CR, LF, NUL, and the rest) that
// have no place in a header value. Everything else — including Unicode — is
// preserved for mime.FormatMediaType to encode.
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
}
