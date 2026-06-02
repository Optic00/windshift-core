package wscli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var linkCmd = &cobra.Command{
	Use:   "link",
	Short: "Manage cross-entity links (item ↔ item, item ↔ page, item ↔ test_case)",
	Long: `Create, list, and remove links between work items, pages, and test
cases. Links carry a "link type" that defines the relationship label and,
in some cases, restricts which entity types may be linked:

  * Page    — only item ↔ page (cannot link a page to a test case or
              to another page)
  * Tests   — only item ↔ test_case
  * Others  — Implements, Depends On, Relates To, Links To, Duplicates,
              Child Of: item ↔ item only

When --type is omitted the CLI picks the single compatible type for the
(source, target) pair: Page for item↔page, Tests for item↔test_case, and
Relates To for item↔item. Pass --type NAME to override (case-insensitive).

Entity references accept:
  WI-1            work item by key-number (uses workspace from config)
  item:42         work item by numeric id
  page:42         page by numeric id
  test:5          test case by numeric id (alias: test_case:5)`,
}

var linkAddCmd = &cobra.Command{
	Use:   "add SOURCE TARGET",
	Short: "Create a link between two entities",
	Long: `Create a link from SOURCE to TARGET.

The link type is auto-selected when only one is compatible with the
(source, target) entity-type pair; pass --type NAME to choose explicitly.

Examples:
  ws link add WI-1 page:42                       # auto: Page link type
  ws link add WI-1 test:5                        # auto: Tests link type
  ws link add WI-1 WI-2                          # auto: Relates To
  ws link add WI-1 WI-2 --type Implements        # explicit item↔item type`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		srcType, srcID, err := parseEntityRef(client, args[0])
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		tgtType, tgtID, err := parseEntityRef(client, args[1])
		if err != nil {
			return fmt.Errorf("target: %w", err)
		}
		linkType, err := resolveLinkType(client, linkAddType, srcType, tgtType)
		if err != nil {
			return err
		}
		req := LinkCreateRequest{
			LinkTypeID: linkType.ID,
			SourceType: srcType,
			SourceID:   srcID,
			TargetType: tgtType,
			TargetID:   tgtID,
		}
		link, err := client.CreateLink(req)
		if err != nil {
			return fmt.Errorf("failed to create link: %w", err)
		}
		NewOutput().Print(link)
		return nil
	},
}

var linkLsCmd = &cobra.Command{
	Use:   "ls ENTITY",
	Short: "List links attached to an entity",
	Long: `List outgoing and incoming links for ENTITY.

ENTITY uses the same prefix syntax as ws link add (WI-1, item:42, page:42,
test:5).

Examples:
  ws link ls WI-1
  ws link ls page:42
  ws link ls test:5`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		entityType, id, err := parseEntityRef(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.ListLinksForEntity(entityType, id)
		if err != nil {
			return fmt.Errorf("failed to list links: %w", err)
		}
		NewOutput().Print(resp)
		return nil
	},
}

var linkRmCmd = &cobra.Command{
	Use:   "rm LINK-ID",
	Short: "Delete a link by numeric id",
	Long: `Delete a link by its numeric id (visible in ws link ls output).

The server requires edit permission on the link's source entity.

Examples:
  ws link rm 123`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid link id %q: %w", args[0], err)
		}
		client, err := NewClient()
		if err != nil {
			return err
		}
		if err := client.DeleteLink(id); err != nil {
			return fmt.Errorf("failed to delete link: %w", err)
		}
		_, _ = fmt.Fprintf(stdout, "Deleted link %d\n", id)
		return nil
	},
}

var linkTypesCmd = &cobra.Command{
	Use:   "types",
	Short: "List available link types and their allowed entity-type pairs",
	Long: `List every active link type the server exposes. Use the Name column
with --type when creating a link.

Examples:
  ws link types
  ws link types -o json`,
	RunE: func(_ *cobra.Command, _ []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		types, err := client.ListLinkTypes()
		if err != nil {
			return fmt.Errorf("failed to list link types: %w", err)
		}
		NewOutput().Print(types)
		return nil
	},
}

// linkAddType backs the --type flag on `ws link add`. Empty means "let the
// CLI auto-pick the single compatible link type for this entity-type pair".
var linkAddType string

func init() {
	rootCmd.AddCommand(linkCmd)
	linkCmd.AddCommand(linkAddCmd)
	linkCmd.AddCommand(linkLsCmd)
	linkCmd.AddCommand(linkRmCmd)
	linkCmd.AddCommand(linkTypesCmd)

	linkAddCmd.Flags().StringVar(&linkAddType, "type", "", "link type name (e.g. Implements, Depends On). Omit to auto-pick.")
}

// parseEntityRef turns a user-supplied entity reference into (entityType,
// numeric id). Accepts the prefixed forms `item:42`, `page:42`, `test:5`,
// and `test_case:5`, plus the bare `WI-1` workspace-key/number form for
// items (resolved via Client.ResolveItemID).
func parseEntityRef(client *Client, ref string) (entityType string, id int, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", 0, fmt.Errorf("empty entity reference")
	}

	if i := strings.IndexByte(ref, ':'); i > 0 {
		prefix := strings.ToLower(ref[:i])
		rest := ref[i+1:]
		id, err := strconv.Atoi(rest)
		if err != nil {
			return "", 0, fmt.Errorf("invalid numeric id in %q", ref)
		}
		switch prefix {
		case "item":
			return "item", id, nil
		case "page":
			return "page", id, nil
		case "test", "test_case", "tc":
			return "test_case", id, nil
		default:
			return "", 0, fmt.Errorf("unknown entity-type prefix %q in %q (want item:, page:, test:)", prefix, ref)
		}
	}

	// No prefix → assume item (KEY-NUMBER or numeric). ResolveItemID
	// handles both forms and reaches into workspace context for the
	// key-number lookup.
	id, err = client.ResolveItemID(ref)
	if err != nil {
		return "", 0, fmt.Errorf("could not resolve %q as an item (use item:/page:/test: prefix for other entity types): %w", ref, err)
	}
	return "item", id, nil
}

// autoLinkTypeName picks the canonical link type for an entity-type pair
// when the user didn't pass --type. (item, item) is ambiguous server-side
// but here we default to "Relates To" — the most general, bidirectional
// option — so the zero-arg flow Just Works for the common case.
//
// Returns "" when no sensible default exists; callers should require
// --type in that case.
func autoLinkTypeName(srcType, tgtType string) string {
	a, b := srcType, tgtType
	if a > b {
		a, b = b, a
	}
	switch {
	case a == "item" && b == "page":
		return "Page"
	case a == "item" && b == "test_case":
		return "Tests"
	case a == "item" && b == "item":
		return "Relates To"
	default:
		return ""
	}
}

// resolveLinkType returns the LinkType the user wants for this create, or
// an error explaining why no type fits. When `name` is non-empty it must
// match a server-side link type case-insensitively and its
// AllowedEntityTypes (if non-nil) must contain both srcType and tgtType.
// When `name` is empty it auto-picks per autoLinkTypeName.
//
// The compatibility check is a front-load: the server is authoritative
// (services/item_link_service.go:65) and would reject mismatches anyway,
// but it returns a generic "incompatible link type" error — surfacing
// the link type's actual allowed entity types is a friendlier failure.
func resolveLinkType(client *Client, name, srcType, tgtType string) (*LinkType, error) {
	types, err := client.ListLinkTypes()
	if err != nil {
		return nil, fmt.Errorf("failed to list link types: %w", err)
	}

	want := name
	if want == "" {
		want = autoLinkTypeName(srcType, tgtType)
		if want == "" {
			return nil, fmt.Errorf("no default link type for %s ↔ %s; pass --type NAME (see `ws link types`)", srcType, tgtType)
		}
	}

	for i := range types {
		lt := &types[i]
		if !lt.Active {
			continue
		}
		if !strings.EqualFold(lt.Name, want) {
			continue
		}
		if !linkTypeAllows(lt, srcType, tgtType) {
			allowed := "any"
			if len(lt.AllowedEntityTypes) > 0 {
				allowed = strings.Join(lt.AllowedEntityTypes, ", ")
			}
			return nil, fmt.Errorf("link type %q does not allow %s ↔ %s (allowed entity types: %s)", lt.Name, srcType, tgtType, allowed)
		}
		return lt, nil
	}

	if name == "" {
		return nil, fmt.Errorf("default link type %q not found on server; pass --type NAME (see `ws link types`)", want)
	}
	return nil, fmt.Errorf("link type %q not found (see `ws link types` for the catalog)", name)
}

// linkTypeAllows reports whether a link type's allowed_entity_types covers
// both endpoints. A nil/empty AllowedEntityTypes means "any combination"
// (mirrors the server-side rule).
func linkTypeAllows(lt *LinkType, srcType, tgtType string) bool {
	if len(lt.AllowedEntityTypes) == 0 {
		return true
	}
	// Mirror the server's budget check exactly: each endpoint consumes one
	// slot from allowed_entity_types. This intentionally stays as a thin
	// client-side preflight; the server remains authoritative.
	budget := make(map[string]int, len(lt.AllowedEntityTypes))
	for _, t := range lt.AllowedEntityTypes {
		budget[t]++
	}
	need := map[string]int{srcType: 1}
	need[tgtType]++
	for t, n := range need {
		if budget[t] < n {
			return false
		}
	}
	return true
}
