package wscli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// --- shared flag-bound vars (reset by Run before each invocation) ---

var (
	pageLabelCreateName  string
	pageLabelCreateColor string
	pageLabelEditName    string
	pageLabelEditColor   string

	pageLabelAttachAdd    []int
	pageLabelAttachRemove []int
	pageLabelAttachSet    []int

	pageListLabelFilter []string
)

// --- workspace-scoped label CRUD: `ws page-label ...` ---

var pageLabelCmd = &cobra.Command{
	Use:   "page-label",
	Short: "Manage workspace page labels",
	Long: `Page labels are workspace-scoped labels that attach to pages only.
Separate from work-item labels — they live in their own table and never
collide with the work-item label namespace.

A workspace must be configured via -w, $WS_WORKSPACE, or
defaults.workspace_key in ws.toml.

Examples:
  ws page-label list
  ws page-label create --name design --color "#3B82F6"
  ws page-label edit 5 --name "Design docs"
  ws page-label delete 5`,
}

var pageLabelListCmd = &cobra.Command{
	Use:   "list",
	Short: "List page labels in the current workspace",
	RunE: func(_ *cobra.Command, _ []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		labels, err := client.ListPageLabels(wsID)
		if err != nil {
			return fmt.Errorf("failed to list page labels: %w", err)
		}
		NewOutput().Print(labels)
		return nil
	},
}

var pageLabelGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get a page label by id",
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
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid label id: %s", args[0])
		}
		label, err := client.GetPageLabel(wsID, id)
		if err != nil {
			return fmt.Errorf("failed to get page label: %w", err)
		}
		NewOutput().Print(label)
		return nil
	},
}

var pageLabelCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new page label",
	Long: `Create a page label scoped to the current workspace. --color
defaults to #3B82F6 (blue) if omitted.

Examples:
  ws page-label create --name design
  ws page-label create --name urgent --color "#EF4444"`,
	RunE: func(_ *cobra.Command, _ []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(pageLabelCreateName)
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		req := PageLabelCreateRequest{Name: name, Color: pageLabelCreateColor}
		label, err := client.CreatePageLabel(wsID, req)
		if err != nil {
			return fmt.Errorf("failed to create page label: %w", err)
		}
		if outputFormat == "" || outputFormat == "table" {
			_, _ = fmt.Fprintf(stdout, "Created page label %d (%s, %s)\n", label.ID, label.Name, label.Color)
			return nil
		}
		NewOutput().Print(label)
		return nil
	},
}

var pageLabelEditCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Rename / recolor a page label",
	Long: `Apply a partial update to a page label. Omit a flag to leave that
field unchanged.

Examples:
  ws page-label edit 5 --name "Design docs"
  ws page-label edit 5 --color "#10B981"`,
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
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid label id: %s", args[0])
		}
		var req PageLabelUpdateRequest
		if cmd.Flags().Changed("name") {
			name := strings.TrimSpace(pageLabelEditName)
			req.Name = &name
		}
		if cmd.Flags().Changed("color") {
			color := pageLabelEditColor
			req.Color = &color
		}
		if req.Name == nil && req.Color == nil {
			return fmt.Errorf("nothing to update: pass --name and/or --color")
		}
		label, err := client.UpdatePageLabel(wsID, id, req)
		if err != nil {
			return fmt.Errorf("failed to update page label: %w", err)
		}
		if outputFormat == "" || outputFormat == "table" {
			_, _ = fmt.Fprintf(stdout, "Updated page label %d (%s, %s)\n", label.ID, label.Name, label.Color)
			return nil
		}
		NewOutput().Print(label)
		return nil
	},
}

var pageLabelDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a page label (also removes it from every page it's attached to)",
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
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid label id: %s", args[0])
		}
		if err := client.DeletePageLabel(wsID, id); err != nil {
			return fmt.Errorf("failed to delete page label: %w", err)
		}
		_, _ = fmt.Fprintf(stdout, "Deleted page label %d\n", id)
		return nil
	},
}

// --- page-scoped attachment: `ws page label-* ...` ---

var pageLabelsCmd = &cobra.Command{
	Use:   "labels <page-id>",
	Short: "List, attach, detach, or replace the labels on a page",
	Long: `Manage page-label assignments on a specific page. Without any
flag this prints the labels currently attached to the page. Pass any
combination of --add, --remove, and --set to mutate the assignment set
(--set is exclusive with --add/--remove and atomically replaces the full
set).

Examples:
  ws page labels 42
  ws page labels 42 --add 5
  ws page labels 42 --add 5,7 --remove 11
  ws page labels 42 --set 5,7,9`,
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

		hasMutations := len(pageLabelAttachAdd) > 0 || len(pageLabelAttachRemove) > 0 || len(pageLabelAttachSet) > 0
		if len(pageLabelAttachSet) > 0 && (len(pageLabelAttachAdd) > 0 || len(pageLabelAttachRemove) > 0) {
			return fmt.Errorf("--set is exclusive with --add and --remove")
		}

		if !hasMutations {
			labels, lerr := client.ListPageLabelsForPage(wsID, pageID)
			if lerr != nil {
				return fmt.Errorf("failed to list labels on page %d: %w", pageID, lerr)
			}
			NewOutput().Print(labels)
			return nil
		}

		var labels []PageLabel
		switch {
		case len(pageLabelAttachSet) > 0:
			labels, err = client.SetPageLabelsForPage(wsID, pageID, pageLabelAttachSet)
			if err != nil {
				return fmt.Errorf("failed to set labels on page %d: %w", pageID, err)
			}
		default:
			for _, id := range pageLabelAttachAdd {
				labels, err = client.AddPageLabelToPage(wsID, pageID, id)
				if err != nil {
					return fmt.Errorf("failed to attach label %d to page %d: %w", id, pageID, err)
				}
			}
			for _, id := range pageLabelAttachRemove {
				if rerr := client.RemovePageLabelFromPage(wsID, pageID, id); rerr != nil {
					return fmt.Errorf("failed to detach label %d from page %d: %w", id, pageID, rerr)
				}
			}
			// After remove-only flows we still need to fetch the final state.
			if len(pageLabelAttachAdd) == 0 {
				labels, err = client.ListPageLabelsForPage(wsID, pageID)
				if err != nil {
					return fmt.Errorf("failed to list labels on page %d: %w", pageID, err)
				}
			}
		}
		NewOutput().Print(labels)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pageLabelCmd)
	pageLabelCmd.AddCommand(pageLabelListCmd)
	pageLabelCmd.AddCommand(pageLabelGetCmd)
	pageLabelCmd.AddCommand(pageLabelCreateCmd)
	pageLabelCmd.AddCommand(pageLabelEditCmd)
	pageLabelCmd.AddCommand(pageLabelDeleteCmd)

	pageLabelCreateCmd.Flags().StringVar(&pageLabelCreateName, "name", "", "label name (required)")
	pageLabelCreateCmd.Flags().StringVar(&pageLabelCreateColor, "color", "", "hex color (default #3B82F6)")

	pageLabelEditCmd.Flags().StringVar(&pageLabelEditName, "name", "", "new label name")
	pageLabelEditCmd.Flags().StringVar(&pageLabelEditColor, "color", "", "new hex color")

	// `ws page labels` lives under the existing `ws page` tree.
	pageCmd.AddCommand(pageLabelsCmd)
	pageLabelsCmd.Flags().IntSliceVar(&pageLabelAttachAdd, "add", nil, "label id(s) to attach (repeatable or comma-separated)")
	pageLabelsCmd.Flags().IntSliceVar(&pageLabelAttachRemove, "remove", nil, "label id(s) to detach (repeatable or comma-separated)")
	pageLabelsCmd.Flags().IntSliceVar(&pageLabelAttachSet, "set", nil, "atomically replace the label set with these id(s)")

	// Extend `ws page list` with a client-side --label filter that matches
	// labels by exact name (case-insensitive). Pass the flag multiple times
	// or comma-separate to require all listed labels (AND semantics).
	pageListCmd.Flags().StringSliceVar(&pageListLabelFilter, "label", nil, "filter to pages tagged with all of the given label names (case-insensitive)")
}

// filterPagesByLabels keeps only pages whose Labels include every name in
// `wanted` (case-insensitive). Used by the `--label` post-filter on
// `ws page list` and the `ws page label` query helpers.
func filterPagesByLabels(pages []Page, wanted []string) []Page {
	if len(wanted) == 0 {
		return pages
	}
	wantSet := make(map[string]struct{}, len(wanted))
	for _, w := range wanted {
		w = strings.TrimSpace(strings.ToLower(w))
		if w == "" {
			continue
		}
		wantSet[w] = struct{}{}
	}
	if len(wantSet) == 0 {
		return pages
	}
	out := pages[:0]
	for _, p := range pages {
		got := make(map[string]struct{}, len(p.Labels))
		for _, l := range p.Labels {
			got[strings.ToLower(l.Name)] = struct{}{}
		}
		matched := true
		for want := range wantSet {
			if _, ok := got[want]; !ok {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, p)
		}
	}
	return out
}
