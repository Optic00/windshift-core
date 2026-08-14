package wscli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// --- flag-bound vars (reset by Run before each invocation) ---

var (
	taskLabelAdd    []string
	taskLabelRemove []string
	taskLabelSet    []string
)

// --- global label catalog: `ws label ...` ---

var labelCmd = &cobra.Command{
	Use:   "label",
	Short: "Manage global item labels",
	Long: `Item labels come from a global catalog and attach to work items.
Fully separate from page labels — they live in their own table and never
collide with the page-label namespace.

A workspace must be configured to provide the API authorization context.
Use -w, $WS_WORKSPACE, or defaults.workspace_key in ws.toml.

Examples:
  ws label ls
  ws label ls -w PROJ
  ws task label PROJ-12 --add backend,urgent`,
}

var labelListCmd = &cobra.Command{
	Use:   "ls",
	Short: "List global item labels",
	RunE: func(_ *cobra.Command, _ []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		labels, err := client.ListLabels(wsID)
		if err != nil {
			return fmt.Errorf("failed to list labels: %w", err)
		}
		NewOutput().Print(labels)
		return nil
	},
}

// --- per-item attachment: `ws task label <item>` ---

var taskLabelCmd = &cobra.Command{
	Use:   "label <id|KEY-123>",
	Short: "List, attach, detach, or replace the labels on a task",
	Long: `Manage label assignments on a work item. Without any flag this
prints the labels currently attached to the item. Pass any combination of
--add and --remove, or --set, to mutate the assignment set (--set is
exclusive with --add/--remove and atomically replaces the full set).

Labels are given by name (case-insensitive, resolved against the global
catalog) or numeric ID.

Examples:
  ws task label PROJ-12
  ws task label PROJ-12 --add backend
  ws task label PROJ-12 --add backend,urgent --remove frontend
  ws task label PROJ-12 --set backend,urgent`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}

		itemID, err := client.ResolveItemID(args[0])
		if err != nil {
			return fmt.Errorf("failed to resolve item: %w", err)
		}

		hasMutations := len(taskLabelAdd) > 0 || len(taskLabelRemove) > 0 || len(taskLabelSet) > 0
		if len(taskLabelSet) > 0 && (len(taskLabelAdd) > 0 || len(taskLabelRemove) > 0) {
			return fmt.Errorf("--set is exclusive with --add and --remove")
		}

		if !hasMutations {
			labels, lerr := client.ListItemLabels(itemID)
			if lerr != nil {
				return fmt.Errorf("failed to list labels on %s: %w", args[0], lerr)
			}
			NewOutput().Print(labels)
			return nil
		}

		// The workspace path supplies authorization context for catalog access.
		item, err := client.GetItem(itemID, "")
		if err != nil {
			return fmt.Errorf("failed to get item: %w", err)
		}
		catalog, err := client.ListLabels(item.WorkspaceID)
		if err != nil {
			return fmt.Errorf("failed to list global labels: %w", err)
		}

		var labels []Label
		switch {
		case len(taskLabelSet) > 0:
			ids, rerr := resolveLabelIDs(taskLabelSet, catalog)
			if rerr != nil {
				return rerr
			}
			labels, err = client.SetItemLabels(itemID, ids)
			if err != nil {
				return fmt.Errorf("failed to set labels on %s: %w", args[0], err)
			}
		default:
			addIDs, rerr := resolveLabelIDs(taskLabelAdd, catalog)
			if rerr != nil {
				return rerr
			}
			removeIDs, rerr := resolveLabelIDs(taskLabelRemove, catalog)
			if rerr != nil {
				return rerr
			}
			for _, id := range addIDs {
				labels, err = client.AddItemLabel(itemID, id)
				if err != nil {
					return fmt.Errorf("failed to attach label %d to %s: %w", id, args[0], err)
				}
			}
			for _, id := range removeIDs {
				if derr := client.RemoveItemLabel(itemID, id); derr != nil {
					return fmt.Errorf("failed to detach label %d from %s: %w", id, args[0], derr)
				}
			}
			// After remove-only flows we still need to fetch the final state.
			if len(addIDs) == 0 || len(removeIDs) > 0 {
				labels, err = client.ListItemLabels(itemID)
				if err != nil {
					return fmt.Errorf("failed to list labels on %s: %w", args[0], err)
				}
			}
		}
		NewOutput().Print(labels)
		return nil
	},
}

// resolveLabelIDs maps label names / numeric IDs to label IDs using the
// global catalog. Names match case-insensitively, exact first, then
// unique substring — same convention as resolveItemTypeID.
func resolveLabelIDs(inputs []string, catalog []Label) ([]int, error) {
	ids := make([]int, 0, len(inputs))
	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if id, err := strconv.Atoi(input); err == nil {
			if id <= 0 {
				return nil, fmt.Errorf("label ID must be positive: %s", input)
			}
			ids = append(ids, id)
			continue
		}

		inputLower := strings.ToLower(input)
		var exact *Label
		var partial []Label
		for i := range catalog {
			nameLower := strings.ToLower(catalog[i].Name)
			if nameLower == inputLower {
				exact = &catalog[i]
				break
			}
			if strings.Contains(nameLower, inputLower) {
				partial = append(partial, catalog[i])
			}
		}
		switch {
		case exact != nil:
			ids = append(ids, exact.ID)
		case len(partial) == 1:
			ids = append(ids, partial[0].ID)
		case len(partial) > 1:
			var matches []string
			for _, l := range partial {
				matches = append(matches, l.Name)
			}
			return nil, fmt.Errorf("label %q is ambiguous (matches %s)", input, strings.Join(matches, ", "))
		default:
			var available []string
			for _, l := range catalog {
				available = append(available, fmt.Sprintf("%s (ID: %d)", l.Name, l.ID))
			}
			if len(available) == 0 {
				return nil, fmt.Errorf("unknown label %q (the workspace has no labels)", input)
			}
			return nil, fmt.Errorf("unknown label %q. Available labels:\n  - %s", input, strings.Join(available, "\n  - "))
		}
	}
	return ids, nil
}

func init() {
	rootCmd.AddCommand(labelCmd)
	labelCmd.AddCommand(labelListCmd)

	taskCmd.AddCommand(taskLabelCmd)
	taskLabelCmd.Flags().StringSliceVar(&taskLabelAdd, "add", nil, "label name(s) or ID(s) to attach (repeatable or comma-separated)")
	taskLabelCmd.Flags().StringSliceVar(&taskLabelRemove, "remove", nil, "label name(s) or ID(s) to detach (repeatable or comma-separated)")
	taskLabelCmd.Flags().StringSliceVar(&taskLabelSet, "set", nil, "atomically replace the label set with these name(s) or ID(s)")
}
