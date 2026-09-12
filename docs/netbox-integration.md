# NetBox integration

This MVP attaches selected NetBox devices and virtual machines to Windshift
items. NetBox remains the source of truth. Windshift performs only explicit GET
requests and stores a small local snapshot when a user links or refreshes an
object. It does not import an inventory, poll automatically, write to NetBox,
or delete NetBox objects.

## Setup

In **Administration → Integrations → NetBox**, create a connection with:

- A name and unique slug.
- The HTTPS base URL of the NetBox installation, without `/api`. A deployment
  prefix is supported, for example `https://netbox.example.org/netbox`.
- An API token and its authentication scheme: `bearer` for a v2 token in
  `nbt_<key>.<token>` format, or `token` for a legacy hexadecimal token.
- Explicitly selected workspaces, or an explicit all-workspaces choice.

Use a dedicated NetBox account/token restricted to the objects and read
permissions needed by those workspaces. A connection test reads one bounded
page from both the devices and virtual-machines endpoints. It does not prove
that every expected object is visible: NetBox may filter results by its own
object permissions. A successful test can include a warning if NetBox ignores
the requested field projection; Windshift still discards unselected fields.
Enable the connection before testing it; disabled connections cannot make
remote requests through the test or item endpoints.

Tokens are encrypted through Windshift's existing managed action-credential
service and are never returned by these endpoints. On edit, an omitted or
empty token preserves the current secret. The base URL, authentication scheme
and slug cannot be changed after creation. Create a new connection to change
them. Updates require the current connection `revision`; a stale edit returns
409 instead of overwriting another administrator's update.

See the [official NetBox REST API documentation](https://netbox.readthedocs.io/en/stable/integrations/rest-api/)
for token provisioning, object permissions and pagination. Compatibility with
an actual deployment must be checked using its version and the connection
test; synthetic contract tests are not a live-instance compatibility claim.

## Item workflow and permissions

The item sidebar shows saved NetBox links. Users with ordinary workspace
`item.view` permission may read visible snapshots. `item.edit` is required to
search, link, refresh or unlink. Approval-only item visibility does not grant
access to NetBox data. Connection administration requires `system.admin`.

Search selects a connection and either devices or virtual machines. Object
identity includes the connection, type and numeric ID, so a device and a VM
with the same number remain distinct. Re-linking the same object to the same
item returns its existing link (HTTP 200, without another creation audit).
A new link returns HTTP 201. Refresh an existing link explicitly to update its
snapshot.

Saved fields are limited to name, status, site, role, primary IPv4/IPv6, typed
identity and a link constructed from the configured base URL. No custom
fields, configuration, comments, contact or tenant records are copied.
Snapshots do not enter item history, notifications or audit payloads. Audit
events contain identifiers and operation names, not search queries, tokens,
snapshot text or remote response bodies.

Every saved-link read checks the item's current workspace and the connection's
current enabled state and workspace scope. Disabling a connection or removing
a workspace hides its previously saved snapshots on subsequent reads. It does
not erase them: restoring access makes them visible again. This cannot revoke
copies already seen by a user. Hidden links also cannot be refreshed or
unlinked through the item endpoint. Restore visibility or delete the
connection if its local links must be removed. Deleting an item or connection
cascades its local links only; it never deletes anything in NetBox.

A failed refresh keeps the previous snapshot and its timestamp. In particular,
NetBox 404 means not found **or not accessible**, not confirmed deletion.
Concurrent refresh, unlink, item move and connection changes are checked
before persistence; stale results are rejected instead of restoring a removed
link or overwriting a newer snapshot.

## Network and storage limits

- HTTPS only; redirects are not followed and environment HTTP proxies are not
  used. Requests are restricted to the configured origin and the device/VM
  collection or numeric-detail paths.
- Windshift's existing `ALLOW_LOCAL_CONNECTIONS` policy applies. Its default
  permits local/private destinations, including addresses that can expose
  internal services. Set it to `false` when such destinations must be blocked.
  This integration does not promise private-network isolation under the default.
- Certificate verification follows the existing global `TLS_SKIP_VERIFY`
  setting. Keep verification enabled and configure trusted certificates;
  enabling that global override weakens server authentication.
- Each request has a 30-second timeout and a 1 MiB response limit. Search reads
  one page, never follows NetBox's `next` URL, and uses explicit offsets.
- Search queries contain 2–200 characters; the page size defaults to 20 and is
  capped at 50. Offset is limited to 0–1000. Narrow the query beyond that bound.
- Each item has at most 100 NetBox links. A snapshot is limited to 16 KiB.
  IDs above JavaScript's exact integer range are rejected by the remote client.

## Internal API and lifecycle

These are session-authenticated internal `/api` endpoints, not public API-v2
operations or an API-v2 client contract:

| Resource | Operations |
| --- | --- |
| `/api/admin/netbox-connections` | GET, POST |
| `/api/admin/netbox-connections/{id}` | GET, PUT, DELETE |
| `/api/admin/netbox-connections/{id}/test` | POST |
| `/api/workspaces/{workspaceId}/netbox-connections` | GET |
| `/api/workspaces/{workspaceId}/netbox-connections/{id}/search` | GET |
| `/api/items/{id}/netbox-links` | GET, POST |
| `/api/items/{id}/netbox-links/{linkId}` | DELETE |
| `/api/items/{id}/netbox-links/{linkId}/refresh` | POST |

Both SQLite and PostgreSQL receive new connection/link tables through migration
`20260912_netbox_integration`; existing migration checksums are unchanged.
The dedicated link table has item/connection foreign keys and is not mirrored
into generic integration links.

Connection creation uses the existing compensated managed-credential pattern:
if provider creation fails, it attempts to remove the just-created credential.
It is not crash-atomic across those two transactions. A process crash in that
window, or failure of the compensating cleanup, can leave an orphaned managed
credential. Do not treat creation as a cross-resource atomic guarantee.
