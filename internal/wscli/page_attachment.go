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

// Page attachments for `ws page create/edit --upload-assets`: upload local
// inline Markdown images, rewrite their URLs, then submit. Plain links,
// reference images, and HTML images are intentionally left untouched.

// imageRefRegex matches simple inline images; titled paths fall through as
// skipped files.
var imageRefRegex = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// localImageRef tracks an image reference and its upload outcome.
type localImageRef struct {
	original    string // exact substring in the source markdown, e.g. "![hero](./img.png)"
	altText     string
	rawPath     string // path exactly as written in markdown
	absPath     string // resolved against baseDir
	uploadedURL string
	skipReason  string // populated when we deliberately did not upload
}

// extractImageRefs resolves local images and records skipped references.
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
		// Replace every identical image reference.
		out = strings.ReplaceAll(out, ref.original, replacement)
	}
	return out
}

// uploadAndRewrite uploads resolvable images and returns rewritten Markdown and
// a summary. Errors preserve prior uploads; callers surface the retry command.
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
