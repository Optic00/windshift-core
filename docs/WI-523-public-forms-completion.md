# WI-523 Public forms completion

Date: 2026-07-27

## Product model

A form channel owns the public URL, branding, allowed target workspaces, and
channel-level success defaults. It can publish one or more forms. Each form owns
its fields, authentication requirement, submit label, success override,
redirect, target workspace, and created item type.

The admin UI uses “form” for the public object. “Request type” remains an
internal implementation detail and a compatibility API name.

## Shared rendering

Public forms and portal request forms now use the same field renderer and form
model for:

- title and description;
- custom and virtual fields;
- initial-value normalization;
- required-field validation;
- step derivation and navigation;
- saved-step clamping after schema changes.

The surrounding hosted page and portal modal remain separate so each surface
can keep its own shell, submission transport, authentication, and styling.

## Public URLs and preview

- `/forms/:slug` is the channel landing URL.
- `/forms/:slug/:formId` is the stable URL for an individual form.
- Admin preview renders the current unsaved field/config state and disables the
  final submit action, so preview cannot create a work item.
- The builder exposes preview, copy-link, and open-form actions. A channel
  without a slug routes the admin to channel settings instead.
- Field, per-form configuration, and routing edits all participate in the
  visible unsaved state and the main Save action.

## Authentication return flow

Authentication-required forms are gated before data entry when the browser has
no Windshift session. Hosted forms link to:

```text
/login?return_to=/forms/:slug/:formId
```

Only local absolute paths are accepted as return destinations. Successful
interactive login navigates back to the exact hosted form. Embedded
authentication-required forms show a hosted-form fallback because third-party
iframes cannot reliably carry the Windshift session.

The existing submit-time 403 handling remains as a fail-closed fallback if the
session expires or the form configuration changes while it is open.

## Draft policy

Public form drafts are browser-local for both anonymous and authenticated
visitors.

- Drafts are scoped by origin, channel slug, and form ID.
- Title, description, custom/virtual values, and current step are stored.
- Empty forms are not persisted.
- A restored draft is announced and can be discarded with “Start fresh.”
- Successful submission deletes the draft.
- Attachments are deliberately not persisted because browsers cannot restore
  local `File` objects safely.
- Embed mode uses the same origin-scoped storage and remains anonymous-safe.

Server-side drafts remain a portal feature. Public drafts do not create server
records, require an account, or add a new public write endpoint.

## Verification matrix

Automated coverage exercises:

- shared default/custom/virtual value initialization;
- exact required-field failures;
- multi-step derivation and stale-step clamping;
- browser draft save, restore, and deletion;
- pre-submit and submit-time authentication states;
- login return to an individual form;
- stable multi-form URLs;
- admin preview without submission;
- routing changes in the builder's save state;
- attachment retry and exactly-once creation;
- polished success state and submit-another action.
