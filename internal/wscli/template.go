package wscli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// --- flag-bound vars ---

var templateListType string

// `ws task template ...` — the agent-facing read surface for work item
// templates (WI-438). Mirrors the human create-modal picker: list the
// templates valid for a type (flagging the mandatory one), then fetch the raw
// scaffold to fill in.

var taskTemplateCmd = &cobra.Command{
	Use:   "template",
	Short: "Discover work item templates (description scaffolds)",
	Long: `Work item templates are workspace-defined reusable description bodies.
A type may enforce a "mandatory" template (auto-applied when you create an item
of that type without a description) or offer "selectable" ones.

  ws task template ls                 # all templates in the workspace
  ws task template ls --type Bug      # templates valid for the Bug type
  ws task template get bug-report     # stream the raw Markdown scaffold

Then create an item, optionally seeding the scaffold:

  ws task create -t "Login 500s" --type Bug --template bug-report`,
}

var taskTemplateListCmd = &cobra.Command{
	Use:   "ls",
	Short: "List work item templates in the current workspace",
	RunE: func(_ *cobra.Command, _ []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		typeID := 0
		if templateListType != "" {
			typeID, err = resolveItemTypeID(client, templateListType, &wsID)
			if err != nil {
				return err
			}
		}
		resp, err := client.ListItemTemplates(wsID, typeID)
		if err != nil {
			return fmt.Errorf("failed to list templates: %w", err)
		}
		NewOutput().Print(resp.Items)
		return nil
	},
}

var taskTemplateGetCmd = &cobra.Command{
	Use:   "get <name|id>",
	Short: "Stream a template's raw Markdown scaffold",
	Long: `Print the raw description_body of a template so it can be filled in and
passed to 'ws task create -d'. The body is streamed as Markdown regardless of
the global -o format.`,
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
		tmpl, err := resolveItemTemplate(client, wsID, args[0])
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, tmpl.DescriptionBody)
		return nil
	},
}

// resolveItemTemplate maps a template name or numeric ID to a template in the
// workspace. Names match case-insensitively: exact first, then unique
// substring — same convention as resolveItemTypeID / resolveLabelIDs.
func resolveItemTemplate(client *Client, workspaceID int, input string) (*ItemTemplate, error) {
	input = strings.TrimSpace(input)
	if id, err := strconv.Atoi(input); err == nil {
		if id <= 0 {
			return nil, fmt.Errorf("template ID must be positive: %s", input)
		}
		tmpl, gerr := client.GetItemTemplate(workspaceID, id)
		if gerr != nil {
			return nil, fmt.Errorf("failed to get template %d: %w", id, gerr)
		}
		return tmpl, nil
	}

	resp, err := client.ListItemTemplates(workspaceID, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to list templates: %w", err)
	}
	inputLower := strings.ToLower(input)
	var exact *ItemTemplate
	var partial []ItemTemplate
	for i := range resp.Items {
		nameLower := strings.ToLower(resp.Items[i].Name)
		if nameLower == inputLower {
			exact = &resp.Items[i]
			break
		}
		if strings.Contains(nameLower, inputLower) {
			partial = append(partial, resp.Items[i])
		}
	}
	switch {
	case exact != nil:
		return exact, nil
	case len(partial) == 1:
		return &partial[0], nil
	case len(partial) > 1:
		var matches []string
		for _, t := range partial {
			matches = append(matches, t.Name)
		}
		return nil, fmt.Errorf("template %q is ambiguous (matches %s)", input, strings.Join(matches, ", "))
	default:
		if len(resp.Items) == 0 {
			return nil, fmt.Errorf("unknown template %q (the workspace has no templates)", input)
		}
		var available []string
		for _, t := range resp.Items {
			available = append(available, fmt.Sprintf("%s (ID: %d)", t.Name, t.ID))
		}
		return nil, fmt.Errorf("unknown template %q. Available templates:\n  - %s", input, strings.Join(available, "\n  - "))
	}
}

func init() {
	taskCmd.AddCommand(taskTemplateCmd)
	taskTemplateCmd.AddCommand(taskTemplateListCmd)
	taskTemplateCmd.AddCommand(taskTemplateGetCmd)
	taskTemplateListCmd.Flags().StringVar(&templateListType, "type", "", "filter to templates valid for this item type (name or ID)")
}
