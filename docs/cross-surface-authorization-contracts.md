# Cross-surface application and authorization contracts

This document records the contract shared by the cookie API, REST v1, and MCP.
It is the implementation inventory for WI-742 and the authorization mapping for
WI-739. Update it whenever a mapped endpoint or MCP tool changes its scope or
domain permission.

## Authorization layers

Every operation must pass each applicable layer. A token scope never grants a
workspace or record permission that the underlying user lacks.

| Layer | Cookie API | REST v1 | MCP |
| --- | --- | --- | --- |
| Authentication | Session cookie | API bearer token | API bearer token issued for the MCP resource |
| Capability scope | Not applicable | Route `RequirePermission(...)` middleware | `aitools.Tool.Scopes`, enforced before dispatch |
| Workspace discovery | Request-specific permission check | Fresh `PermissionService` / `authz` check | Fresh `AccessibleWorkspaceIDs` snapshot per request |
| Domain permission | Shared permission/application service | Shared permission/application service | Shared permission/application service |
| Existence masking | 404 for inaccessible workspace/item records | 404 for inaccessible workspace/item records | Tool result reports `not found`; MCP transport remains successful |
| Error envelope | Cookie API error JSON | REST v1 `APIError` JSON | MCP tool content or protocol error for missing scopes |

The different envelopes are intentional. Tests compare the authorization
decision and side effects, not identical wire bodies across protocols.

## Recurrence inventory

Recurrence is the first WI-742 migration. Both HTTP surfaces delegate the
operations below to `services.RecurrenceService`; handlers retain only auth,
path/body decoding, pagination conventions, and response formatting.

| Operation | Cookie API | REST v1 | User permission | Token scope | Intentional difference |
| --- | --- | --- | --- | --- | --- |
| Get rule | `GET /api/items/{id}/recurrence` | `GET /rest/api/v1/items/{id}/recurrence` | `item.view` | `items:read` | Both return JSON `null` when no rule exists |
| Create rule | `POST /api/items/{id}/recurrence` | `POST /rest/api/v1/items/{id}/recurrence` | `item.edit` | `items:write` | Surface-specific validation envelope |
| Update rule | `PUT /api/items/{id}/recurrence` | `PUT /rest/api/v1/items/{id}/recurrence` | `item.edit` | `items:write` | Surface-specific validation envelope |
| Delete rule | `DELETE /api/items/{id}/recurrence` | `DELETE /rest/api/v1/items/{id}/recurrence` | `item.edit` | `items:write` | Same 204 outcome |
| List instances | `GET /api/items/{id}/recurrence/instances` | `GET /rest/api/v1/items/{id}/recurrence/instances` | `item.view` | `items:read` | Cookie uses limit/offset (default 20); v1 uses page/limit (default 50) |
| Force generation | `POST /api/items/{id}/recurrence/generate` | `POST /rest/api/v1/items/{id}/recurrence/generate` | `item.edit` | `items:write` | Same generated-count payload |
| Preview RRULE | `POST /api/recurrence/preview` | `POST /rest/api/v1/recurrence-rules/preview` | Authenticated user | `items:read` | Paths and error envelopes differ |
| List workspace rules | `GET /api/workspaces/{id}/recurrence-rules` | No v1 route | `item.view` | Not applicable | Cookie-only administrative listing |

Shared recurrence behavior includes identifier sanitization, RRULE/date
validation, defaults, persistence, rule lookup, instance lookup/counting,
bounded preview iteration, and scheduler dispatch. RRULE values longer than the
identifier limit are rejected before sanitization so truncation cannot change
their meaning. The shared creation path also enforces the atomic workspace
quota and common conflict response documented in
[Recurrence scheduling safeguards](recurrence-scheduling-safeguards.md).

## Test-run execution inventory

Cookie and REST v1 test-run handlers now retain transport concerns only. The
execution read model and mutation rules below live in
`services.TestRunService`; `record_test_result` uses the same result mutation
method as both HTTP surfaces.

| Operation | Cookie API | REST v1 | MCP | User permission | Token scope |
| --- | --- | --- | --- | --- | --- |
| Create run | `POST /api/workspaces/{workspaceId}/test-runs` | `POST /rest/api/v1/workspaces/{workspaceId}/test-runs` | `start_test_run` | `test.execute` | `tests:write` |
| Update run | `PUT /api/workspaces/{workspaceId}/test-runs/{id}` | `PUT /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}` | No direct equivalent | `test.execute` | `tests:write` |
| Complete run | `POST /api/workspaces/{workspaceId}/test-runs/{id}/end` | `POST /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/end` | `end_test_run` | `test.execute` | `tests:write` |
| Get execution detail | `GET /api/workspaces/{workspaceId}/test-runs/{id}/detail` | `GET /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/detail` | No direct equivalent | `test.view` | `tests:read` |
| List case results | `GET /api/workspaces/{workspaceId}/test-runs/{id}/results` | `GET /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/results` | Used internally by run tools | `test.view` | `tests:read` |
| Update case result | `PUT /api/workspaces/{workspaceId}/test-runs/{id}/results/{resultId}` | `PUT /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/results/{resultId}` | `record_test_result` | `test.execute` | `tests:write` |
| List step results | `GET /api/workspaces/{workspaceId}/test-runs/{id}/steps` | `GET /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/steps` | No direct equivalent | `test.view` | `tests:read` |
| Upsert step result | `PUT /api/workspaces/{workspaceId}/test-runs/{id}/steps/{stepId}` | `PUT /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/steps/{stepId}` | No direct equivalent | `test.execute` | `tests:write` |
| Link/list/unlink result items | `/api/workspaces/{workspaceId}/test-results/{resultId}/items` | `/rest/api/v1/workspaces/{workspaceId}/test-results/{resultId}/items` | No direct equivalent | `test.execute` to mutate; `test.view` to list | `tests:write` / `tests:read` |

Shared execution behavior includes workspace and run/result ownership checks,
run-name and rich-text sanitization, result-status validation, step-result
upsert, parent case-status aggregation (`failed` > `blocked` > `skipped` >
`passed` > `not_run`), linked-item workspace validation, and the complete
detail/result/step response models. MCP intentionally addresses a result by
test-case ID while HTTP addresses its result-row ID, and MCP does not expose
`not_run` as a `record_test_result` input.

The WI-739 contract test covers all three authorization layers for execution:
a missing `tests:write` token scope is 403/protocol-denied, a Viewer with that
scope is still existence-masked by `test.execute`, and a Tester succeeds on
both REST v1 and MCP. Cookie and REST v1 parity tests separately prove that
mutations are mutually visible and use the same sanitization and validation.

## Test catalog inventory

Test sets and run templates are the next WI-742 migration. Cookie and REST v1
handlers delegate catalog rules to `services.TestSetService` and
`services.TestRunTemplateService`; MCP template execution uses the same
template service.

| Operation family | Cookie API | REST v1 | MCP | Domain permission | Token scope |
| --- | --- | --- | --- | --- | --- |
| List/get test sets | `/api/workspaces/{workspaceId}/test-sets` | `/rest/api/v1/workspaces/{workspaceId}/test-sets` | Set lookup inside `start_test_run` | `test.view` or `test.execute` for execution | `tests:read` / `tests:write` |
| Create/update/delete test sets | `/api/workspaces/{workspaceId}/test-sets` | `/rest/api/v1/workspaces/{workspaceId}/test-sets` | No direct equivalent | `test.manage` | `tests:write` |
| Attach/list/detach set cases | `/api/workspaces/{workspaceId}/test-sets/{id}/test-cases` | `/rest/api/v1/workspaces/{workspaceId}/test-sets/{id}/test-cases` | No direct equivalent | `test.manage` to mutate; `test.view` to list | `tests:write` / `tests:read` |
| List set runs | `/api/workspaces/{workspaceId}/test-sets/{id}/runs` | `/rest/api/v1/workspaces/{workspaceId}/test-sets/{id}/runs` | No direct equivalent | `test.view` | `tests:read` |
| List/get run templates | `/api/workspaces/{workspaceId}/test-run-templates` | `/rest/api/v1/workspaces/{workspaceId}/test-run-templates` | Template lookup inside `start_test_run` | `test.view` or `test.execute` for execution lookup | `tests:read` / `tests:write` |
| Create/update/delete run templates | `/api/workspaces/{workspaceId}/test-run-templates` | `/rest/api/v1/workspaces/{workspaceId}/test-run-templates` | No direct equivalent | `test.manage` | `tests:write` |
| Execute template/list executions | `/api/workspaces/{workspaceId}/test-run-templates/{id}/execute` | `/rest/api/v1/workspaces/{workspaceId}/test-run-templates/{id}/execute` | `start_test_run` with `template_id` | `test.execute` to execute; `test.view` to list | `tests:write` / `tests:read` |

Shared catalog behavior includes name/description sanitization, milestone
workspace validation, set/case workspace ownership, template/set workspace
ownership, execution naming, and transactional run/result creation. Template
execution now uses the canonical run lifecycle on every surface, so generated
case results consistently start as `not_run` rather than the former
repository-only `pending` value.

## Item creation inventory

Cookie and REST v1 item creation now delegate their matched domain pipeline to
`services.ItemCreationService`. The handlers retain authentication, workspace
permission decisions, request DTO mapping, project-name masking, and their
surface-specific response/error envelopes.

| Shared operation | Cookie API | REST v1 | Domain permission | Token scope | Intentional difference |
| --- | --- | --- | --- | --- | --- |
| Create item | `POST /api/items` | `POST /rest/api/v1/items` | `item.edit` | `items:write` | Cookie returns the internal item model; v1 returns the public DTO and mandatory-template summary |

The shared creation operation owns title/description sanitization, hierarchy
and related-item validation, parent normalization, project-inheritance
defaulting, custom-field validation and canonical JSON, planning/project/type/
workflow validation through the transactional creator, full-item hydration,
and committed-item event emission. Production passes the same fully wired
event coordinator to both surfaces, so bearer-created items now trigger the
same notification, automation, and webhook pipeline as cookie-created items.

## Item update inventory

Cookie and REST v1 item updates now delegate their matched application pipeline
to `services.ItemUpdateApplicationService`. Handlers retain authentication,
item/workspace permission checks, request decoding, public response mapping,
project-name masking, and their surface-specific error envelopes.

| Shared operation | Cookie API | REST v1 | Domain permission | Token scope | Intentional difference |
| --- | --- | --- | --- | --- | --- |
| Update item fields | `PUT /api/items/{id}` | `PUT /rest/api/v1/items/{id}` | `item.edit` | `items:write` | Cookie accepts its legacy map payload; v1 maps the public update DTO and preserves explicit JSON `null` for nullable fields |

The shared update operation owns edit-activity tracking, transactional field
validation/persistence/history through `ItemUpdateService`, effective-project
cache invalidation for project/reparent changes, committed-item event emission,
and description-mention reconciliation. Production passes the same fully wired
service instance to both handlers, so bearer updates no longer skip the
notification, automation, webhook, activity, cache, or mention side effects
performed by cookie updates. `status_id` and `item_type_id` remain rejected on
this generic update path; callers must use the dedicated transition and
change-type operations.

## Item deletion inventory

Cookie, REST v1, and MCP destructive item operations now delegate authorization
and committed side effects to `services.ItemDeletionApplicationService`.

| Operation | Cookie API | REST v1 | MCP | Domain permission | Token scope | Intentional difference |
| --- | --- | --- | --- | --- | --- | --- |
| Delete one item | `DELETE /api/items/{id}` | No equivalent | No equivalent | `item.delete` | Not applicable | Legacy cookie-only operation preserves descendants |
| Cascade delete | `DELETE /api/items/{id}/cascade` | `DELETE /rest/api/v1/items/{id}` | `delete_item` | `item.delete` | `items:delete` | Cookie returns `deletedCount`; REST returns 204; MCP returns its tool result |

The shared application operation owns the exact `item.delete` permission
decision, single/cascade transaction selection, deleted and descendant counts,
hierarchy/project cache invalidation, and committed delete event. This fixes
REST v1 previously treating general item-edit permission as sufficient to
delete. Production shares the fully wired service with MCP as well, so MCP and
REST deletes emit the same notification/webhook effects as cookie deletes.
Request/tool audit metadata remains transport-owned: cookie and REST persist
request-aware audit rows, while MCP writes its source-tagged tool audit.

## Consolidation measurements

The cumulative scoped test-management `jscpd --reporters ai --min-lines 10`
report across the five cookie handlers and the REST v1 test handler changed
from 24 clones (5.8%) at 5,138 lines, to 16 clones (3.8%) at 4,717 lines after
the execution slice, and now to 13 clones (3.3%) at 4,476 lines after the
catalog slice. That is 11 fewer clones and 662 fewer handler lines (12.9%)
across the two slices.

For the catalog slice alone, the same AI reporter over the four catalog cookie
handlers plus the v1 handler changed from 12 clones (3.2%) at 4,229 lines to
10 clones (2.9%) at 3,987 lines. The extracted set/template services add 250
lines of reusable domain behavior while the obsolete generic workspace checker
and repository-only template execution path were deleted. Remaining clones are
small transport-decoding and response patterns; the report did not identify
another domain rule that should move into this slice.

For the item-creation slice, the two item handlers changed from 12 clones at
6.4% across 3,170 lines to 11 clones at 6.5% across 2,972 lines. The percentage
rose slightly because 198 handler lines were removed while the remaining clone
tokens stayed nearly constant. Including the new 155-line shared service, the
measured item-creation production scope is 3,127 lines, a net reduction of 43
lines (1.4%). The post-refactor AI report confirms the creation clone was
removed; the remaining high-impact matches are read transport patterns and
legacy delete-family orchestration reserved for subsequent slices.

For the item-update slice, the scoped AI report stayed at 11 clones and 6.5%:
the matched blocks are read/delete transport patterns, while the update drift
was semantic side-effect orchestration rather than token-identical code. The
two handlers decreased from 2,972 to 2,947 lines; the 129-line shared
application service makes the measured production scope 3,076 lines. The
report therefore guided this slice toward the behavior gap—especially missing
REST-v1 mention/event/cache/activity effects—without forcing unrelated
transport clones into the service.

For the destructive item slice, the scoped AI report across cookie, REST v1,
and MCP item implementations improved from 17 clones / 7.1% / 3,830 lines to
12 clones / 4.2% / 3,793 lines. The extracted 161-line application service
makes that measured production scope 3,954 lines. Although this slice adds 124
lines to the scoped footprint, it removes five orchestration clones and closes
the independently implemented permission/event/cache behavior that caused the
REST delete-policy drift.

For the page application slice, cookie HTTP, REST v1, and MCP mutations use one
`PageApplicationService`. It owns page-create permission, parent edit checks,
partial-update merge behavior, move destination checks, root/subtree archive
authorization, revision restore, ACL mutation checks, inheritance changes, and
post-commit audit emission. Adapters retain authentication, token scopes,
accessible-workspace calculation, existence-masking presentation, and response
shapes. Equivalent mutations now emit canonical `page.*` audit actions;
`details.auth_method` distinguishes cookie/bearer HTTP and `details.source`
identifies MCP/AI-tool writes.

The page-scoped jscpd report across cookie, REST v1, and AI-tool adapters
improved from 7 clones / 3.6% / 2,285 lines to 6 clones / 3.4% / 2,133 lines.
The 272-line application service makes the measured page slice 2,405
production lines. This adds 120 lines to that local scope while removing 152
handler/tool lines and, more importantly, replaces three independently audited
mutation paths with one permission and side-effect boundary.

Across the complete migrated adapter scope (cookie handlers, REST v1 handlers,
and item/page AI tools), production code decreased from 11,737 to 10,088 lines:
1,649 fewer lines, or 14.0%. Including the extracted shared services and the
repositories directly changed for these slices, the comparable implementation
scope decreased from 12,280 to 12,077 lines: a net reduction of 203 production
lines, or 1.7%. A final comparable handler-wide `jscpd --min-lines 10` scan
improved from 414 clones / 5.1% to 395 clones / 4.9%. These cumulative numbers
use fixed file sets and options; they are not compared with the issue's earlier
default-scan number because that scan used different inputs and thresholds.

The final WI-742 contract matrix passed against both SQLite and PostgreSQL 17.
It covers recurrence, test execution and catalog management, item
create/update/delete, page mutations, and the linked MCP/REST authorization
contracts. The PostgreSQL run also exposed an older item-update fixture that
used SQLite-only integer booleans and `LastInsertId`; the fixture now uses a
real boolean and portable `INSERT ... RETURNING id`, and its focused SQLite
regression remains green.

## MCP to REST v1 mapping

The executable scope declarations live beside each MCP tool in
`internal/aitools`; corresponding route scopes live in
`internal/restapi/v1/router.go`. This table records the domain-policy mapping
and intentional surface gaps. The cross-surface contract tests exercise the
high-risk read/write/delete and existence-masking rows.

| MCP tools | REST v1 operation family | MCP / REST scope | Domain authorization |
| --- | --- | --- | --- |
| `list_items`, `get_item`, `search_items`, `get_item_children` | Item list/get/search | `items:read` | Accessible workspace plus `item.view` |
| `create_item`, `update_item`, `transition_item` | Item create/update/transition | `items:write` | Accessible workspace plus `item.edit`; shared validation/workflow rules |
| `delete_item` | Item delete | `items:delete` | Accessible workspace plus `item.delete` |
| `list_comments` | Item comments read | `items:read` | Item must be visible |
| `add_comment`, `update_comment` | Comment create/update | `items:write` | `item.edit`; update also enforces owner or edit-others permission |
| `delete_comment` | Comment delete | `items:delete` | Owner or `comment.edit_others` permission |
| `get_page`, `list_pages`, `get_page_permissions`, `search_knowledge` | Page read/search/ACL read | `pages:read` | Workspace access plus effective page ACL |
| `create_page`, `update_page`, `move_page`, `restore_page_revision`, ACL mutations | Page write/move/history/ACL routes | `pages:write` | Page create/edit/admin rules as applicable |
| `archive_page` | Page archive | `pages:delete` | Workspace page-delete plus subtree page-admin |
| Test-case/run read tools | Test management reads | `tests:read` | Workspace test-view permission |
| Test-run lifecycle/result tools | Test run mutations | `tests:write` | Workspace test-execute/edit permission as applicable |
| `list_time_projects`, `list_worklogs` | Time project/worklog reads | `time:read` | Per-project access and own-worklog policy |
| `log_time`, `start_timer`, `stop_timer` | Worklog/timer mutations | `time:write` | Per-project access; linked item must also be visible |
| Diagram tools | `/items/{id}/diagrams`, `/diagrams/{id}` | `items:read` or `items:write` | Parent item view/edit permission |
| Link tools | `/links`, entity link lists | `items:read` or `items:write` | Source edit and target view checks by entity type |

The WI-739 page-ACL contract exercises the page rows above across REST v1 and
MCP. It proves that a missing `pages:read` scope is denied at the token gate; a
workspace Viewer cannot discover a page whose inheritance is disabled and has
no matching ACL; an explicit user `view` grant enables reads on both surfaces;
and that view grant still existence-masks REST/MCP updates. The denial test
also reloads the page as an administrator to prove neither attempted update
changed persisted title or content.

The page mutation contract additionally proves that cookie, REST v1, and MCP
partial updates preserve omitted fields, share the same committed state, and
write exactly one canonical audit row per successful mutation with the
initiating transport recorded. Denied and empty mutations write neither a page
change nor an audit row.

The WI-739 comment contract proves the owner-or-`comment.edit_others` rule for
both REST v1 and MCP. A comment author can update their own row, an Editor with
general item-edit permission is existence-masked when attempting to update or
delete another author's row, and a workspace Administrator can update and
delete through both surfaces. Denial coverage reloads the comment as its author
to prove neither attempted mutation changed persisted content. REST v1 now
checks the dedicated comment permission rather than treating general workspace
edit access as an ownership override.

The WI-739 time-project contract proves the per-project manager/member boundary
on both REST v1 and MCP. A missing `time:read` scope is denied at the token
gate; a user outside the configured project ACL cannot discover the project in
single or list reads and cannot book time even with `time:write`; and an
explicit project manager/member can read and create worklogs through both
surfaces. The allowed case verifies both worklogs and their exact total
duration in persisted state.

The WI-739 destructive-item contract proves that `items:delete` is enforced at
both token gates, an Editor with item-edit permission still cannot delete, an
inaccessible workspace is existence-masked, and an Administrator cascade
deletes both the root and descendants through REST v1 and MCP. Every denial
reloads the target as an administrator to prove no destructive write occurred.

### Intentional MCP-only or REST-only behavior

- MCP requires the additional `mcp:access` transport scope. REST v1 does not.
- MCP item details may truncate long descriptions unless explicitly requested;
  REST v1 returns its normal DTO.
- MCP denials that must not leak an entity are returned as tool content such as
  `item not found`; REST v1 uses HTTP 404.
- `get_item_approvals` is MCP-only and follows the item-read scope and approval
  actor policy because there is no equivalent REST v1 route.
- Action validation/catalog and embedded action-template discovery have MCP
  tools without one-to-one REST routes; they still use the same action scopes
  and `action.manage` permission checks as persisted action operations.
- Cookie recurrence workspace listing has no REST v1 or MCP counterpart.
- Cookie `DELETE /items/{id}` is deliberately single-item deletion; REST v1
  and MCP match cookie `DELETE /items/{id}/cascade`.

## Change checklist

When adding or changing a mapped operation:

1. Update the MCP tool's `Scopes` and the REST v1 route middleware together.
2. Keep workspace, ownership, ACL, and destructive-operation checks in a shared
   authorization-aware service or shared authorization primitive.
3. Extend cross-surface tests with the allowed case and the exact denial case,
   including cross-workspace behavior where an entity ID is accepted.
4. Record any intentional protocol-only behavior in this document.
5. Run the contract tests with SQLite and PostgreSQL.
