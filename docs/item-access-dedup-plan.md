# Item Access Deduplication Plan

## Goal

Reduce redundant item and item-link SQL by establishing a small set of canonical repository/service entry points for item access, item hydration, hierarchy traversal, creation/copy/delete workflows, and link traversal.

## Non-goals

- Do not redesign the `items` schema.
- Do not change API response contracts unless a test proves an inconsistency is already a bug.
- Do not collapse intentionally specialized DTO queries that are clearly aggregation/reporting-only.

## Current high-impact duplication

1. Common item hydration SQL is repeated across `ItemRepository`, `ItemCRUDService`, `ItemUpdateService`, hierarchy code, item-link code, and handlers.
2. Base item scanning/null handling is duplicated between `internal/repository` and `internal/services`.
3. Hierarchy traversal exists in both repository and service layers with overlapping recursive CTEs and divergent selected fields/order/depth behavior.
4. Copy/delete flows are split between handlers, services, and repositories.
5. Item-link traversal and hydration has a central service path, but several handlers still mint outgoing/incoming SQL directly.

## Phase 0 — Safety net and contracts

### 0.1 Add/extend characterization tests

Target the current behavior before refactoring:

- `ItemRepository.FindByID`, `FindByIDWithDetails`, `FindAllWithDetails`
- `ItemUpdateService.UpdateItem` response hydration
- `HierarchyService.GetAncestors`, `GetDescendants`, `GetChildren`, `GetRoot`
- `ItemCRUDService.Delete`, handler `Delete`, handler `DeleteCascade`
- `ItemLinkService.ListLinksForEntityWithChecks`, asset linked-item/linked-asset handlers

Assertions should cover nullable fields, custom fields, milestones, workspace metadata, status/type/priority names, parent fields, and permission-hidden 404 behavior.

### 0.2 Capture query-count baselines

For list/get/hierarchy/link endpoints, add lightweight benchmark or integration logs to detect accidental N+1 regressions, especially around `FindByIDsWithDetails`.

## Phase 1 — Canonical item projections and scanners

### 1.1 Introduce repository-level projection definitions

Create one internal repository module, e.g. `internal/repository/item_projection.go`, with named projections:

- `ItemProjectionBase` — raw item columns, no joins.
- `ItemProjectionDetails` — standard API item details.
- `ItemProjectionHierarchy` — hierarchy/list-friendly subset.
- `ItemProjectionList` — current list/search shape.

Each projection should provide:

- `Select(alias string) string`
- `Joins(alias string) string`
- `Scan(scanner) (*models.Item, error)` or row scanner functions

### 1.2 Move base item scanner into repository

Replace service-local helpers in `internal/services/item_update_service.go`:

- `nullInt64ToIntPtr`
- `parseItemCustomFieldValues`
- `scanItemBaseFields`

with exported or package-internal repository helpers, e.g.:

- `repository.ScanItemBase(scanner)`
- `repository.ItemBaseSelect(alias)`

### 1.3 Refactor highest-use readers first

Migrate in this order:

1. `ItemRepository.FindByID`
2. `ItemRepository.FindByIDWithWorkspaceStatus`
3. `ItemRepository.FindAllWithDetails`
4. `ItemUpdateService.loadItemInTx`
5. `ItemUpdateService.recordItemCreationHistory`
6. `ItemUpdateService.loadItemWithJoins`

Acceptance criteria:

- No service package scans raw base item columns itself.
- `FindByIDWithDetails` remains the canonical single-item response path.
- Existing tests pass and API response snapshots/fixtures are unchanged.

## Phase 2 — Batch item hydration and remove N+1 readers

### 2.1 Replace `FindByIDsWithDetails` loop

Current `FindByIDsWithDetails` loops over `FindByIDWithDetails`. Replace with a single `WHERE i.id IN (...)` query using the canonical details projection.

### 2.2 Add `FindByIDsWithProjection`

Add an internal repository helper for callers that need many hydrated items:

```go
FindByIDsWithProjection(ids []int, projection ItemProjection) ([]*models.Item, error)
```

or a narrower:

```go
FindByIDsWithDetails(ids []int) ([]*models.Item, error)
FindByIDsForHierarchy(ids []int) ([]*models.Item, error)
```

Acceptance criteria:

- No new N+1 item detail hydration is introduced.
- `item_links`, graph, AI, and homepage code use batch hydration where applicable.

## Phase 3 — Collapse hierarchy implementation

### 3.1 Choose one hierarchy owner

Make `services.HierarchyService` the business/API owner and `repository.ItemRepository` the SQL owner.

Recommended split:

- Repository owns recursive CTE SQL and item projection scanning.
- Service owns depth caps, cycle policy, validation semantics, and public methods.

### 3.2 Replace duplicate service SQL

Refactor `internal/services/hierarchy.go` methods to call repository methods:

- `GetAncestors`
- `GetDescendants`
- `GetChildren`
- `GetRoot`
- `CountDescendants`

Repository methods should support:

- max depth
- root-first vs direct-parent-first ordering where needed
- direct children vs recursive descendants

### 3.3 Unify depth behavior

Use one `maxHierarchyDepth` policy and ensure every recursive CTE has an explicit cap.

Acceptance criteria:

- No raw hierarchy item hydration SQL remains in `services/hierarchy.go` except simple cycle parent lookups if necessary.
- Existing handler behavior and ordering are preserved or intentionally documented.

## Phase 4 — Centralize update response loading

### 4.1 Remove `ItemUpdateService.loadItemWithJoins`

After update commit, call `repository.NewItemRepository(db).FindByIDWithDetails(itemID)` or `ItemCRUDService.GetByID(itemID)`.

### 4.2 Keep tx-aware base load repository-owned

`loadItemInTx` can become:

```go
repo.FindByIDForUpdate(tx, itemID)
```

It should append `FOR UPDATE` on Postgres and use the canonical base scanner.

Acceptance criteria:

- Update service contains validation/history/business logic, not item hydration SQL.
- Updated item response includes the same or richer fields than before.

## Phase 5 — Normalize create/copy/delete workflows

### 5.1 Define service-level workflow ownership

`ItemCRUDService` or a dedicated `ItemLifecycleService` should own:

- create
- copy
- delete single
- delete cascade
- milestone carry-over
- history creation
- frac-index retry
- cache invalidation inputs

Handlers should only perform:

- auth
- request parsing
- permission checks
- audit/event emission
- response formatting

### 5.2 Consolidate copy

Move handler-level copy retry/transaction code from `internal/handlers/items.go` into service.

Keep one copy implementation that supports:

- `IncludeChildren` when needed later
- parent override
- milestone carry-over
- history recording
- frac-index retry

### 5.3 Consolidate delete

Make both handler `Delete` and `DeleteCascade` call service methods:

- `DeleteSingle(itemID)` for only the item
- `DeleteCascade(itemID)` for item + descendants

Both should share cleanup policy for watches, history, links, worklogs, and related side tables.

Acceptance criteria:

- No handler manually deletes from `items` or `item_links`.
- Single source of truth for cleanup coverage.
- Existing audit/event payloads remain unchanged.

## Phase 6 — Centralize item-link traversal/hydration

### 6.1 Promote link-list SQL behind `ItemLinkService`

Use `ItemLinkService.getLinksWhere`/public wrappers for every list endpoint that displays `item_links`.

Add wrappers if needed:

- `ListLinksForItem(itemID, direction)`
- `ListLinksForAsset(assetID, direction)`
- `ListLinkedAssetsForItem(itemID)`
- `ListGraphNeighbors(entityType, entityID)`

### 6.2 Remove mirrored outgoing/incoming handler SQL

Refactor:

- `internal/handlers/asset_link_handlers.go` outgoing/incoming asset queries
- `internal/handlers/item_links.go` direct queries
- graph BFS neighbor fetches

Acceptance criteria:

- Handlers do not query `item_links` directly except truly specialized migrations/imports.
- Direction label logic lives in one service/repository function.

## Phase 7 — Clean up micro-query scatter

### 7.1 Expand item repository accessors

Add safe repository methods for common single-column reads:

- `GetAllowedColumnValue(itemID, column)`
- `GetCustomFieldValuesMap(itemID)`
- `GetStatusID(itemID)`
- `GetWorkspaceOwnership(itemID)` already exists; use it consistently.

### 7.2 Replace direct service/handler item micro-queries

Targets:

- `internal/services/item_validation.go`
- `internal/services/action_service.go`
- `internal/services/approval_service.go`
- any handler with `SELECT ... FROM items WHERE id = ?`

Acceptance criteria:

- Dynamic item-column reads are allowlisted and repository-owned.
- Custom-field JSON parsing behavior is consistent everywhere.

## Phase 8 — Decide what not to refactor

Leave these alone unless they grow more duplication:

- analytics aggregation SQL
- stats/count queries
- migration-analysis aggregation queries
- public board specialized DTOs
- importer/exporter projections

They are not generic item access and forcing them through a common item hydrator may hurt performance/readability.

## Execution progress

Completed in this pass:

- Centralized base item loading in `ItemRepository` via shared base scanner and `FindByIDForUpdate`.
- Updated `ItemUpdateService` to use repository-owned item loading for tx reads, creation-history reads, and post-update response hydration.
- Removed service-local base item scanner/null helpers that duplicated repository scan helpers.
- Added repository-owned dynamic allowed-column reads and moved action-service item-field reads through it.
- Added `ItemCRUDService.DeleteSingle` and changed the non-cascade delete handler to delegate lifecycle deletion to the service.
- Moved handler-level copy transaction / frac-index retry / milestone carry-over / history recording into `ItemCRUDService.Copy`.
- Added depth caps to repository hierarchy recursive CTEs and moved key `HierarchyService` reads to repository methods where safe.
- Added lightweight repository hierarchy ancestor projection to preserve minimal hierarchy-test compatibility without service-owned SQL.
- Replaced one scattered item workspace lookup in item validation with `ItemRepository.GetWorkspaceID`.

Validated with:

```bash
go test ./internal/repository ./internal/services ./internal/handlers
```

## Suggested implementation order

1. Phase 0 tests/baselines.
2. Phase 1 base/details scanner consolidation.
3. Phase 4 update-service hydration cleanup.
4. Phase 2 batch item hydration.
5. Phase 3 hierarchy consolidation.
6. Phase 5 lifecycle create/copy/delete cleanup.
7. Phase 6 link traversal cleanup.
8. Phase 7 micro-query cleanup.

## Risks and mitigations

- **Response drift:** use characterization tests and compare JSON outputs for key endpoints.
- **N+1 regressions:** add batch hydration before refactoring graph/link/list paths.
- **Permission leaks:** keep permission filtering in services/handlers; repository should not silently broaden access.
- **Over-abstracted SQL:** keep projections small and explicit; do not build a full ORM.
- **Postgres/SQLite differences:** keep placeholder conversion and JSON extraction tests for both drivers where applicable.

## Done criteria

- Service and handler layers no longer mint common item-detail SQL.
- Hierarchy SQL has one owner.
- Copy/delete lifecycle logic has one owner.
- `item_links` traversal/display SQL has one owner.
- `jscpd` item-access clones are gone or limited to intentional specialized queries.
- All Go tests pass; changed Go files are formatted.
