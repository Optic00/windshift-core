package wscli

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Page-attachment import for `ws page create/edit --upload-assets`.
//
// Lives in its own file (not in attachment.go) so the existing
// work-item attachment surface stays untouched per the design
// constraint. The split also matches the per-noun layout the rest of
// the package uses (page.go, page_label.go, task.go, comment.go).
//
// Flow when --upload-assets is set:
//  1. Scan the markdown for `![alt](path)` references that resolve to
//     a local file (extractImageRefs).
//  2. For each match, upload the file as a page attachment via
//     Client.UploadPageAttachment.
//  3. Rewrite the markdown to replace each ref's path with the new
//     attachment download URL (rewriteImageRefs).
//  4. Submit the rewritten markdown.
//
// Only image syntax `![](url)` is rewritten; plain link syntax
// `[](url)` is intentionally left alone so a markdown link to a local
// document doesn't silently get uploaded as an attachment. Reference-
// style images and inline HTML `<img>` are also skipped — defer if
// users ask for them.

// imageRefRegex matches `![alt](path)`. The `alt` group is captured for
// completeness even though we only consume the path; nested brackets in
// alt text aren't supported, matching the limit of most markdown
// renderers. The path group is everything between the first `(` after
// `]` and the matching `)` — titles like `![alt](path "title")` are
// rare in imported documents and are not parsed here (the title text
// would be kept inside the path group and cause the existence check
// below to fail, which falls through to "skipped — file not found").
var imageRefRegex = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// localImageRef is one image reference resolved against the source
// markdown's directory. uploadedURL is filled in after the upload step
// runs; an empty value means the ref was scanned but no upload was
// attempted (skip cases) or the upload failed.
type localImageRef struct {
	original    string // exact substring in the source markdown, e.g. "![hero](./img.png)"
	altText     string
	rawPath     string // path exactly as written in markdown
	absPath     string // resolved against baseDir
	uploadedURL string
	skipReason  string // populated when we deliberately did not upload
}

// extractImageRefs walks the markdown and returns one entry per image
// reference whose target resolves to a regular file on disk relative to
// baseDir. References pointing at remote URLs, absolute paths, data:
// URIs, or non-existent files are also returned but with skipReason set
// — the caller surfaces a summary so the user can see what happened.
func extractImageRefs(markdown, baseDir string) []localImageRef {
	matches := imageRefRegex.FindAllStringSubmatchIndex(markdown, -1)
	refs := make([]localImageRef, 0, len(matches))
	for _, m := range matches {
		whole := markdown[m[0]:m[1]]
		alt := markdown[m[2]:m[3]]
		path := strings.TrimSpace(markdown[m[4]:m[5]])
		ref := localImageRef{original: whole, altText: alt, rawPath: path}

		switch {
		case path == "":
			ref.skipReason = "empty path"
		case looksRemote(path):
			ref.skipReason = "remote URL"
		case strings.HasPrefix(path, "/api/attachments/"):
			ref.skipReason = "already an attachment URL"
		case strings.HasPrefix(path, "/"):
			ref.skipReason = "absolute path"
		default:
			abs := path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(baseDir, abs)
			}
			info, err := os.Stat(abs)
			switch {
			case err != nil:
				ref.skipReason = "file not found"
			case info.IsDir():
				ref.skipReason = "path is a directory"
			default:
				ref.absPath = abs
			}
		}
		refs = append(refs, ref)
	}
	return refs
}

// looksRemote reports whether the markdown path uses a scheme that means
// "do not touch this".
func looksRemote(path string) bool {
	if u, err := url.Parse(path); err == nil && u.Scheme != "" {
		return true
	}
	return false
}

// rewriteImageRefs replaces every ref whose uploadedURL is non-empty
// with `![<alt>](<uploadedURL>)`. References without an uploadedURL
// (skipped or upload-failed) are left untouched.
func rewriteImageRefs(markdown string, refs []localImageRef) string {
	out := markdown
	for _, ref := range refs {
		if ref.uploadedURL == "" {
			continue
		}
		replacement := fmt.Sprintf("![%s](%s)", ref.altText, ref.uploadedURL)
		// Replace ALL occurrences so the (rare) case of the same
		// `![alt](path)` literal appearing twice in the document is
		// handled — using n=1 would only swap the first.
		out = strings.ReplaceAll(out, ref.original, replacement)
	}
	return out
}

// uploadAndRewrite uploads each resolvable image ref to the given page
// and returns the rewritten markdown plus a one-line summary fit to
// print at the end of `ws page create/edit`.
//
// Errors from a single upload bubble up — partial state in the form of
// previously-uploaded attachments may remain on the page; the
// orchestration call site is expected to surface this and suggest the
// user re-run `ws page edit … --file … --upload-assets` to retry.
func uploadAndRewrite(client *Client, workspaceID, pageID int, markdown, baseDir string, progress io.Writer) (rewritten, summary string, err error) {
	refs := extractImageRefs(markdown, baseDir)
	if len(refs) == 0 {
		return markdown, "no image references found in markdown", nil
	}

	uploaded := 0
	skipped := 0
	for i := range refs {
		ref := &refs[i]
		if ref.skipReason != "" {
			skipped++
			if progress != nil {
				_, _ = fmt.Fprintf(progress, "  skip %s (%s)\n", ref.rawPath, ref.skipReason)
			}
			continue
		}

		f, err := os.Open(ref.absPath) //nolint:gosec // G304: path resolved against the user's --file directory, intentional
		if err != nil {
			return markdown, "", fmt.Errorf("open %s: %w", ref.absPath, err)
		}
		att, err := client.UploadPageAttachment(workspaceID, pageID, filepath.Base(ref.absPath), f)
		_ = f.Close()
		if err != nil {
			return markdown, "", fmt.Errorf("upload %s: %w", ref.rawPath, err)
		}
		ref.uploadedURL = fmt.Sprintf("/api/attachments/%d/download", att.ID)
		uploaded++
		if progress != nil {
			_, _ = fmt.Fprintf(progress, "  uploaded %s -> attachment %d\n", ref.rawPath, att.ID)
		}
	}

	summary = fmt.Sprintf("uploaded %d of %d image reference(s); %d skipped", uploaded, len(refs), skipped)
	return rewriteImageRefs(markdown, refs), summary, nil
}

// pageInputDir returns the directory image refs in a markdown file
// should be resolved against. For real file paths this is the file's
// directory; for stdin (or an empty path) it falls back to the current
// working directory so `cat blog.md | ws page create --upload-assets`
// still finds relative images in CWD.
func pageInputDir(filePath string) string {
	if filePath == "" || filePath == "-" {
		if cwd, err := os.Getwd(); err == nil {
			return cwd
		}
		return "."
	}
	return filepath.Dir(filePath)
}

// translatePagePermissionError wraps an error returned by a page-related
// API call so a 404 (used uniformly for "not found" AND "no permission"
// per the server's security policy — failing closed without disclosing
// resource existence) surfaces as an actionable hint that the user
// likely needs the Editor role.
//
// op is a short verb like "create page" or "update page"; resourceID
// is an optional contextual id (page id or empty for create).
func translatePagePermissionError(err error, op, resourceID string) error {
	if err == nil {
		return nil
	}
	apiErr := apiErrorFromError(err)
	if apiErr == nil || apiErr.Status != 404 {
		// Non-404 errors pass through with the original message —
		// validation errors, server errors, etc. already carry useful
		// context.
		if op != "" {
			return fmt.Errorf("%s: %w", op, err)
		}
		return err
	}
	context := op
	if resourceID != "" {
		context = fmt.Sprintf("%s (id %s)", op, resourceID)
	}
	return fmt.Errorf("%s: not found, or you lack page.edit in this workspace (Editor role required) — server said: %s", context, apiErr.Error())
}

// apiErrorFromError unwraps *APIError from a wrapped error chain. Returns
// nil if no APIError is found.
func apiErrorFromError(err error) *APIError {
	for cur := err; cur != nil; {
		if ae, ok := cur.(*APIError); ok {
			return ae
		}
		unwrapper, ok := cur.(interface{ Unwrap() error })
		if !ok {
			return nil
		}
		cur = unwrapper.Unwrap()
	}
	return nil
}
