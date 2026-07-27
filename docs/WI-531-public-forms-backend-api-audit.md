# WI-531 Public Forms backend and API audit

Date: 2026-07-27

## Outcome

The existing public Forms API is sufficient for the 0.8.4 Forms UI stories.
No new backend endpoint or submission response field is required.

Two gaps did require changes:

1. Multi-form channels need a stable URL for one selected form. The public SPA
   now supports `/forms/:slug/:formId` and continues to load the selected form
   through the existing channel-scoped detail endpoint.
2. A legacy request type with no `workspace_id` must not route to the first
   configured workspace when a form channel serves several workspaces.
   Submission now rejects that ambiguous configuration. The legacy fallback is
   retained only when the channel serves exactly one workspace.

## Public entry and form selection

### Decision

Use a path segment:

```text
/forms/:slug/:formId
```

Keep `/forms/:slug` as the channel landing page. A one-form channel still opens
its only form automatically without rewriting the channel URL. Selecting a form
from a multi-form landing page updates the URL to the direct form path, and
Back to forms returns to the channel URL.

The form ID is intentionally scoped by the channel slug. The existing
`GET /api/forms/{slug}/forms/{id}/detail` handler verifies that the request type
belongs to the selected channel and returns 404 otherwise. The new browser route
therefore needs no new API endpoint and does not weaken form/channel isolation.

### Why a path instead of a query parameter

- It gives WI-526 one canonical hosted URL to preview, copy, and open.
- It is compatible with refreshes, browser history, and context-path
  deployments.
- It preserves `?embed=true` for the existing iframe integration.
- It avoids introducing two equivalent share URL formats.

## Target workspace routing

### Current contract

Each form request type owns its target `workspace_id`. The form creation and
editing UI require an explicit target workspace and constrain it to workspaces
served by the channel. Submission uses that request-type workspace and verifies
that it is still present in `form_workspace_ids`.

### Legacy rows

Older request types may have a NULL `workspace_id`.

- If the channel serves one workspace, submission uses that sole workspace.
- If the channel serves multiple workspaces, submission returns 400 with an
  explicit request-type configuration error.
- A pinned workspace that is not served by the channel also returns 400.

Configuration order is not a routing policy. In particular, reordering
`form_workspace_ids` cannot change where a form creates items.

## Submission response

Successful `POST /api/forms/{slug}/submit` responses contain:

```json
{
  "success": true,
  "item_id": 123,
  "success_message": "Submission received successfully",
  "attachment_count": 0,
  "redirect_url": "https://example.test/thank-you"
}
```

`redirect_url` is optional. Per-form success copy and redirect take precedence
over channel defaults.

This is sufficient for the public success actions in WI-525:

- render configured success copy;
- return to the form/channel and submit another response;
- follow a configured external redirect.

The response deliberately does not promise a view-item link. Anonymous
submitters may not have permission to read the created item, while the SPA item
route also needs workspace context. A future authenticated-only “view request”
action should define its authorization policy before adding a public response
URL.

## Redirect validation

Redirect handling is sufficient and uses defense in depth:

- channel configuration rejects non-HTTP(S) `form_redirect_url` values;
- per-form configuration rejects non-HTTP(S) `redirect_url` values;
- public channel/bootstrap responses drop unsafe stored channel redirects;
- submission responses revalidate both per-form and channel fallback values
  before returning them to the browser.

Protocol-relative and executable schemes such as `javascript:`, `data:`, and
`vbscript:` are not accepted.

## Authentication-required forms

The backend contract is sufficient for WI-528:

- public form summaries expose `config.require_auth`, allowing the hosted page
  to show an authentication state before submission;
- form submission uses optional internal/portal authentication and fails closed
  with 403 when the selected form requires identity;
- malformed per-form config fails closed instead of silently disabling
  authentication;
- the hosted renderer already turns a 403 into an actionable sign-in message;
- integration settings hide iframe/widget modes when any form requires
  authentication, because cross-site embeds cannot reliably carry a Windshift
  identity.

WI-528 still owns the browser workflow: preserving the direct form return URL,
performing sign-in, and resuming or retrying the form. That work does not require
a new Forms API.

## Existing API surface retained

```text
GET  /api/forms/{slug}/bootstrap
GET  /api/forms/{slug}
GET  /api/forms/{slug}/forms
GET  /api/forms/{slug}/forms/{id}/detail
GET  /api/forms/{slug}/forms/{id}/fields
GET  /api/forms/{slug}/custom-fields
POST /api/forms/{slug}/submit
```

The granular GET endpoints remain compatibility surfaces. The hosted page uses
the aggregate bootstrap and complete form-detail endpoints to avoid request
waterfalls.

## Follow-up boundaries

- WI-525: polished hosted shell and success-state presentation.
- WI-526: per-form preview/share UI should generate
  `/forms/:slug/:formId`.
- WI-527: renderer consolidation must preserve the API contracts above.
- WI-528: login/return/resume browser flow.
- WI-529: draft persistence policy and implementation.
