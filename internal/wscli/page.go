package wscli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// --- shared flag-bound vars (reset by Run before each invocation) ---

var (
	pageCreateTitle     string
	pageCreateFile      string
	pageCreateParent    int
	pageCreateContent   string
	pageEditTitle       string
	pageEditFile        string
	pageEditContent     string
	pageMoveParent      int
	pageMoveToRoot      bool
	pageGetRaw          bool
	pageHistoryRevision int
)

var pageCmd = &cobra.Command{
	Use:   "page",
	Short: "Manage workspace knowledge pages",
	Long: `Commands for listing, creating, editing, moving, and archiving
workspace knowledge (wiki) pages from the command line.

A workspace must be configured via -w, $WS_WORKSPACE, or
defaults.workspace_key in ws.toml.`,
}

var pageListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pages in the current workspace",
	Long: `List every page the caller can view in the configured workspace.
Output includes id, depth-indented title, slug, and updated_at.

Examples:
  ws page list
  ws page list -o json`,
	RunE: func(_ *cobra.Command, _ []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		pages, err := client.ListPages(wsID)
		if err != nil {
			return fmt.Errorf("failed to list pages: %w", err)
		}
		NewOutput().Print(pages)
		return nil
	},
}

var pageGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get a page by id",
	Long: `Fetch a single page by its numeric id. By default prints the
Markdown source to stdout; use -o json/table for the full record.

Examples:
  ws page get 42 > onboarding.md
  ws page get 42 -o json`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		pageID, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid page id: %s", args[0])
		}
		page, err := client.GetPage(wsID, pageID)
		if err != nil {
			return fmt.Errorf("failed to get page: %w", err)
		}
		// In table mode (the default for human use), stream the raw
		// Markdown so callers can pipe it into a file. JSON/CSV go
		// through the structured printer.
		if outputFormat == "" || outputFormat == "table" {
			if !pageGetRaw {
				_, _ = fmt.Fprintf(stdout, "# %s\n\n", page.Title)
			}
			_, _ = fmt.Fprint(stdout, page.Content)
			if !strings.HasSuffix(page.Content, "\n") {
				_, _ = fmt.Fprintln(stdout)
			}
			return nil
		}
		NewOutput().Print(page)
		return nil
	},
}

var pageCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new page in the current workspace",
	Long: `Create a new page. Markdown content can come from --file or
--content; --file wins. Title resolution priority:

  1. --title flag
  2. first H1 (line starting with "# ") found in the file
  3. filename (without extension) of --file

Examples:
  ws page create --file onboarding.md
  ws page create --title "Runbook" --file runbook.md
  ws page create --title "Notes" --content "Initial body"
  ws page create --title "Child" --parent 12 --file notes.md`,
	RunE: func(_ *cobra.Command, _ []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}

		title, content, err := resolvePageInput(pageCreateTitle, pageCreateContent, pageCreateFile)
		if err != nil {
			return err
		}
		if title == "" {
			return fmt.Errorf("title required: pass --title or use a --file whose first heading is an H1, or whose filename is non-empty")
		}

		req := PageCreateRequest{
			Title:   title,
			Content: content,
		}
		if pageCreateParent > 0 {
			pid := pageCreateParent
			req.ParentID = &pid
		}

		page, err := client.CreatePage(wsID, req)
		if err != nil {
			return fmt.Errorf("failed to create page: %w", err)
		}
		if outputFormat == "" || outputFormat == "table" {
			_, _ = fmt.Fprintf(stdout, "Created page %d (%s)\n", page.ID, page.Title)
			return nil
		}
		NewOutput().Print(page)
		return nil
	},
}

var pageEditCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Edit (replace) a page's content from a file or string",
	Long: `Replace a page's content atomically (writes a new revision).
By default --file replaces the body but leaves the title unchanged.
Pass --title to also update the title.

Examples:
  ws page edit 42 --file rewritten.md
  ws page edit 42 --title "New title" --file rewritten.md
  ws page edit 42 --content "Quick patch"`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		pageID, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid page id: %s", args[0])
		}

		var req PageUpdateRequest
		var content string
		var titleFromFile string

		if pageEditFile != "" {
			body, fileTitle, ferr := readMarkdownFile(pageEditFile)
			if ferr != nil {
				return ferr
			}
			content = body
			titleFromFile = fileTitle
		} else if pageEditContent != "" {
			content = pageEditContent
		}

		if pageEditTitle != "" {
			t := pageEditTitle
			req.Title = &t
		} else if titleFromFile != "" && pageEditFile != "" && pageEditTitle == "" {
			// File supplied a title but the caller didn't ask for a
			// title change. Leave title untouched — matches the doc
			// for "edit --file" semantics (content replace by default).
			_ = titleFromFile
		}

		if pageEditFile != "" || pageEditContent != "" {
			req.Content = &content
		}

		if req.Title == nil && req.Content == nil {
			return fmt.Errorf("nothing to update: pass --title, --content, or --file")
		}

		page, err := client.UpdatePage(wsID, pageID, req)
		if err != nil {
			return fmt.Errorf("failed to update page: %w", err)
		}
		if outputFormat == "" || outputFormat == "table" {
			_, _ = fmt.Fprintf(stdout, "Updated page %d (%s)\n", page.ID, page.Title)
			return nil
		}
		NewOutput().Print(page)
		return nil
	},
}

var pageArchiveCmd = &cobra.Command{
	Use:   "archive <id>",
	Short: "Archive a page (and its entire subtree)",
	Long: `Archive a page. Phase 1 archive is a soft-delete that hides the
page and its descendants from the tree; restoring an explicit revision
is the recovery path.

Examples:
  ws page archive 42`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		pageID, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid page id: %s", args[0])
		}
		if err := client.ArchivePage(wsID, pageID); err != nil {
			return fmt.Errorf("failed to archive page: %w", err)
		}
		_, _ = fmt.Fprintf(stdout, "Archived page %d\n", pageID)
		return nil
	},
}

var pageMoveCmd = &cobra.Command{
	Use:   "move <id>",
	Short: "Move a page under a new parent (or to the workspace root)",
	Long: `Move a page under a new parent. Either --parent <id> or --root
must be supplied. The server enforces cycle and depth limits.

Examples:
  ws page move 42 --parent 7
  ws page move 42 --root`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		pageID, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid page id: %s", args[0])
		}
		if !pageMoveToRoot && pageMoveParent == 0 {
			return fmt.Errorf("must pass --parent <id> or --root")
		}
		if pageMoveToRoot && pageMoveParent != 0 {
			return fmt.Errorf("--parent and --root are mutually exclusive")
		}
		var parent *int
		if !pageMoveToRoot {
			pid := pageMoveParent
			parent = &pid
		}
		page, err := client.MovePage(wsID, pageID, parent)
		if err != nil {
			return fmt.Errorf("failed to move page: %w", err)
		}
		if outputFormat == "" || outputFormat == "table" {
			dest := "root"
			if parent != nil {
				dest = fmt.Sprintf("under page %d", *parent)
			}
			_, _ = fmt.Fprintf(stdout, "Moved page %d %s (new path: %s)\n", page.ID, dest, page.Path)
			return nil
		}
		NewOutput().Print(page)
		return nil
	},
}

var pageHistoryCmd = &cobra.Command{
	Use:   "history <id>",
	Short: "Show revision history for a page",
	Long: `List revisions for a page, newest-first. Each row shows the
revision number, change_type, author, and timestamp.

Examples:
  ws page history 42`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		pageID, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid page id: %s", args[0])
		}
		revs, err := client.GetPageHistory(wsID, pageID)
		if err != nil {
			return fmt.Errorf("failed to load history: %w", err)
		}
		NewOutput().Print(revs)
		return nil
	},
}

// --- helpers ---

// h1Regex captures the first ATX H1 from a Markdown body. Multi-line
// regex anchors so we can match a heading anywhere in the file rather
// than only at the very start, which is brittle when files lead with a
// frontmatter block or blank lines.
var h1Regex = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)

// resolvePageInput applies the create-time title-and-content rules:
//
//   - content comes from --file when set, otherwise --content
//   - title flag wins; else first H1 in the content; else filename
//
// Returns (title, content, error). title can still be "" if neither
// flag nor heading nor file was supplied — caller decides whether to
// fail.
func resolvePageInput(flagTitle, flagContent, file string) (title, content string, err error) {
	var fileTitle string
	if file != "" {
		body, h1, rerr := readMarkdownFile(file)
		if rerr != nil {
			return "", "", rerr
		}
		content = body
		fileTitle = h1
	} else if flagContent != "" {
		content = flagContent
	}

	title = flagTitle
	if title == "" {
		title = fileTitle
	}
	if title == "" && file != "" {
		title = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	title = strings.TrimSpace(title)
	return title, content, nil
}

// readMarkdownFile reads the given file (- means stdin) and extracts the
// first H1 heading as a candidate title. The H1 text is returned
// separately so the caller can decide whether to honor it; the body is
// returned verbatim (including the heading) so the server-side excerpt
// and chunker see the full document.
func readMarkdownFile(path string) (content, h1Title string, err error) {
	var data []byte
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		// #nosec G304 -- path is user-supplied CLI arg, intentional.
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", "", fmt.Errorf("failed to read %s: %w", path, err)
	}
	body := string(data)
	if match := h1Regex.FindStringSubmatch(body); len(match) > 1 {
		h1Title = strings.TrimSpace(match[1])
	}
	return body, h1Title, nil
}

func init() {
	rootCmd.AddCommand(pageCmd)
	pageCmd.AddCommand(pageListCmd)
	pageCmd.AddCommand(pageGetCmd)
	pageCmd.AddCommand(pageCreateCmd)
	pageCmd.AddCommand(pageEditCmd)
	pageCmd.AddCommand(pageArchiveCmd)
	pageCmd.AddCommand(pageMoveCmd)
	pageCmd.AddCommand(pageHistoryCmd)

	pageGetCmd.Flags().BoolVar(&pageGetRaw, "raw", false, "in table mode, omit the synthetic '# Title' header and print only the body")

	pageCreateCmd.Flags().StringVarP(&pageCreateTitle, "title", "t", "", "page title (wins over --file H1 / filename)")
	pageCreateCmd.Flags().StringVarP(&pageCreateFile, "file", "f", "", "path to a Markdown file (use - for stdin)")
	pageCreateCmd.Flags().StringVar(&pageCreateContent, "content", "", "inline Markdown content (ignored when --file is set)")
	pageCreateCmd.Flags().IntVar(&pageCreateParent, "parent", 0, "parent page id (omit or pass 0 for a root page)")

	pageEditCmd.Flags().StringVarP(&pageEditTitle, "title", "t", "", "new page title (omit to keep existing)")
	pageEditCmd.Flags().StringVarP(&pageEditFile, "file", "f", "", "path to a Markdown file (use - for stdin)")
	pageEditCmd.Flags().StringVar(&pageEditContent, "content", "", "inline Markdown content (ignored when --file is set)")

	pageMoveCmd.Flags().IntVar(&pageMoveParent, "parent", 0, "new parent page id")
	pageMoveCmd.Flags().BoolVar(&pageMoveToRoot, "root", false, "move the page to the workspace root")

	pageHistoryCmd.Flags().IntVar(&pageHistoryRevision, "revision", 0, "show only the given revision (default: list all)")
}
