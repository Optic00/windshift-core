# Jira Service Management import identity mapping

Jira Service Management imports keep portal-only customers separate from
Windshift internal users. Jira identities with `accountType=customer` or a
`qm:` account ID become `portal_customers`; Atlassian and application accounts
continue through the internal-user import path.

## Attribution

- Requests use `items.creator_portal_customer_id` when their Jira reporter or
  creator is a portal customer.
- Customer comments use `comments.portal_customer_id`. JSM public/internal
  visibility is retained through `comments.is_private` and mapping metadata.
- The imported portal and request type are stored in `items.channel_id` and
  `items.request_type_id`.
- Jira reporter and creator identities are also retained in
  `items.custom_field_values` as `_jira_reporter` and `_jira_creator`. Each
  object carries the available stable account ID, account type, display name,
  and email. This is the lossless fallback for `reporter_id` and `creator_id`,
  which accept only internal users.
- Attachment uploaders map to `attachments.uploaded_by` when they are internal
  users. When the uploader is a portal customer, `uploaded_by` remains null and
  the complete source identity is retained in the attachment's
  `jira_import_id_mappings.metadata_json` `author` object.

## Organizations and access

Organization membership is enumerated even when the operator chooses not to
create Windshift customer organizations, so organization members are still
imported as portal customers. Enabling organization import additionally creates
or reuses `customer_organisations` and assigns customers to them.

Every imported portal customer receives access through
`portal_customer_channels` and the Portal Customer contact role.

## Retry behavior

Jira account IDs are the stable source keys in import mappings. Portal customers
reuse an existing case-insensitive email match; customers without a visible
email receive a deterministic `<account-id>@jira-customer.invalid` address.
Portal slugs and request types are reused for the same destination workspace.

Retries preserve a mapping's original `was_created` ownership bit. A later
reuse must not change a job-created record into a pre-existing record, because
that would prevent the import cleanup operation from removing resources the job
originally created.
