package wscli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var (
	pageDiagramName         string
	pageDiagramMermaid      string
	pageDiagramExcalidraw   string
	pageDiagramFromFile     string
	pageDiagramPlacement    string
	pageDiagramExpectedHash string
)

var pageDiagramCmd = &cobra.Command{
	Use:   "diagram",
	Short: "Manage diagrams embedded in Pages",
	Long: `Create, list, get, and update immutable attachment-backed diagrams
embedded in workspace Pages.

Every mutation can carry --expected-content-hash so concurrent Page edits fail
instead of overwriting newer Markdown.`,
}

var pageDiagramCreateCmd = &cobra.Command{
	Use:   "create <page-id>",
	Short: "Create and embed a Page diagram",
	Long: `Create a diagram and insert its Markdown fence at --placement start
or end. Provide exactly one of --mermaid, --excalidraw, or --from-file.

Examples:
  ws page diagram create 42 --name "Auth flow" --mermaid "graph TD; A-->B" --placement end
  ws page diagram create 42 --name Architecture --from-file scene.json --expected-content-hash abc123`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		if strings.TrimSpace(pageDiagramName) == "" {
			return fmt.Errorf("--name is required")
		}
		pageID, err := parsePageDiagramID(args[0], "page")
		if err != nil {
			return err
		}
		mermaid, excalidraw, err := pageDiagramInput()
		if err != nil {
			return err
		}
		client, workspaceID, err := pageDiagramClient()
		if err != nil {
			return err
		}
		diagram, err := client.CreatePageDiagram(workspaceID, pageID, PageDiagramCreateRequest{
			Name:                pageDiagramName,
			Mermaid:             mermaid,
			Excalidraw:          excalidraw,
			Placement:           pageDiagramPlacement,
			ExpectedContentHash: optionalPageDiagramHash(),
		})
		if err != nil {
			return translatePageDiagramError(err, "create", pageID, 0)
		}
		NewOutput().Print(diagram)
		return nil
	},
}

var pageDiagramListCmd = &cobra.Command{
	Use:   "list <page-id>",
	Short: "List diagrams embedded in a Page",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		pageID, err := parsePageDiagramID(args[0], "page")
		if err != nil {
			return err
		}
		client, workspaceID, err := pageDiagramClient()
		if err != nil {
			return err
		}
		diagrams, err := client.ListPageDiagrams(workspaceID, pageID)
		if err != nil {
			return translatePageDiagramError(err, "list", pageID, 0)
		}
		NewOutput().Print(diagrams)
		return nil
	},
}

var pageDiagramGetCmd = &cobra.Command{
	Use:   "get <page-id> <attachment-id>",
	Short: "Get a Page diagram",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		pageID, err := parsePageDiagramID(args[0], "page")
		if err != nil {
			return err
		}
		attachmentID, err := parsePageDiagramID(args[1], "attachment")
		if err != nil {
			return err
		}
		client, workspaceID, err := pageDiagramClient()
		if err != nil {
			return err
		}
		diagram, err := client.GetPageDiagram(workspaceID, pageID, attachmentID)
		if err != nil {
			return translatePageDiagramError(err, "get", pageID, attachmentID)
		}
		NewOutput().Print(diagram)
		return nil
	},
}

var pageDiagramUpdateCmd = &cobra.Command{
	Use:   "update <page-id> <attachment-id>",
	Short: "Replace a Page diagram",
	Long: `Replace a Page diagram with a new immutable attachment and update its
Markdown fence. Provide exactly one of --mermaid, --excalidraw, or --from-file.
The existing attachment remains available to Page revision history.`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		pageID, err := parsePageDiagramID(args[0], "page")
		if err != nil {
			return err
		}
		attachmentID, err := parsePageDiagramID(args[1], "attachment")
		if err != nil {
			return err
		}
		mermaid, excalidraw, err := pageDiagramInput()
		if err != nil {
			return err
		}
		client, workspaceID, err := pageDiagramClient()
		if err != nil {
			return err
		}
		diagram, err := client.UpdatePageDiagram(workspaceID, pageID, attachmentID, PageDiagramUpdateRequest{
			Name:                pageDiagramName,
			Mermaid:             mermaid,
			Excalidraw:          excalidraw,
			ExpectedContentHash: optionalPageDiagramHash(),
		})
		if err != nil {
			return translatePageDiagramError(err, "update", pageID, attachmentID)
		}
		NewOutput().Print(diagram)
		return nil
	},
}

func pageDiagramClient() (*Client, int, error) {
	client, err := NewClient()
	if err != nil {
		return nil, 0, err
	}
	workspaceID, err := resolveRequiredWorkspace(client)
	if err != nil {
		return nil, 0, err
	}
	return client, workspaceID, nil
}

func parsePageDiagramID(raw, kind string) (int, error) {
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s id: %s", kind, raw)
	}
	return id, nil
}

func pageDiagramInput() (mermaid string, excalidraw json.RawMessage, err error) {
	payload, err := buildDiagramPayload(pageDiagramMermaid, pageDiagramExcalidraw, pageDiagramFromFile)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(pageDiagramMermaid) != "" {
		return strings.TrimSpace(pageDiagramMermaid), nil, nil
	}
	return "", json.RawMessage(payload), nil
}

func optionalPageDiagramHash() *string {
	hash := strings.TrimSpace(pageDiagramExpectedHash)
	if hash == "" {
		return nil
	}
	return &hash
}

func translatePageDiagramError(err error, operation string, pageID, attachmentID int) error {
	apiErr := apiErrorFromError(err)
	resource := fmt.Sprintf("Page %d", pageID)
	if attachmentID > 0 {
		resource = fmt.Sprintf("diagram attachment %d on Page %d", attachmentID, pageID)
	}
	if apiErr == nil {
		return fmt.Errorf("failed to %s %s: %w", operation, resource, err)
	}
	switch apiErr.Status {
	case 400, 413:
		return fmt.Errorf("invalid diagram payload: %w", err)
	case 409:
		return fmt.Errorf("stale Page content or ambiguous diagram reference: refresh the Page content hash and retry: %w", err)
	case 404:
		if attachmentID > 0 {
			return fmt.Errorf("%s was not found, or you lack access to its Page: %w", resource, err)
		}
		return fmt.Errorf("%s was not found, or you lack page access: %w", resource, err)
	case 403:
		return fmt.Errorf("token lacks the required pages API scope to %s %s: %w", operation, resource, err)
	default:
		return fmt.Errorf("failed to %s %s: %w", operation, resource, err)
	}
}

func init() {
	pageCmd.AddCommand(pageDiagramCmd)
	pageDiagramCmd.AddCommand(pageDiagramCreateCmd, pageDiagramListCmd, pageDiagramGetCmd, pageDiagramUpdateCmd)

	pageDiagramCreateCmd.Flags().StringVar(&pageDiagramName, "name", "", "diagram name")
	pageDiagramUpdateCmd.Flags().StringVar(&pageDiagramName, "name", "", "optional replacement diagram name")
	pageDiagramCreateCmd.Flags().StringVar(&pageDiagramPlacement, "placement", "end", "insert fence at start or end of the Page")

	for _, command := range []*cobra.Command{pageDiagramCreateCmd, pageDiagramUpdateCmd} {
		command.Flags().StringVar(&pageDiagramMermaid, "mermaid", "", "mermaid source (stored as a seed wrapper)")
		command.Flags().StringVar(&pageDiagramExcalidraw, "excalidraw", "", "Excalidraw scene JSON (inline)")
		command.Flags().StringVar(&pageDiagramFromFile, "from-file", "", "path to an Excalidraw scene JSON file (use - for stdin)")
		command.Flags().StringVar(&pageDiagramExpectedHash, "expected-content-hash", "", "fail if the Page content hash has changed")
	}
}
