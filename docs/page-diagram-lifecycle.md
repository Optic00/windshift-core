# Page diagram storage and lifecycle

Page diagrams are editable Excalidraw scenes embedded in workspace knowledge
Pages. They use the existing Page attachment and revision systems rather than a
second diagram table.

## Storage contract

A live Page references a diagram with a fenced Markdown block:

````markdown
```excalidraw
{"attachmentId":123,"name":"Authentication flow"}
```
````

The referenced attachment:

- has `entity_type = page` and `item_id = <page id>`;
- stores a JSON payload under the configured attachment root;
- is owned by exactly one Page;
- is immutable after a successful Page mutation.

The payload is either:

- an Excalidraw scene object with an `elements` array and optional object-valued
  `appState` and `files`; or
- a Mermaid seed object, `{"type":"mermaid","source":"..."}`.

Mermaid seeds are converted in the browser when first opened for editing. The
next save writes a normal Excalidraw scene as a new attachment.

## Shared lifecycle

`services.PageDiagramService` is the single lifecycle implementation used by
the cookie-auth Page editor, bearer REST API, MCP tools, AI tool registry, and
`ws` CLI.

Create performs these steps:

1. Validate the name, payload, placement, Page access, and optional content
   hash.
2. Upload a new Page-owned attachment.
3. Insert the fence at the explicit `start` or `end` placement.
4. Update the Page through `PageApplicationService`, producing a Page revision
   and normal Page audit event.
5. Delete the uploaded attachment if the Page mutation fails.

Update follows the same pattern, but requires the old attachment ID to appear
in exactly one fence in the current Page. It uploads a replacement attachment
and swaps that fence atomically. The old attachment is intentionally retained
because older Page revisions may still reference it.

There is no diagram delete operation. Removing a fence is a normal Page edit;
retention and eventual cleanup must remain revision-aware.

## Concurrency and revisions

Mutations accept `expected_content_hash`. A mismatch returns a conflict without
changing Page content and without leaving the newly uploaded attachment behind.
Clients should refresh the Page, reconcile changes, and retry with its current
hash.

Every successful create or update creates a normal Page revision. Restoring an
older revision restores its older fence and attachment ID. Since successful
replacement attachments are immutable and retained, the restored diagram is
still readable and editable.

## Authorization and ownership

Listing and reading require effective `page.view`. Creating and replacing
require effective `page.edit`. Permission denials and missing Pages or diagrams
are masked as not found where the Page security policy requires it.

An attachment ID is valid only for its owning Page. A fence copied into another
Page does not grant access and is omitted or rejected by the diagram service.
Duplicate references to the same attachment in one Page are rejected for
updates because the replacement target would be ambiguous.

Bearer REST routes also require `pages:read` or `pages:write`. Cookie routes
use the authenticated browser session. MCP and AI tools apply their normal
workspace allowlist before calling the shared service.

## Public surfaces

Cookie editor:

- `GET|POST /api/workspaces/{workspaceId}/pages/{pageId}/diagrams`
- `GET|PUT /api/workspaces/{workspaceId}/pages/{pageId}/diagrams/{attachmentId}`

Bearer REST:

- `GET|POST /rest/api/v1/workspaces/{workspaceId}/pages/{pageId}/diagrams`
- `GET|PUT /rest/api/v1/workspaces/{workspaceId}/pages/{pageId}/diagrams/{attachmentId}`

MCP and AI tools:

- `create_page_diagram`
- `list_page_diagrams`
- `get_page_diagram`
- `update_page_diagram`

CLI:

- `ws page diagram create <page-id>`
- `ws page diagram list <page-id>`
- `ws page diagram get <page-id> <attachment-id>`
- `ws page diagram update <page-id> <attachment-id>`

All mutation surfaces support Mermaid or Excalidraw input and the optimistic
content-hash precondition. Create additionally requires explicit placement.

## Validation limits

The shared payload validator rejects:

- missing or simultaneous Mermaid and Excalidraw inputs;
- malformed JSON or non-object Excalidraw payloads;
- missing/non-array `elements`;
- elements without a non-empty string `id`;
- non-object `appState` or `files`; and
- payloads larger than `services.MaxDiagramPayloadBytes`.

Keep validation in the shared service so new surfaces cannot accidentally
accept a payload the Page editor cannot load.
