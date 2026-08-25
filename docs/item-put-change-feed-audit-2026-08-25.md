# Item PUT and change-feed audit

Date: 2026-08-25

## Scope

Reviewed cookie API and bearer v1 `PUT` routes whose primary resource is a work
item or an item-owned subresource. Also reviewed label assignment and generic
link create/delete paths because GitHub issue #219 names them explicitly.

The relevant contracts are:

- `items.updated_at` changes when the serialized shared item payload changes.
- `items.last_active_at` changes for card activity, but not for manual ordering
  or catalog-wide metadata maintenance.
- Every committed item-row update emits an `item_change_log` row through the
  SQLite/PostgreSQL schema triggers used by `/items/changes`.

## Results

| Operation | Shared item payload | `updated_at` | `last_active_at` | Change feed | Result |
| --- | --- | --- | --- | --- | --- |
| `PUT /items/{id}` | Changes | Yes | Yes | Yes | Existing shared update service is correct. |
| `PUT /items/{id}/labels` | Changes | Yes | Yes | Yes | Fixed in the shared label repository; cookie, v1, CLI, and AI paths inherit it. |
| `POST/DELETE /items/{id}/labels...` | Changes | Yes | Yes | Yes | Fixed with the same transactional assignment contract. A missing-label removal remains a no-op. |
| `PUT /labels/{id}` | Changes labels nested in assigned items | Yes | No | Yes | Fixed by invalidating assigned items in the label transaction without bubbling every card. |
| `DELETE /labels/{id}` | Removes labels nested in assigned items | Yes | No | Yes | Fixed before the assignment cascade in the same transaction. |
| `POST/DELETE /links...` | Changes item detail links | Yes, for every item endpoint | Yes, for every item endpoint | Yes | Fixed transactionally. Single-value replacement also invalidates the removed target. |
| `PUT /items/{id}/frac-index` | Changes manual order only | No | No | Yes | Intentional. The item-row trigger propagates ordering while timestamps stay stable. |
| `PUT /comments/{id}` | No; comment is fetched separately | No | Yes | Yes | Intentional. Comment activity touches the item and has its own resource timestamp. |
| `PUT /items/{id}/recurrence` | No; recurrence is fetched separately | No | No | No | No parent invalidation required by the item payload contract. |
| `PUT /diagrams/{id}` | No; diagrams are fetched separately | No | No | No | No parent invalidation required; diagram history and timestamps are recorded on the diagram resource. |
| `PUT /items/{id}/personal-labels` | Viewer-specific data | No | No | No | Intentional. Per-user metadata must not mutate shared item activity for every viewer. |

## Transaction boundaries

Label assignments now update the junction table and item timestamps in one
transaction. Generic link creation, deletion, and single-value replacement do
the same for every affected item endpoint. A timestamp/change-feed failure
therefore rolls back the relationship mutation instead of returning a partial
success.

Jira import continues to attach labels without touching item activity so the
imported source timestamps remain intact. GitHub issue sync already replaces
labels inside the transaction that updates the item itself.
