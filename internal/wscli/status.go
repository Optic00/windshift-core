package wscli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Status management commands",
	Long:  `Commands for listing and viewing status information.`,
}

var statusListCmd = &cobra.Command{
	Use:   "ls",
	Short: "List workspace-available statuses",
	Long: `List statuses available to items in the selected workspace.

By default this command requires a selected workspace and returns only
statuses referenced by workflows applicable in that workspace. These are
distinct from system statuses, which form the global catalog but may not be
usable by any workflow in the selected workspace.

Use --system explicitly to inspect the global status catalog. System output is
labeled as system scope and must not be treated as workspace move targets.

Examples:
	ws status ls                            # Statuses for the configured workspace
	ws status ls -w PROJ                    # Statuses available in workspace PROJ
	ws status ls --system                   # Global system status catalog`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}

		result := &StatusListResult{Scope: "system", Statuses: []Status{}}
		if statusListSystem {
			if cmd.Flags().Changed("workspace") {
				return fmt.Errorf("--system cannot be combined with --workspace")
			}
			result.Statuses, err = client.ListStatuses()
			if err != nil {
				return fmt.Errorf("failed to list system statuses: %w", err)
			}
		} else {
			wsKey := cfg.GetEffectiveWorkspace()
			if wsKey == "" {
				return fmt.Errorf("workspace is required for status listing: use -w, configure defaults.workspace_key, or pass --system for the global catalog")
			}
			wsID, resolveErr := client.ResolveWorkspaceID(wsKey)
			if resolveErr != nil {
				return fmt.Errorf("failed to resolve workspace: %w", resolveErr)
			}
			workspace, workspaceErr := client.GetWorkspace(wsID)
			if workspaceErr != nil {
				return fmt.Errorf("failed to get workspace: %w", workspaceErr)
			}
			result.Scope = "workspace"
			result.Workspace = &StatusListWorkspace{ID: workspace.ID, Key: workspace.Key, Name: workspace.Name}
			result.Statuses, err = client.GetWorkspaceStatuses(wsID)
			if err != nil {
				return fmt.Errorf("failed to list workspace statuses: %w", err)
			}
		}

		output := NewOutput()
		output.Print(result)
		return nil
	},
}

var statusListSystem bool

var itemTypeCmd = &cobra.Command{
	Use:   "item-type",
	Short: "Item type commands",
	Long:  `Commands for listing and viewing item types.`,
}

var itemTypeListCmd = &cobra.Command{
	Use:   "ls",
	Short: "List available item types",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}

		var itemTypes []ItemType
		if wsKey := cfg.GetEffectiveWorkspace(); wsKey != "" {
			wsID, resolveErr := client.ResolveWorkspaceID(wsKey)
			if resolveErr != nil {
				return fmt.Errorf("failed to resolve workspace: %w", resolveErr)
			}
			itemTypes, err = client.GetWorkspaceItemTypes(wsID)
			if err != nil {
				return fmt.Errorf("failed to list workspace item types: %w", err)
			}
		} else {
			itemTypes, err = client.ListItemTypes()
			if err != nil {
				return fmt.Errorf("failed to list item types: %w", err)
			}
		}

		output := NewOutput()
		output.Print(itemTypes)
		return nil
	},
}

var priorityCmd = &cobra.Command{
	Use:   "priority",
	Short: "Priority commands",
	Long:  `Commands for listing and viewing priorities.`,
}

var priorityListCmd = &cobra.Command{
	Use:   "ls",
	Short: "List available priorities",
	Long: `List priorities available for items.

If a workspace is configured, shows only the priorities enabled for that
workspace's configuration set. Otherwise, shows all priorities in the system.

Examples:
  ws priority ls                          # Priorities for the current workspace
  ws priority ls -w PROJ                  # Priorities for workspace PROJ`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}

		var priorities []Priority

		// Scope to the workspace's configuration set when one is configured,
		// mirroring `ws status ls`. Falls back to all priorities otherwise.
		wsKey := cfg.GetEffectiveWorkspace()
		if wsKey != "" {
			var wsID int
			wsID, err = client.ResolveWorkspaceID(wsKey)
			if err != nil {
				return fmt.Errorf("failed to resolve workspace: %w", err)
			}
			priorities, err = client.GetWorkspacePriorities(wsID)
			if err != nil {
				return fmt.Errorf("failed to list workspace priorities: %w", err)
			}
		} else {
			priorities, err = client.ListPriorities()
			if err != nil {
				return fmt.Errorf("failed to list priorities: %w", err)
			}
		}

		output := NewOutput()
		output.Print(priorities)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.AddCommand(statusListCmd)
	statusListCmd.Flags().BoolVar(&statusListSystem, "system", false, "list the global system status catalog instead of workspace-available statuses")

	rootCmd.AddCommand(itemTypeCmd)
	itemTypeCmd.AddCommand(itemTypeListCmd)

	rootCmd.AddCommand(priorityCmd)
	priorityCmd.AddCommand(priorityListCmd)
}
