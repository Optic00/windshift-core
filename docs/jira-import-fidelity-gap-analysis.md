# Jira Import Fidelity Gap Analysis

Verification date: 2026-07-30

Primary work items: WI-225, WI-226, WI-229, WI-232, WI-233, WI-234,
WI-235, and WI-236.

## Conclusion

The original audit found that the importer had closed several issue-data gaps,
but its configuration import was not yet a faithful Jira migration. The risks
were structural rather than isolated missing type aliases:

1. **Closed by the WI-226 0.8.4 fidelity pass:** custom fields unknown to the
   hard-coded allowlist were removed before the wizard could inspect or
   preserve them;
2. **Closed by the WI-226 0.8.4 fidelity pass:** select
   and multiselect definitions were created without Jira options while
   imported item values were stored as display strings, although Windshift
   choice fields use numeric option IDs. Both observed and configured-but-unused
   Jira options now receive stable Windshift option IDs;
3. **Closed with an explicit model boundary:** custom-field contexts, defaults,
   project/issue-type applicability, disabled options, and cascading parent
   relationships are preserved in import metadata. Windshift fields remain
   global, so source applicability is reported rather than falsely enforced;
4. **Closed by WI-229:** the workflow importer did not read a Jira workflow
   graph and created every non-self status pair as a transition;
5. **Closed conservatively by WI-229:** Jira workflow conditions, validators,
   approvals, post-functions, transition screens, and status properties were
   neither imported nor included in the readiness score;
6. **Closed for supported Cloud capability tiers by WI-229:** Jira screen
   schemes and field configurations were not fetched. The only Jira-specific
   screen code cloned existing Windshift screens to add imported Assets fields;
7. **Closed for issue creation order in 0.8.4:** Jira Software Rank was treated
   as a skipped custom field. The importer now asks Jira for issues in Rank
   order and generates Windshift fractional indexes in that sequence;
8. **Closed for new imports in 0.8.4:** the original Jira issue key existed only
   in underscore metadata and job-local mappings. Imports now create or reuse a
   searchable `Jira Key` text field, populate it on every issue, and add it to
   imported workspace screens.

This document originally established that WI-229 was substantively
unimplemented. The 0.8.4 implementation described below closes the workflow
graph and company-managed screen slices while retaining this audit as the
baseline for the remaining rule-conversion model boundaries.

## 0.8.4 WI-229 implementation

The 0.8.4 implementation adds optional configuration capabilities rather than
widening the base Jira client contract:

- Jira Cloud reads the effective workflow separately for every project and
  issue-type pair with `POST /rest/api/3/workflows`. Jira workflow source
  identity, description, statuses, initial/directed/global topology, and
  per-issue-type assignments are preserved.
- Global transitions expand to explicit Windshift directed edges. Same-status
  loop transitions are reported and omitted because Windshift currently treats
  a same-status update as a no-op before transition rules execute.
- Jira Cloud's current workflow-read response does not expose configured
  condition trees. The importer therefore creates the graph but attaches a
  generated validator lock to every non-initial edge. An operator must review
  the Jira conditions and validators and remove or replace that lock before
  the edge becomes executable. Exposed validators, actions, and triggers are
  counted in import metadata and the final job report.
- When an authoritative graph is unavailable (including Data Center's standard
  REST surface or insufficient Cloud permissions), the importer creates one
  deterministic initial transition and no guessed directed edges. The former
  all-to-all fallback has been removed.
- Jira Cloud company-managed projects resolve project issue-type screen scheme,
  issue-type mappings, screen schemes, operation-specific/default screen IDs,
  tabs, and ordered tab fields. Create/edit/view defaults and per-item-type
  overrides are assigned to the imported configuration set.
- Multiple Jira tabs flatten in tab/field order. Supported Jira system fields
  map to Windshift system fields; selected imported custom fields map to their
  Windshift custom-field IDs. Unmapped screen fields, transition screens,
  renderer settings, and Jira hidden/required field-configuration rules remain
  explicit lossy/unsupported findings.
- Readiness now probes workflow and screen capabilities and reports graph
  availability, review locks, exposed guards, post-functions/actions, triggers,
  loop transitions, flattened tabs, omitted screen fields, and field-layout
  limitations.
- Completed import jobs persist workflow and screen source identities plus
  fidelity metadata in `result_json`; `GetJobStatus` returns that report.
- Imported screens participate in provenance-aware cleanup. Creation-only
  workflow transitions no longer leak into the set of moves offered for an
  existing item.

This implementation did not itself close the custom-field slices in WI-225.
The subsequent WI-226 0.8.4 implementation below closes unknown field
visibility, populated choice option/value normalization, and raw preservation.
The subsequent WI-226 pass also imports contexts, defaults, applicability, and
configured-but-unused options as described below.

## 0.8.4 WI-226 custom-field fidelity implementation

The reopened WI-226 implementation corrects the difference between extracting
a value and importing a valid, editable Windshift custom field:

- `SuggestFieldMappings` now returns every Jira custom-field definition.
  Unknown plugin keys use `schema.type` and `schema.items` to select safe native
  number, text, date, user, multi-user, select, and multiselect mappings.
- Unknown complex arrays, objects, app-owned values, and definitions without a
  proven schema become explicit textarea mappings with `preserve_raw=true`.
  Their complete JSON value is retained rather than flattened to a guessed
  display string or hidden from the wizard.
- Known mappings also refine from schema shape. In particular, Jira Service
  Management Request participants with `items=user` now maps to Windshift
  `multi_user`, not a generic multiselect.
- Before creating fields, execution reads every issue in the selected import
  scope for selected choice fields, unions populated display labels
  case-insensitively, and creates stable Windshift numeric option IDs. Select
  and multiselect item values are written using those IDs, making the imported
  fields valid and editable under Windshift's choice-field contract.
- Re-import merges newly observed labels without changing existing option IDs.
  Two Jira fields with the same display name and target type retain distinct
  source-specific Windshift definitions instead of being conflated.
- The wizard import plan carries the raw-preservation disposition. Readiness
  marks opaque JSON preservation as lossy rather than clean, and completed job
  results include custom-field source identity, target identity, option counts,
  and preservation metadata.
- Jira Cloud configuration reads retain every context, its project and
  issue-type applicability, defaults, disabled options, and cascading parent
  relationships. Fields that reject the option or default endpoint retain the
  rest of their configuration and report only that unavailable slice.
- Jira contexts cannot be enforced by Windshift's global custom-field model.
  They are therefore retained on the field mapping and used to bind fields only
  to applicable imported project screens; the job report states the boundary.
- Jira datetime values retain RFC3339 text, while readiness and the final
  report identify Windshift's date-only editing model as lossy.

The opt-in read-only live contract check was run against both projects in the
saved Storymap Premium configuration:

- `KAN`: 96 project fields, including 47 plugin types outside the explicit
  mapping table; all remained visible, 19 received raw-preservation mappings,
  and populated labels were discovered for 9 of 32 choice fields.
- `SP`: 85 project fields, including 47 plugin types outside the explicit
  mapping table; all remained visible and 19 received raw-preservation
  mappings. Its four issues did not populate the 22 applicable choice fields.
  The live configuration read nevertheless imported 22 contexts, 110
  configured options, and 18 defaults. Two watched issues exposed two
  importable watcher identities.

## 0.8.4 follow-up hardening

The final live/browser pass closed five concrete gaps without widening the
intentional Jira/Windshift model boundaries:

- Jira Cloud initial workflow transitions may include a link with an empty
  source reference. Initial transitions are source-less by definition, so the
  client now ignores their source links while continuing to reject unknown
  sources on directed transitions. This keeps Jira Service Management `Create`
  transitions authoritative instead of falling the entire project back to
  status membership.
- Team-managed projects are importable from the wizard. Their issue data,
  hierarchy, observed custom fields, and other supported entities use the same
  import path; the mapping UI and job report explicitly state that Jira does
  not expose company-managed workflow/screen schemes for those projects, so
  Windshift creates conservative configuration.
- Every durable import configuration now carries a deterministic SHA-256 plan
  fingerprint over normalized scope and mappings. Connection identity,
  force-reimport control flow, volatile issue counts, acknowledgement state,
  and Xray secrets do not affect the fingerprint. Conflict responses identify
  configuration drift, and forced re-import results retain the current
  fingerprint plus prior import evidence.
- Cloud and Data Center read requests retry Jira `429` and `503` responses up
  to three times. `Retry-After` is honored when present; otherwise a bounded
  exponential delay is used. Request bodies are recreated for Jira's allowed
  read-only POST endpoints.
- Readiness describes out-of-scope Jira issue references as durable integration
  links, matching execution rather than the obsolete dropped-link behavior.

## 0.8.4 Jira Rank and durable issue-key implementation

Jira Software's Rank is a LexoRank value whose comparison contract belongs to
Jira. Windshift uses its own fractional-index format, so the importer does not
copy or reinterpret the source string:

- issue keys are listed with
  `ORDER BY Rank ASC, created ASC, key ASC`;
- Jira Core-only or restricted deployments that reject Rank retry with the
  deterministic former order, `created ASC, key ASC`, and log the downgrade;
- Jira bulk fetch is treated as set-oriented. Every returned batch is restored
  to the ordered key sequence before issue creation;
- the centralized Windshift item creation path appends a fresh `frac_index` for
  each issue in that sequence. Jira relative order is therefore retained while
  all stored indexes remain native Windshift fractional indexes;
- readiness classifies Rank as a clean first-class ordering conversion rather
  than a blocked custom field.

Every import also creates or reuses a global text custom field named `Jira Key`
(or a deterministic imported fallback name if that name has an incompatible
type). The field is populated with values such as `SP-42`, retained alongside
the legacy `_jira_issue_key` metadata, included on imported workspace screens,
and queryable through Windshift QL, for example:

```text
`cf_Jira Key` = "SP-42"
```

The regression contract imports a deliberately shuffled bulk response, asserts
that `ORDER BY frac_index` returns the Jira Rank sequence, and retrieves one
item through the declared Jira Key custom field. The read-only Storymap Premium
check confirms that project `SP` accepts the Rank-ordered JQL without requiring
the fallback.

## 0.8.4 durable re-import, identity, and content

Forced re-import is now an update operation keyed by the stable Jira issue ID:

- an existing imported item keeps its Windshift ID and workspace item number;
  mutable fields are reconciled and its native fractional index is regenerated
  in the new Jira Rank sequence;
- comment and worklog rows update by stable Jira IDs; attachment mappings and
  immutable files are reused; internal-link and cleanup ownership moves to the
  newest job so deleting an older import cannot remove current data;
- links to issues outside the selected scope become item-facing Jira
  integration links with browse URLs and relation metadata;
- the conflict response remains the safety gate. The wizard offers an explicit
  **Re-import and update** action rather than overloading a normal import.

User collection covers creator, reporter, assignee, comment/update authors,
attachment and worklog authors, user fields, ADF mentions, voters, and
watchers. Watcher identities become native `item_watches`. Hidden-email
accounts retain deterministic synthetic identities. Deleted and anonymous
people use the shared fallback user where a foreign key is required while
their display identity remains in item/entity mapping metadata.

The WI-232 content contract is verified end to end: paged comments and
worklogs are completed; JSM comment visibility fails closed; restricted Jira
visibility becomes a private Windshift comment with its original scope
retained; supported ADF blocks convert to Markdown; and description/comment
media nodes link to the imported attachment.

## 0.8.4 Assets and collaboration fidelity

Jira Assets schemas, types, attributes, and objects continue to import as
Windshift asset sets, types, fields, and assets. The final fidelity pass adds:

- all Jira object types—including abstract nodes—as a native asset-category
  hierarchy, with concrete objects assigned to the matching category;
- Jira object-reference attributes as typed asset fields, resolved after all
  objects are created so forward references work;
- Jira user attributes as typed user fields using the same deterministic Jira
  identity mapping;
- Jira object statuses as Windshift asset statuses;
- raw display preservation and an explicit job finding for references that
  cannot be resolved.

For the remaining WI-236 collaboration features:

- votes and available voter identities are retained as item metadata because
  Windshift has no voting model;
- issue-security levels are retained as metadata and deliberately do not
  broaden or rewrite workspace access;
- project roles and permission schemes are explicitly unsupported because
  Windshift role/grant semantics are not equivalent;
- JSM request types, request participants, portals, organizations, and portal
  customers use their native Windshift models;
- SLA values are preserved, while calendars, goals, pause conditions, and
  running timers are reported as unsupported runtime semantics.

Every completed job returns `fidelity_findings` with a code, severity,
disposition, explanation, and count where applicable. Unsupported and
permission-unavailable behavior is therefore visible without granting broader
access or inventing source semantics.

## Original verified baseline behavior (historical)

### Custom fields

`jira.SuggestFieldMappings` calls `IsKnownFieldType` and drops every field whose
`schema.custom` key is not present in `jiraFieldTypeMap`
(`internal/jira/field_mapper.go:127-135,214-224`). This makes the schema-based
fallback in `MapJiraFieldToWindshift` unreachable for unknown app fields, even
when Jira reports an ordinary `string`, `number`, `date`, `datetime`, `user`,
`option`, or `array` schema (`field_mapper.go:173-211`).

The captured ASFJ Cloud fixture contains 95 field definitions but only 46
mapping suggestions. The omitted 49 include scalar fields whose reported
schemas are representable, such as:

- Jira Product Discovery string and number fields;
- charting and Service Management datetime fields;
- ProForma number fields;
- read-only string and number fields;
- third-party option and array fields.

This is silent loss: omitted fields are absent from the mapping UI, and the
readiness scan sees only a count of populated unknown values, not their names,
types, or samples.

There are additional semantic mismatches among allowlisted fields:

- `sd-request-participants` has `items=user` but is forced to `multiselect`
  instead of `multi_user`;
- multi-group pickers become strings rather than group relationships;
- cascading selects flatten parent and child into one `"parent / child"`
  string;
- datetime fields become Windshift calendar-date fields;
- multi-version fields become generic multiselect values rather than version
  references;
- context-specific Jira options and defaults are ignored.

`ensureCustomFields` initializes `fieldOptions` as empty except for Assets
fields (`internal/handlers/jira_import_execution.go:1531-1556`). Meanwhile,
`extractCustomFieldValue` stores select and multiselect display strings
(`internal/handlers/jira_import_entities.go:600-607`). Windshift's choice-field
contract is ID-based: definitions contain `{id,label}` options and values are
numeric option IDs. Imported choice fields are therefore displayable only as
raw fallback values and are not safely editable or validatable as native
Windshift select fields.

### Workflows

Both Jira clients implement `GetProjectWorkflowScheme` with
`/project/{key}/statuses`. They collapse the response to a unique status list
and return no transitions (`internal/jira/client.go:923-962` and the equivalent
Data Center implementation).

Execution does not use that method. `ensureWorkflowsAndConfigSet`:

1. fetches issue types and their available statuses;
2. groups issue types by identical status sets;
3. invents initial transitions to every status in Windshift category 1;
4. inserts all non-self status pairs as transitions.

See `internal/handlers/jira_import_execution.go:899-1042`.

Consequences:

- forbidden Jira transitions become permitted;
- loop transitions disappear;
- Jira global transitions are not distinguished from directed transitions;
- initial workflow semantics are guessed from status category;
- workflows with the same statuses but different graphs are merged;
- workflows with different identities but the same status set are merged;
- transition names, descriptions, screens, rules, and properties disappear.

The current Windshift workflow model also constrains fidelity. A transition has
only `(workflow, from status, to status, order, handles)` and the database has a
unique constraint on that tuple. Jira can have named transitions, multiple
transitions between the same statuses with different rules or screens, global
transitions, loop transitions, and transition-specific properties. Exact
import requires a model decision before only changing the Jira client.

### Conditions, validators, approvals, and actions

Windshift condition sets can express:

- current user, creator, assignee, regular user field, or custom user field in
  a Windshift role or group;
- a regex over one field value;
- a Windshift script;
- flat AND or OR logic for a transition;
- condition mode (hide) or validator mode (block with a message).

Jira exposes a larger rule language, including nested condition trees,
permissions, role/group/account restrictions, field comparisons, previous
status checks, parent/child blocking, separation of duties, app/Forge
expressions, approvals, and transition screens. Post-functions can mutate
fields, assign users, set resolution, fire events, or invoke apps.

The safe mapping boundary is:

| Jira rule | Windshift target | Fidelity |
|---|---|---|
| simple AND/OR field equality on an imported scalar field | `field_value` condition/validator | convertible after field-ID mapping |
| current user in a mapped role or mapped group | `user_in_role` / `user_in_group` | convertible only after explicit role/group mapping |
| assignee/reporter/current-user restrictions | user condition with the matching field reference | partially convertible |
| required field validator | new native `field_required` validator, or a carefully generated script | model extension recommended |
| nested condition tree | nested condition AST | not representable by current flat logic |
| previous-status, parent/child, separation-of-duties | new native rule types | not currently representable |
| Jira/JSM approval gate | Windshift approval set | requires an explicit approval/workflow mapping design |
| Connect/Forge/app rule | preserved opaque source rule | not executable in Windshift |
| transition post-function | preserved opaque source action | do not silently emulate |
| transition screen | preserved source metadata | no Windshift transition-screen context exists |

Generated scripts must not be the default compatibility mechanism. They are
harder to audit, can drift from Jira semantics, and would turn imported
configuration into executable code. Unsupported rules should remain disabled
and visible in the import report unless an operator explicitly replaces them.

### Screens and field layouts

Windshift can represent a useful subset:

- configuration-set create, edit, and view screens;
- per-item-type create, edit, and view overrides;
- a flat ordered list of system/custom fields;
- per-screen requiredness and width.

Jira company-managed projects compose:

1. project to issue-type-screen scheme;
2. issue type to screen scheme;
3. create/edit/view/default operation to screen;
4. screen tabs to ordered fields;
5. project/issue-type field configuration to hidden/required/description and
   renderer settings;
6. custom-field context to project/issue-type applicability and options.

This composition maps reasonably to Windshift after flattening tabs, but the
importer has no Jira client methods for it. The current screen helper
(`jira_import_execution.go:588-784`) only clones the already assigned Windshift
screens and appends imported Assets fields. It is not Jira screen or field
layout import.

Tab boundaries, field renderers, per-field descriptions, transition screens,
and Jira's dynamic issue layout are not represented and must be reported as
lossy or unsupported.

### Readiness reporting

The readiness scan samples issue payloads and custom-field values. It does not
inspect workflow graphs, workflow rules, screen schemes, field configurations,
custom-field contexts, or options. It can therefore produce a high score for a
project whose operational semantics will be substantially changed.

The report also labels all recognized custom-field types clean, even where
datetime precision, cascading structure, choice editability, or relationship
semantics are lost. Readiness needs configuration coverage and confidence, not
only issue-value coverage.

## Jira API capability tiers

The import plan must record what was actually accessible.

### Tier 1: ordinary migration credential

The existing Cloud setup asks primarily for `read:jira-work` and
`read:jira-user`. This is sufficient for issues, visible field definitions,
issue-type/status membership, and user-visible create/edit metadata. It is not
authoritative configuration access.

Tier 1 can:

- preserve issue values;
- infer scalar fallback types;
- query create metadata per project/issue type;
- query edit metadata for sampled issues;
- import a conservative field surface;
- report workflow and screen configuration as unavailable.

It must not generate an all-to-all workflow and call it equivalent to Jira.
A conservative fallback is a clearly named generated workflow with a warning,
or an operator-selected Windshift workflow.

### Tier 2: Jira project/global administration credential

For Jira Cloud, authoritative configuration uses APIs such as:

- `POST /rest/api/3/workflows` for workflow graphs;
- workflow capabilities for supported rule vocabulary;
- workflow scheme reads for workflow-to-issue-type assignments;
- issue type screen schemes, screen schemes, screens/tabs/fields;
- field configurations or field schemes;
- custom-field contexts and context options.

These endpoints generally require project/global administration permissions
and configuration scopes. The analyzer must probe capabilities and return
`available`, `forbidden`, `not_supported`, or `failed` per capability.

Relevant Atlassian references:

- [Workflows API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-workflows/)
- [Workflow schemes API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-workflow-schemes/)
- [Issue type screen schemes API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-type-screen-schemes/)
- [Screen schemes API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-screen-schemes/)
- [Screen tabs API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-screen-tabs/)
- [Issue field configurations API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-field-configurations/)
- [Custom-field contexts API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-custom-field-contexts/)
- [Custom-field options API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-custom-field-options/)

### Jira Data Center

Data Center must have a separate adapter and capability matrix. Do not assume
Cloud configuration endpoints exist. If REST cannot export authoritative
workflow descriptors, screens, or field layouts for the supported Data Center
versions, the supported alternatives are:

- an administrator-supplied Jira configuration export;
- an optional Windshift Jira app/export endpoint;
- explicit degraded import with preserved source metadata.

Database access to a Jira installation must not be a normal importer
requirement.

## Target design

### 1. Introduce a source configuration intermediate representation

Do not let API response shapes drive database writes directly. Add an internal
Jira import plan with stable source identities:

```text
JiraImportPlan
  capabilities[]
  projects[]
    issueType -> workflow source ID
    issueType -> create/edit/view screen source IDs
    issueType -> field configuration source ID
  fields[]
    schema, contexts, options, observed value shapes, disposition
  workflows[]
    statuses, transitions, condition tree, validators, actions, screens
  screens[]
    operation, tabs, ordered fields, required/hidden metadata
  findings[]
```

Every entity and rule gets a disposition:

- `exact`: native equivalent with the same contract;
- `converted`: imported with a documented deterministic conversion;
- `preserved`: retained as source metadata but not executable/editable;
- `unsupported`: recognized but not retained as active behavior;
- `unavailable`: the credential/API did not reveal the source configuration;
- `ignored`: explicitly selected by the operator.

The analyzer, wizard, executor, and final import report must consume the same
plan so preview and execution cannot drift.

### 2. Make custom fields never invisible

Replace the allowlist filter with classification:

1. recognize first-class Jira concepts such as sprint, story points, request
   type, Assets, and approvals;
2. map known native custom types;
3. infer compatible scalar/array types from `schema.type` and `schema.items`;
4. sample populated values and verify their runtime shapes;
5. offer `preserve as text/JSON` for complex or app-owned values;
6. show unsupported/internal fields and default them to skip, rather than
   removing them.

For select/multiselect/cascading fields:

- fetch contexts and all paged options when permitted;
- union options for the target Windshift definition while retaining Jira
  context and option IDs in mapping metadata;
- allocate stable Windshift numeric option IDs;
- convert issue values to those IDs;
- preserve cascading parent/child IDs even if the editable Windshift field is a
  flattened representation;
- add observed options absent from configuration access as explicitly
  discovered values;
- report that Windshift does not enforce Jira context-specific option subsets.

For datetime fields, either add a Windshift datetime custom-field type or mark
conversion to calendar date as lossy. It must not be classified clean.

### 3. Implement WI-229 workflow import conservatively

The first workflow slice should:

1. add client capability discovery;
2. read Cloud workflow schemes for each project and resolve the exact workflow
   source ID for every issue type;
3. fetch those workflows with statuses, initial/directed/global/loop
   transitions, names, rules, and screen references;
4. create one Windshift workflow per distinct Jira workflow source ID, not per
   status set;
5. import exact initial and directed edges;
6. expand a Jira global transition into source-status edges only when doing so
   preserves behavior, retaining the Jira transition ID as metadata;
7. preserve loop transitions if Windshift supports them after validation;
8. bind issue types to workflow overrides from the Jira scheme;
9. produce findings for every rule or transition property not activated.

Before supporting duplicate Jira transitions between the same status pair,
extend the Windshift transition model with source identity and display name and
revisit the unique constraint. Collapsing duplicates is acceptable only as an
explicit lossy operator choice.

If graph access is unavailable, do not infer exact transitions from status
membership. Present these choices:

- use a conservative generated workflow;
- select an existing Windshift workflow;
- provide an administrative Jira connection/export;
- continue with statuses only and a prominent fidelity warning.

### 4. Add screen and field-layout import

For Cloud company-managed projects with configuration access:

1. resolve project issue-type-screen scheme mappings;
2. resolve each screen scheme's create/edit/view/default screen IDs;
3. fetch all tabs and ordered fields;
4. resolve field configuration per issue type;
5. exclude hidden fields and apply requiredness;
6. map Jira system field IDs to Windshift system field identifiers;
7. map Jira custom field IDs through the custom-field plan;
8. flatten tabs in tab/order sequence;
9. create/reuse Windshift screens by stable source fingerprint;
10. assign configuration defaults and per-item-type overrides.

For Tier 1, create metadata can produce an effective create screen, and sampled
edit metadata can inform a best-effort edit screen. Such screens must be marked
`converted`, not exact, because results are permission-, issue-, status-, and
context-dependent. View should fall back explicitly rather than being assumed
equal to edit.

### 5. Import only proven workflow rules

Start with a mapping registry keyed by Jira rule key/type. A mapping function
must return:

- the native Windshift condition/validator;
- its fidelity and prerequisites;
- or an unsupported finding with preserved source configuration.

Only flat, fully understood rule trees should activate automatically. Nested
trees, unmapped fields, missing group/role mappings, app rules, and rule types
without equivalent semantics remain inactive. The import must never broaden a
restricted transition because one rule was dropped.

A safe default for a partially mapped guarded transition is to keep the edge
disabled or require operator acknowledgement. “Import the edge without its
guard” is not a safe default.

### 6. Expand the import report

Report counts and source identities for:

- fields exact/converted/preserved/unsupported/unavailable/ignored;
- populated values imported, preserved, or dropped;
- workflows and issue-type assignments;
- exact versus generated transitions;
- conditions, validators, approvals, actions, and transition screens;
- screens, tabs flattened, fields hidden/required, and context assignments;
- APIs forbidden or unavailable.

The final job persists the normalized plan fingerprint and prior-import
comparison so a re-import exposes configuration drift.

## Delivery slices

### Slice A: truth before behavior

- Add capability probing and configuration findings to readiness.
- Stop hiding unknown fields.
- Show field name, schema, sample shape, confidence, and fallback in the
  wizard.
- Mark the existing all-to-all workflow as generated/lossy.
- Add captured Cloud and Data Center fixtures for each capability tier.

### Slice B: custom-field correctness

- Import contexts/options.
- Store select values as Windshift option IDs.
- Refine schema mapping using both custom key and `schema.items`.
- Add raw JSON/text preservation.
- Correct readiness classifications.

### Slice C: WI-229 workflow graph and schemes

- Add workflow/scheme DTOs and clients.
- Import exact graph and per-issue-type assignments.
- Handle initial, directed, global, and loop transitions.
- Add model support for transition source identity/name and duplicates if
  required.

### Slice D: WI-229 screens

- Add issue-type screen scheme, screen scheme, tab/field, and field
  configuration clients.
- Compose create/edit/view screens and per-item-type overrides.
- Document and report flattened tabs, renderer loss, and transition screens.

### Slice E: guarded transitions

- Add the rule mapping registry.
- Implement the small exact subset first.
- Add model rule types where justified.
- Integrate Jira approvals only after approval identities and transitions have
  an explicit mapping.

## Required tests

Tests belong in `../core-tests` and must cover Cloud and Data Center adapters
separately.

Minimum regression cases:

- an unknown app field with scalar schema remains visible and preserves values;
- an unknown complex object/array is preserved as JSON and reported converted;
- request participants refine to multi-user from `schema.items=user`;
- select/multiselect options are paged, assigned stable target IDs, and item
  values use those IDs;
- two contexts with overlapping/distinct options produce deterministic output;
- datetime-to-date is reported lossy;
- two Jira workflows with the same statuses but different graphs stay distinct;
- forbidden edges are absent;
- initial, global, loop, and duplicate endpoint transitions;
- issue type to workflow assignment comes from the scheme;
- unavailable workflow API does not masquerade as an empty workflow;
- simple mapped conditions and validators enforce the exact denial contract;
- an unsupported guard does not result in an unguarded active edge;
- screen scheme default fallback and per-operation overrides;
- per-issue-type screen overrides;
- hidden and required field configuration;
- tab flattening order;
- unavailable screen APIs produce an explicit finding and deterministic
  fallback.

Run focused overlay tests while iterating, then both SQLite and PostgreSQL
coverage for the importer because workflows, conditions, screens, mappings,
and cleanup are database-backed.

## WI-229 acceptance clarification

WI-229 should be considered complete only when:

- actual Jira workflow source identities and graphs are imported where
  authoritative data is available;
- status-only fallback is explicit and never described as exact;
- issue-type workflow assignments come from Jira schemes;
- Jira create/edit/view screen composition is imported where available;
- configuration access limitations are surfaced;
- unsupported rule, post-function, transition-screen, and layout semantics are
  enumerated in readiness and the final job report;
- no dropped guard can broaden a transition without explicit operator action.

The 0.8.4 implementation satisfies these criteria for Jira Cloud
company-managed projects and provides explicit conservative/unavailable
behavior for other capability tiers. It intentionally does not claim exact
rule or field-configuration conversion: non-initial Cloud transitions remain
review-locked because condition trees are not returned by the current source
API, and field requiredness/hidden rules are reported rather than inferred.
