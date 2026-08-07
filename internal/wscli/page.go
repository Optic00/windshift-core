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
	pageCreateTitle        string
	pageCreateFile         string
	pageCreateParent       int
	pageCreateContent      string
	pageCreateUploadAssets bool
	pageEditTitle          string
	pageEditFile           string
	pageEditContent        string
	pageEditUploadAssets   bool
	pageMoveParent         int
	pageMoveToWorkspace    string
	pageMoveToRoot         bool
	pageMoveBefore         int
	pageMoveAfter          int
	pageGetRaw             bool
	pageHistoryLimit       int
	pageHistoryOffset      int
	pageHistoryRevision    int
	pageGrantPrincipalType string
	pageGrantPrincipalID   int
	pageGrantLevel         string
	pageInheritanceOn      bool
	pageInheritanceOff     bool
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

Pass --label NAME (repeatable, or comma-separated) to filter to pages
tagged with all of the listed page labels. Matching is case-insensitive
and uses AND semantics — only pages carrying every requested label are
returned. The filter is applied client-side after the server returns the
preloaded labels on each page, so no extra round-trip is required.

Examples:
  ws page list
  ws page list -o json
  ws page list --label design
  ws page list --label design,spec`,
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
		if len(pageListLabelFilter) > 0 {
			pages = filterPagesByLabels(pages, pageListLabelFilter)
		}
		NewOutput().Print(pages)
		return nil
	},
}

var pageSearchLimit int

var pageSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search pages by keyword in the current workspace",
	Long: `Keyword search over pages the caller can view in the configured
workspace. Multiple arguments are joined into a single query string.
Matching is a case-insensitive substring on the page title or Markdown body.
Results omit the page body — fetch a match with ws page get <id>.

A workspace must be configured via -w, $WS_WORKSPACE, or
defaults.workspace_key in ws.toml.

Examples:
  ws page search runbook
  ws page search "incident response" --limit 5
  ws page search design -w PROJ`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		query := strings.TrimSpace(strings.Join(args, " "))
		if query == "" {
			return fmt.Errorf("search query must not be empty")
		}
		pages, err := client.SearchPages(wsID, query, pageSearchLimit)
		if err != nil {
			return fmt.Errorf("failed to search pages: %w", err)
		}
		NewOutput().Print(pages)
		return nil
	},
}

var pageGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get a page by id",
	Long: `Fetch a single page by its numeric id. By default prints the
Markdown source to stdout so callers can pipe straight into a file. Pass
an explicit -o json/csv (or -o table for the human-friendly Markdown
stream) to use the structured printer instead.

Examples:
  ws page get 42 > onboarding.md
  ws page get 42 -o json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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
		// Default behavior: stream Markdown to stdout. Matches the
		// documented `ws page get 42 > onboarding.md` example. The
		// global -o json default doesn't apply unless the user
		// explicitly chose -o on THIS invocation.
		explicitOutput := cmd.Flags().Changed("output")
		if !explicitOutput || outputFormat == "table" {
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

When --upload-assets is set together with --file, every
` + "`![alt](./local.png)`" + ` image reference in the markdown that resolves to
a file on disk is uploaded as a page attachment and the markdown is
rewritten to point at the uploaded URL before the page is finalized.
Remote URLs, absolute paths, and references already pointing at
/api/attachments/... are left alone. Plain links ` + "`[doc](./spec.pdf)`" + `
are NOT uploaded — only image syntax is scanned.

Examples:
  ws page create --file onboarding.md
  ws page create --title "Runbook" --file runbook.md
  ws page create --title "Notes" --content "Initial body"
  ws page create --title "Child" --parent 12 --file notes.md
  ws page create --file blog.md --upload-assets`,
	RunE: func(_ *cobra.Command, _ []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}

		title, content, err := resolvePageInput(pageCreateTitle, ParseCLIEscapes(pageCreateContent), pageCreateFile)
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
			return translatePagePermissionError(err, "create page", "")
		}

		// --upload-assets: scan the body for local image refs, upload each
		// to the page just created, and PUT the rewritten markdown back.
		// We deliberately create-then-update because the upload endpoint
		// requires entity_id=<pageID>; uploading before create isn't
		// possible. If an upload fails mid-way the page exists with the
		// original (broken-ref) markdown — re-run `ws page edit ...
		// --file ... --upload-assets` to retry.
		if pageCreateUploadAssets && pageCreateFile != "" {
			rewritten, summary, uerr := uploadAndRewrite(client, wsID, page.ID, content, pageInputDir(pageCreateFile), stderr)
			if uerr != nil {
				return translatePagePermissionError(uerr, "upload page assets", "")
			}
			if rewritten != content {
				updated, perr := client.UpdatePage(wsID, page.ID, PageUpdateRequest{Content: &rewritten})
				if perr != nil {
					return translatePagePermissionError(perr, "update page with rewritten markdown", strconv.Itoa(page.ID))
				}
				page = updated
			}
			_, _ = fmt.Fprintln(stderr, summary)
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

--upload-assets behaves the same way as on ` + "`page create`" + `: every
` + "`![alt](./local.png)`" + ` image reference in the markdown that resolves
to a file on disk is uploaded as a page attachment first, then the
markdown is rewritten to point at the uploaded URL before the PUT.

Examples:
  ws page edit 42 --file rewritten.md
  ws page edit 42 --title "New title" --file rewritten.md
  ws page edit 42 --content "Quick patch"
  ws page edit 42 --file blog.md --upload-assets`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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

		// Detect flag presence with cobra rather than empty-string checks
		// so an explicit `--content ""` can intentionally clear the body.
		titleChanged := cmd.Flags().Changed("title")
		fileChanged := cmd.Flags().Changed("file")
		contentChanged := cmd.Flags().Changed("content")

		var req PageUpdateRequest
		var content string
		var titleFromFile string

		if fileChanged {
			body, fileTitle, ferr := readMarkdownFile(pageEditFile)
			if ferr != nil {
				return ferr
			}
			content = body
			titleFromFile = fileTitle
		} else if contentChanged {
			content = ParseCLIEscapes(pageEditContent)
		}

		if titleChanged {
			t := pageEditTitle
			req.Title = &t
		}
		// File-supplied H1 never overrides — matches the existing
		// "edit --file" semantics (content replace by default).
		_ = titleFromFile

		if fileChanged || contentChanged {
			req.Content = &content
		}

		if req.Title == nil && req.Content == nil {
			return fmt.Errorf("nothing to update: pass --title, --content, or --file")
		}

		// --upload-assets: upload referenced local images to this page,
		// then rewrite the content we're about to PUT. The page already
		// exists, so we can upload first and submit the rewritten body
		// in a single update — no separate create step needed.
		if pageEditUploadAssets && pageEditFile != "" && req.Content != nil {
			rewritten, summary, uerr := uploadAndRewrite(client, wsID, pageID, content, pageInputDir(pageEditFile), stderr)
			if uerr != nil {
				return translatePagePermissionError(uerr, "upload page assets", strconv.Itoa(pageID))
			}
			content = rewritten
			req.Content = &content
			_, _ = fmt.Fprintln(stderr, summary)
		}

		page, err := client.UpdatePage(wsID, pageID, req)
		if err != nil {
			return translatePagePermissionError(err, "update page", strconv.Itoa(pageID))
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
must be supplied. Use --to-workspace <key> to move the whole subtree to
another workspace. The server enforces cycle and depth limits.

Pass --before <id> or --after <id> to place the page at a specific
position among its siblings (mutually exclusive). Combine freely with
--parent / --root to reparent and reorder in one call.

Examples:
  ws page move 42 --parent 7
  ws page move 42 --root
  ws page move 42 --parent 7 --after 11
  ws page move 42 --parent 7 --before 9
  ws page move 42 --root --to-workspace MA`,
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
		if pageMoveBefore != 0 && pageMoveAfter != 0 {
			return fmt.Errorf("--before and --after are mutually exclusive")
		}
		var destinationWorkspaceID *int
		if strings.TrimSpace(pageMoveToWorkspace) != "" {
			destinationID, resolveErr := client.ResolveWorkspaceID(strings.TrimSpace(pageMoveToWorkspace))
			if resolveErr != nil {
				return fmt.Errorf("failed to resolve destination workspace: %w", resolveErr)
			}
			destinationWorkspaceID = &destinationID
		}
		var parent *int
		if !pageMoveToRoot {
			pid := pageMoveParent
			parent = &pid
		}
		// --after X means "this page comes right after X" →
		// prev_sibling_id = X. --before X means "this page comes right
		// before X" → next_sibling_id = X. Field names in the server
		// payload describe the neighbor relative to the moved page.
		var prevSibling, nextSibling *int
		if pageMoveAfter != 0 {
			v := pageMoveAfter
			prevSibling = &v
		}
		if pageMoveBefore != 0 {
			v := pageMoveBefore
			nextSibling = &v
		}
		page, err := client.MovePage(wsID, pageID, parent, prevSibling, nextSibling, destinationWorkspaceID)
		if err != nil {
			return fmt.Errorf("failed to move page: %w", err)
		}
		if outputFormat == "" || outputFormat == "table" {
			dest := "root"
			if parent != nil {
				dest = fmt.Sprintf("under page %d", *parent)
			}
			if pageMoveToWorkspace != "" {
				dest = fmt.Sprintf("to workspace %s, %s", pageMoveToWorkspace, dest)
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
  ws page history 42
  ws page history 42 --limit 25 --offset 50
  ws page history 42 --revision 99`,
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
		if pageHistoryRevision > 0 {
			rev, err := client.GetPageRevision(wsID, pageID, pageHistoryRevision)
			if err != nil {
				return fmt.Errorf("failed to load revision: %w", err)
			}
			NewOutput().Print(rev)
			return nil
		}
		revs, err := client.GetPageHistory(wsID, pageID, pageHistoryLimit, pageHistoryOffset)
		if err != nil {
			return fmt.Errorf("failed to load history: %w", err)
		}
		NewOutput().Print(revs)
		return nil
	},
}

var pageRestoreCmd = &cobra.Command{
	Use:   "restore <page-id> <revision-id>",
	Short: "Restore a page from a revision",
	Long: `Restore a page's title and Markdown content from a revision. If the
page is archived, restore also unarchives that page (not its subtree).`,
	Args: cobra.ExactArgs(2),
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
		revisionID, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid revision id: %s", args[1])
		}
		page, err := client.RestorePageRevision(wsID, pageID, revisionID)
		if err != nil {
			return fmt.Errorf("failed to restore page: %w", err)
		}
		if outputFormat == "" || outputFormat == "table" {
			_, _ = fmt.Fprintf(stdout, "Restored page %d (%s) from revision %d\n", page.ID, page.Title, revisionID)
			return nil
		}
		NewOutput().Print(page)
		return nil
	},
}

var pagePermissionsCmd = &cobra.Command{
	Use:   "permissions <page-id>",
	Short: "Show page permissions and explicit ACL rows",
	Args:  cobra.ExactArgs(1),
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
		perms, err := client.GetPagePermissions(wsID, pageID)
		if err != nil {
			return fmt.Errorf("failed to load page permissions: %w", err)
		}
		NewOutput().Print(perms)
		return nil
	},
}

var pageGrantCmd = &cobra.Command{
	Use:   "grant <page-id>",
	Short: "Grant a page ACL row",
	Args:  cobra.ExactArgs(1),
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
		if pageGrantPrincipalType == "" || pageGrantPrincipalID <= 0 || pageGrantLevel == "" {
			return fmt.Errorf("pass --type user|group|role, --principal <id>, and --level view|edit|admin")
		}
		perm, err := client.GrantPagePermission(wsID, pageID, PageGrantPermissionRequest{PrincipalType: pageGrantPrincipalType, PrincipalID: pageGrantPrincipalID, PermissionLevel: pageGrantLevel})
		if err != nil {
			return fmt.Errorf("failed to grant page permission: %w", err)
		}
		NewOutput().Print(perm)
		return nil
	},
}

var pageRevokeCmd = &cobra.Command{
	Use:   "revoke <page-id> <permission-id>",
	Short: "Revoke a page ACL row",
	Args:  cobra.ExactArgs(2),
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
		permissionID, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid permission id: %s", args[1])
		}
		if err := client.RevokePagePermission(wsID, pageID, permissionID); err != nil {
			return fmt.Errorf("failed to revoke page permission: %w", err)
		}
		_, _ = fmt.Fprintf(stdout, "Revoked permission %d from page %d\n", permissionID, pageID)
		return nil
	},
}

var pageInheritanceCmd = &cobra.Command{
	Use:   "inheritance <page-id>",
	Short: "Enable or disable page permission inheritance",
	Args:  cobra.ExactArgs(1),
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
		if pageInheritanceOn == pageInheritanceOff {
			return fmt.Errorf("pass exactly one of --on or --off")
		}
		page, err := client.SetPageInheritance(wsID, pageID, pageInheritanceOn)
		if err != nil {
			return fmt.Errorf("failed to set inheritance: %w", err)
		}
		if outputFormat == "" || outputFormat == "table" {
			_, _ = fmt.Fprintf(stdout, "Page %d inherit_permissions=%t\n", page.ID, page.InheritPermissions)
			return nil
		}
		NewOutput().Print(page)
		return nil
	},
}

// --- helpers ---

// h1Regex finds the first ATX H1 anywhere after frontmatter or blank lines.
var h1Regex = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)

// resolvePageInput reads --file or --content and prioritizes title flag, H1,
// then filename. Callers decide whether an empty title is invalid.
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

// readMarkdownFile reads a file or stdin, returns its first H1, and preserves
// the complete body for server excerpts and chunking.
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
	pageCmd.AddCommand(pageSearchCmd)
	pageCmd.AddCommand(pageGetCmd)
	pageCmd.AddCommand(pageCreateCmd)
	pageCmd.AddCommand(pageEditCmd)
	pageCmd.AddCommand(pageArchiveCmd)
	pageCmd.AddCommand(pageMoveCmd)
	pageCmd.AddCommand(pageHistoryCmd)
	pageCmd.AddCommand(pageRestoreCmd)
	pageCmd.AddCommand(pagePermissionsCmd)
	pageCmd.AddCommand(pageGrantCmd)
	pageCmd.AddCommand(pageRevokeCmd)
	pageCmd.AddCommand(pageInheritanceCmd)

	pageSearchCmd.Flags().IntVar(&pageSearchLimit, "limit", 0, "maximum results to return (server default 20, max 100)")

	pageGetCmd.Flags().BoolVar(&pageGetRaw, "raw", false, "in table mode, omit the synthetic '# Title' header and print only the body")

	pageCreateCmd.Flags().StringVarP(&pageCreateTitle, "title", "t", "", "page title (wins over --file H1 / filename)")
	pageCreateCmd.Flags().StringVarP(&pageCreateFile, "file", "f", "", "path to a Markdown file (use - for stdin)")
	pageCreateCmd.Flags().StringVar(&pageCreateContent, "content", "", "inline Markdown content (ignored when --file is set; supports \\n / \\t / \\\\, pass --file for verbatim bytes)")
	pageCreateCmd.Flags().IntVar(&pageCreateParent, "parent", 0, "parent page id (omit or pass 0 for a root page)")
	pageCreateCmd.Flags().BoolVar(&pageCreateUploadAssets, "upload-assets", false, "scan --file for ![](./local.png) image refs, upload each as a page attachment, and rewrite the markdown to point at the uploaded URL before creating the page")

	pageEditCmd.Flags().StringVarP(&pageEditTitle, "title", "t", "", "new page title (omit to keep existing)")
	pageEditCmd.Flags().StringVarP(&pageEditFile, "file", "f", "", "path to a Markdown file (use - for stdin)")
	pageEditCmd.Flags().StringVar(&pageEditContent, "content", "", "inline Markdown content (ignored when --file is set; supports \\n / \\t / \\\\, pass --file for verbatim bytes)")
	pageEditCmd.Flags().BoolVar(&pageEditUploadAssets, "upload-assets", false, "scan --file for ![](./local.png) image refs, upload each as a page attachment, and rewrite the markdown to point at the uploaded URL before the update")

	pageMoveCmd.Flags().IntVar(&pageMoveParent, "parent", 0, "new parent page id")
	pageMoveCmd.Flags().StringVar(&pageMoveToWorkspace, "to-workspace", "", "destination workspace key or id (moves the whole subtree)")
	pageMoveCmd.Flags().BoolVar(&pageMoveToRoot, "root", false, "move the page to the workspace root")
	pageMoveCmd.Flags().IntVar(&pageMoveBefore, "before", 0, "insert the moved page immediately before this sibling id")
	pageMoveCmd.Flags().IntVar(&pageMoveAfter, "after", 0, "insert the moved page immediately after this sibling id")

	pageHistoryCmd.Flags().IntVar(&pageHistoryLimit, "limit", 0, "maximum revisions to return (server default 50, max 200)")
	pageHistoryCmd.Flags().IntVar(&pageHistoryOffset, "offset", 0, "number of newest revisions to skip")
	pageHistoryCmd.Flags().IntVar(&pageHistoryRevision, "revision", 0, "fetch a single revision id instead of listing history")

	pageGrantCmd.Flags().StringVar(&pageGrantPrincipalType, "type", "", "principal type: user, group, or role")
	pageGrantCmd.Flags().IntVar(&pageGrantPrincipalID, "principal", 0, "principal id to grant")
	pageGrantCmd.Flags().StringVar(&pageGrantLevel, "level", "view", "permission level: view, edit, or admin")

	pageInheritanceCmd.Flags().BoolVar(&pageInheritanceOn, "on", false, "enable inheritance")
	pageInheritanceCmd.Flags().BoolVar(&pageInheritanceOff, "off", false, "disable inheritance")
}
