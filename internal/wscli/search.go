package wscli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var searchLimit int

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Full-text search over work items",
	Long: `Search items the caller can view via the v1 search endpoint.
Multiple arguments are joined into a single query string.

The server searches across every accessible workspace; when a workspace is
configured (via -w, $WS_WORKSPACE, or defaults.workspace_key in ws.toml)
the returned page is additionally filtered to that workspace client-side.

Examples:
  ws search "login bug"
  ws search login bug --limit 5
  ws search "rate limit" -w PROJ`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runItemSearch(strings.Join(args, " "), searchLimit)
	},
}

// runItemSearch is the shared body of the item full-text search, used by
// both the top-level `ws search` and the `ws task search` alias. limit <= 0
// falls back to the server default page size.
func runItemSearch(query string, limit int) error {
	client, err := NewClient()
	if err != nil {
		return err
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return fmt.Errorf("search query must not be empty")
	}

	resp, err := client.SearchItems(query, limit)
	if err != nil {
		return fmt.Errorf("failed to search items: %w", err)
	}

	// The v1 search endpoint has no workspace filter parameter, so an
	// effective workspace narrows the returned page client-side.
	wsID, err := resolveOptionalWorkspace(client)
	if err != nil {
		return err
	}
	if wsID != nil {
		filtered := make([]Item, 0, len(resp.Data))
		for _, item := range resp.Data {
			if item.WorkspaceID == *wsID {
				filtered = append(filtered, item)
			}
		}
		NewOutput().Print(filtered)
		return nil
	}

	NewOutput().Print(resp)
	return nil
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.Flags().IntVar(&searchLimit, "limit", 0, "maximum results per page (server default if omitted, max 100)")
}
