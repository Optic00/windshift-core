# Importing Xray test cases from Jira

Status: WI-767 MVP implemented for 0.8.4; follow-on fidelity documented below
Date: 2026-07-26

## Implemented in 0.8.4

The initial implementation now:

- detects Jira Cloud Xray Test issue types only from the exact
  `com.xpandit.xray.issuetype.test` property;
- detects Data Center tests through Raven without consulting issue-type names;
- inserts a conditional Xray wizard screen after project analysis;
- keeps Xray Cloud client credentials in memory for the active import and out
  of the durable job configuration;
- reads Cloud definitions in batches of 100 through GraphQL, retries bounded
  `429` responses using `Retry-After`, and reads Data Center steps through
  Raven with the existing Jira authorization header;
- imports each test case, its ordered manual steps, labels, timestamps, and
  source mapping atomically;
- reports test-case progress separately and removes imported test cases through
  the existing import cleanup operation.

The 0.8.4 slice maps manual `action`, `data`, and `result` fields. Repository
folders, descriptions/source metadata, Xray step attachments, called-test
expansion, Generic/Cucumber bodies, and coverage links remain follow-on work.
Those source concepts are described below so later fidelity work does not need
to rediscover the API contracts.

### Live contract verification

The 0.8.4 path was verified read-only against the storymap-premium Jira/Xray
Cloud tenant on 2026-07-26:

- project `KN` (`Kanban2`) exposed Xray Test issue type ID `10089`;
- the exact issue-type property existed with the live value `{}`;
- Jira issue `KN-21` (numeric ID `10520`) was discovered without using its
  issue-type display name;
- Xray GraphQL returned a `Manual` definition with two steps;
- the browser import journey created one Windshift test case and rendered both
  steps in the same action/data/result order returned by Xray.

The opt-in live checks keep credentials in the ignored local configuration and
never print them or add them to repository fixtures.

## Summary

Windshift can import Xray test cases, but Jira REST alone is not sufficient for
all deployments:

- Xray Cloud stores test definitions, including manual steps, outside Jira.
  Windshift must authenticate to the Xray Cloud API separately and read the
  definitions through Xray GraphQL.
- Xray Server/Data Center exposes manual steps through the Xray REST API
  installed on the Jira host. The existing Jira credentials can normally be
  reused.

The recommended implementation keeps the existing Jira import as the source of
issue metadata and adds an optional Xray enrichment path. Jira connects first.
After the operator selects projects, the normal Jira analysis checks
Xray-owned issue-type metadata rather than issue-type names. Only when Xray
positively identifies Tests in the selected projects does the wizard offer an
Xray test-case import and, for Cloud, ask for the Xray API key. A Jira issue
selected as an Xray Test becomes a Windshift `test_case`, not a normal work
item.

Xray's built-in issue type is named **Test** in its documentation. An
installation may expose or rename it as **Test Case**, and another test
management product may independently use either name. Windshift must never use
the name, description, avatar, or numeric Jira issue-type ID as proof of Xray
ownership.

## What is available from each API

### Jira REST

The current importer already obtains the useful Jira-owned portion of a test:

- issue ID and key
- summary and description
- issue type, status, and priority
- labels, components, versions, users, timestamps, issue links, comments, and
  Jira attachments
- arbitrary `customfield_*` values

This is enough to discover candidate test issues and import their ordinary Jira
metadata. It is not a reliable way to obtain Xray Cloud steps. Xray documents
that Cloud testing information is stored as Xray entities and cannot be managed
through Jira's API:

- <https://getxraydocs.atlassian.net/wiki/spaces/XRAYCLOUD/pages/44574479/API>
- <https://getxraydocs.atlassian.net/wiki/spaces/XRAYCLOUD/pages/44564726/Xray+Server+and+Xray+Cloud>

### Xray Cloud GraphQL

The API endpoint is:

```text
https://<xray-region>.xray.cloud.getxray.app/api/v2/graphql
```

The global endpoint is `https://xray.cloud.getxray.app/api/v2/graphql`.
Regional hosts are available for US, EU, and Australia. Windshift should offer a
closed enum rather than an arbitrary URL:

- global: `xray.cloud.getxray.app`
- US: `us.xray.cloud.getxray.app`
- EU: `eu.xray.cloud.getxray.app`
- Australia: `au.xray.cloud.getxray.app`

Xray lists the regional endpoints and current Cloud rate limits here:

- <https://getxraydocs.atlassian.net/wiki/spaces/XRAYCLOUD/pages/44565892/REST+API>

Cloud authentication is separate from Jira authentication. An Xray API key
provides a client ID and client secret. Windshift exchanges them for a bearer
token using:

```http
POST https://<xray-region>.xray.cloud.getxray.app/api/v2/authenticate
Content-Type: application/json

{"client_id":"...","client_secret":"..."}
```

The resulting token currently expires after 24 hours. It should be held in
memory for the import and refreshed once after an authentication failure, not
stored as a new long-lived secret.

The most useful query is `getTests` or `getExpandedTests`, passing Jira's
numeric issue IDs. `getTests` accepts at most 100 tests per page. It returns:

- Xray test type and kind
- manual steps
- unstructured or Gherkin definitions
- Test Repository folder
- step attachments and custom step fields
- referenced/called tests

The step mapping is direct:

| Xray GraphQL `Step` | Windshift `test_steps` |
| --- | --- |
| `action` | `action` |
| `data` | `data` |
| `result` | `expected` |
| list order | `step_number` |

Relevant schema documentation:

- [`getTests`](https://us.xray.cloud.getxray.app/doc/graphql/gettests.doc.html)
- [`Step`](https://us.xray.cloud.getxray.app/doc/graphql/step.doc.html)
- [`Test`](https://us.xray.cloud.getxray.app/doc/graphql/test.doc.html)
- [`Attachment`](https://us.xray.cloud.getxray.app/doc/graphql/attachment.doc.html)

A suitable query is:

```graphql
query ImportTests($issueIds: [String], $limit: Int!) {
  getTests(issueIds: $issueIds, limit: $limit) {
    total
    results {
      issueId
      testType {
        name
        kind
      }
      unstructured
      gherkin
      scenarioType
      folder {
        name
        path
      }
      steps {
        id
        libStepId
        callTestIssueId
        action
        data
        result
        attachments {
          id
          filename
          storedInJira
          downloadLink
        }
        customFields {
          id
          name
          value
        }
      }
    }
  }
}
```

For Tests that call other Tests, Windshift currently has no equivalent step
type. Xray offers `getExpandedTests`, which returns an execution-equivalent,
flattened list plus warnings. Using it is the most faithful MVP behavior, as
long as Windshift records that the steps were expanded:

- <https://us.xray.cloud.getxray.app/doc/graphql/getexpandedtests.doc.html>
- <https://us.xray.cloud.getxray.app/doc/graphql/expandedstep.doc.html>

### Xray Server/Data Center REST

The Xray REST API is colocated with Jira:

```http
GET https://<jira-host>/rest/raven/2.0/api/test/{testKey}/step
```

Xray documents version 2.0 as the latest REST version and states that it uses
Jira authentication. Windshift uses Jira Data Center personal access tokens for
these requests, so it can reuse the PAT already stored for the Data Center Jira
connection:

- <https://getxraydocs.atlassian.net/wiki/spaces/XRAY600/pages/50595106/REST+API>
- <https://getxraydocs.atlassian.net/wiki/spaces/XRAY640/pages/50070592/Test+Steps+-+REST>

The response contains the manual step fields corresponding to action, data, and
expected result. Xray Server/DC versions have used slightly different response
envelopes and field casing. Before implementation is declared complete, capture
real read-only payloads from at least one supported Xray 7/8 instance and keep
the sanitized response variants as contract fixtures in `../core-tests`.

The Data Center endpoint is per Test rather than bulk. Fetches should therefore
use bounded concurrency, honor cancellation and `Retry-After`, and stop
hammering the endpoint after a systemic authorization or app-not-installed
failure.

## Proposed wizard behavior

### Positive Xray detection

The Xray prompt must be driven by an Xray-owned fingerprint, not a naming
heuristic.

#### Jira Cloud

Xray Cloud marks the Jira issue types it owns with issue-type entity
properties. The current public Xray Connect descriptor uses these keys:

```text
com.xpandit.xray.issuetype.test
com.xpandit.xray.issuetype.pre_condition
com.xpandit.xray.issuetype.test_set
com.xpandit.xray.issuetype.test_plan
com.xpandit.xray.issuetype.test_execution
com.xpandit.xray.issuetype.sub_test_execution
```

For test-case import, the decisive marker is:

```text
com.xpandit.xray.issuetype.test = true
```

After project selection, Windshift already knows each project's issue-type IDs.
For each unique ID, call:

```http
GET /rest/api/3/issuetype/{issueTypeId}/properties/com.xpandit.xray.issuetype.test
```

Only an issue type for which this exact Xray-owned property exists is an Xray
Test type. A current Xray Cloud installation was observed returning `{}` as the
property value, while Jira response variants may wrap or expose a boolean
value. The importer therefore treats a successful response for the exact key as
positive, except for an explicit boolean `false`. This remains valid if an
administrator renames the type. A third-party `Test` or `Test Case` type will
not have Xray's property and must not trigger the prompt.

Atlassian documents the read-only issue-type property API and permits reading
properties for issue types associated with projects the user can browse:

- <https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-type-properties/>

Xray's current public app descriptor is the source of the property namespace
and also registers the per-issue properties `xrayIssueType` and `testType`:

- <https://xray.cloud.getxray.app/atlassian-connect.json>

The project property
`com.xpandit.xray.project.enabled=true` can be used as a cheap corroborating
signal:

```http
GET /rest/api/3/project/{projectIdOrKey}/properties/com.xpandit.xray.project.enabled
```

It must not be the sole decision. A project-level setting proves that Xray is
enabled for a project but does not identify which issue type is the Test type.
Conversely, a migrated or partially repaired installation may have inconsistent
project settings. Jira's project-property API is documented here:

- <https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-project-properties/>

As an optional consistency check, Windshift can read
`/issue/{key}/properties/xrayIssueType` for one issue of the marked type. A
missing or contradictory issue property should produce an Xray migration-health
warning, not cause Windshift to guess from the type name.

#### Jira Server/Data Center

The Cloud entity-property contract should not be assumed on Data Center. Xray
Data Center exposes its own read-only settings endpoint using the existing Jira
credential:

```http
GET /rest/raven/2.0/api/settings/xrayIssueTypes
```

It returns the Jira IDs registered with Xray. Match those IDs against the
selected projects, then use Xray's Test export or step endpoint to positively
identify actual Tests:

```http
GET /rest/raven/2.0/api/test?jql=project%20%3D%20ABC
GET /rest/raven/2.0/api/test/{issueKey}/step
```

The first endpoint is preferable for project-wide discovery where supported.
For older/version-specific response behavior, query one issue per registered
type through the Test endpoint. A successful Xray Test response is the proof;
the returned name or description is not.

Xray's v2 REST schema describes `settings/xrayIssueTypes` as returning all Xray
issue types:

- <https://swagger.cloud.getxray.app/>

The documented Test export endpoint accepts keys, filter ID, or JQL:

- <https://getxraydocs.atlassian.net/wiki/spaces/XRAY630/pages/45844530/Tests+-+REST>

If Raven returns 404, the Xray app is absent, disabled, or the endpoint is
unsupported. If it returns 401/403, report that detection could not be completed
with the Jira credential. Neither case authorizes falling back to issue-type
names.

### Connection and step order

Keep the initial Jira connection screen unchanged. Do not ask for Xray
credentials before the operator has selected projects and Windshift knows
from the positive Xray fingerprint whether those projects contain Xray Tests.

The proposed sequence is:

```text
Connect to Jira
  → Select projects
  → Analyze selected projects with Jira
  → Xray test cases (conditional)
  → Mapping
  → Preview
  → Import
```

The conditional Xray screen appears only when the selected projects contain
issues of an Xray-marked Test type. It should show the Jira counts already
available from analysis and ask:

```text
We found N Xray test cases in the selected projects.
Import these into Windshift Test Management?
```

If the operator declines, no Xray credential is requested and the wizard
continues. The UI must state whether those Jira issues will follow the ordinary
work-item mapping or be skipped; it must not silently choose.

If the operator accepts:

- For Jira Cloud, request Xray region and the Xray API key's **client ID and
  client secret**. Although Xray calls this an API key, it is a credential pair,
  not the Jira API token and not one opaque key field.
- For Jira Data Center, explain that the existing Jira credentials will be
  reused and do not show another secret field.
- Test Xray connectivity and permissions before advancing.
- Enrich the Jira count with the Xray-validated test type breakdown.
- If validation returns no Xray Tests, keep the operator on this screen with a
  useful explanation rather than allowing an empty-looking import.

Cloud detection is possible before Xray authentication because the Xray-owned
issue-type property lives in Jira and is readable with the Jira credential.
The later Xray credential is still required to retrieve Cloud step definitions.
Data Center detection and step retrieval both use Raven with the existing Jira
credential.

Test Jira and Xray connectivity independently and show separate errors. Encrypt
the Xray client secret separately from the Jira credential. Never put either
secret or the temporary bearer token in job `config_json`, payload capture,
logs, or audit details.

For saved Jira Cloud connections, Xray credentials should be optional and
replaceable without replacing the Jira API token. They are only loaded after
the operator opts into Xray import.

### Analysis and mapping

The current issue-type mapping is effectively a list of types that will all
become Windshift item types. Extend it with a destination:

```text
Work item | Test case | Skip
```

Suggested defaults:

- Xray-validated Tests: `Test case`
- all other issue types: `Work item`
- Xray entity types that are not yet supported, such as Test Execution:
  `Skip`, with a clear explanation

For Cloud, the Xray issue-type property identifies candidates before the
prompt, and `getTests` then validates and enriches their issue IDs. For Data
Center, Raven identifies candidates and returns their definitions. Issue types
without a positive Xray marker remain ordinary work-item mappings even if their
name contains `Test`.

The analysis response should include, per project and selected issue type:

- number of Jira issues
- number validated as Xray Tests
- breakdown by Xray test kind/type (`Manual`, `Generic`, `Cucumber`, and custom
  types)
- number with manual steps
- number with called-test expansion warnings
- unsupported count and reason

The preview and final job result should report work items and test cases
separately. “Imported issues” is no longer precise enough.

## Import mapping

For the first production slice, import Xray **Manual** Tests:

| Source | Windshift target | Notes |
| --- | --- | --- |
| Jira project | workspace | Existing mapping |
| Xray Test Repository folder path | nested `test_folders` | Create/reuse each path segment |
| Jira summary | `test_cases.title` | Required |
| Jira description | new `test_cases.description` | Do not misuse `preconditions` |
| Jira priority | `test_cases.priority` | Reuse normalized priority names |
| Xray manual step action | `test_steps.action` | Preserve order |
| Xray manual step data | `test_steps.data` | Preserve empty values |
| Xray manual step result | `test_steps.expected` | Preserve empty values |
| Jira labels | `test_labels` | Workspace-scoped create/reuse |
| Jira created/updated | test-case timestamps | Preserve source chronology |
| Jira issue ID/key | import ID mapping | `entity_type = 'test_case'` |
| Xray step ID | import ID mapping or step source metadata | Needed for diagnostics/re-import |
| Jira requirement/coverage links | `item_links` using Tests link type | Resolve after both sides import |

Windshift currently lacks a description/source-metadata field on test cases. A
small schema/model/API addition is preferable to placing Jira description,
assignee, status, or origin information into the semantically different
`preconditions` field.

Status also needs an explicit rule. Jira workflow status does not map cleanly
to Windshift's `active`, `inactive`, and `draft`. The safe MVP default is
`active`, while retaining the Jira status as source metadata. A later wizard
mapping can translate selected Jira statuses to the three test-case states.

### Deferred but preserved visibly

These Xray concepts do not have lossless Windshift targets today:

- Generic test definition
- Cucumber/Gherkin definition and scenario outlines
- datasets and parameterized iterations
- custom step fields
- Test versions other than the default version
- step-level attachments
- references to shared library steps
- Xray Preconditions as first-class entities
- Test Sets, Test Plans, Test Executions, Test Runs, and execution history

The importer must not silently discard these. The preview should show exact
counts and the final result should contain warnings. For Generic and Cucumber
Tests, the initial release should skip creation rather than manufacture a
misleading manual step. A later schema can add a test definition kind and
definition body.

Jira issue attachments can already be attached to a Windshift test case after
the attachment importer is generalized from `item` to `test_case`. Xray
step-level attachments should wait until Windshift either supports
`entity_type = 'test_step'` or defines a deliberate test-case-level fallback
that retains the source step ID.

## Backend shape

Add a narrow read-only Xray client interface separate from `jira.Client`:

```go
type XrayClient interface {
    TestConnection(ctx context.Context) error
    GetTests(ctx context.Context, issueIDs []string) ([]XrayTest, error)
}
```

Implementations:

- `xrayCloudClient`: static authentication and GraphQL endpoints, fixed query,
  batches of at most 100, token caching, regional host allowlist.
- `xrayDataCenterClient`: Jira base URL plus
  `/rest/raven/2.0/api/test/{key}/step`, existing Jira auth, bounded
  concurrency.

Keep mutation operations out of the interface. The Cloud transport must permit
POST only for the static authentication request and static GraphQL query, in
the same spirit as the Jira importer's read-only request validation.

During execution:

1. Fetch the existing Jira batch with `*all`.
2. Partition it by the operator's destination mapping.
3. Enrich candidate test issues through the appropriate Xray client.
4. Create each test case, its folders, labels, and steps in one database
   transaction.
5. Record `test_case` and, where useful, `test_step` source mappings.
6. Import ordinary issues through the existing work-item path.
7. Resolve cross-entity Jira links after all items and test cases exist.

If an issue selected as a Test is not returned by Xray, record an exact failure
for that issue. Do not silently fall back to importing it as a work item,
because that creates the wrong entity and hides missing Xray permissions.

The import cleanup path must recognize `test_case` before `workspace`.
Deleting a test case will cascade its steps and labels junction rows. Any
test-case attachments and item links must be deleted first, matching the
existing dependency order.

## Error and security behavior

- Distinguish “Xray not installed”, “bad Xray credentials”, “no Browse Projects
  permission”, “not an Xray Test”, rate limiting, malformed payload, and
  unsupported test kind.
- A failure to read Xray steps must fail or partially fail that test case; it
  must never create a successful-looking case with zero steps unless the source
  genuinely has zero steps.
- Log Xray status code, request ID, deployment kind, and endpoint region, but
  never authorization headers or request bodies containing credentials.
- Use the existing Jira host validation for Data Center. Cloud calls only the
  fixed Xray host allowlist.
- Continue respecting the importer's two-hour context and surface cancellation
  in progress instead of converting it to an arbitrary upstream error.

## Delivery plan

### 1. Payload spike and contracts

- Capture sanitized Cloud GraphQL responses for manual, empty, called, Generic,
  and Cucumber Tests.
- Capture sanitized Data Center step responses from supported Xray versions.
- Confirm rich-text encoding, line breaks, empty expected results, and step
  attachment URLs.

### 2. Xray connection and client

- Add the conditional post-project Xray screen.
- Add encrypted Cloud credentials and region as an optional companion to the
  Jira import connection.
- Add read-only Cloud and Data Center clients.
- Add connection validation and typed errors.

### 3. Analysis and wizard

- Add issue destination mapping.
- Detect/validate Xray Tests.
- Show exact supported and unsupported counts.
- Keep existing behavior unchanged when Xray import is disabled.

### 4. Manual Test import

- Add the missing test-case description/source representation.
- Import folders, cases, manual steps, labels, timestamps, and Jira issue
  attachments.
- Extend ID mapping, progress, results, links, and cleanup.

### 5. Follow-on fidelity

- Preconditions and requirement coverage.
- Step attachments and custom step fields.
- Generic/Cucumber definitions and datasets.
- Test Sets/Plans and optional execution history.

## Required tests

All tests and test-only fixtures belong in `../core-tests`.

At minimum, cover:

- Cloud authentication, token refresh, batching at 100, pagination, rate
  limiting, GraphQL partial errors, and regional endpoints.
- Data Center PAT authentication, response-envelope variants, 404,
  forbidden, and app-not-installed behavior.
- issue types named `Test` and `Test Case` from another test-management product
  do not trigger the Xray prompt.
- Cloud Xray Test detection uses the exact
  `com.xpandit.xray.issuetype.test` property, including the live `{}` value and
  a renamed issue type.
- project-enabled property without an Xray Test issue-type property does not
  trigger import.
- Data Center discovery uses Raven's registered issue types/Test response, not
  the returned display name.
- exact action/data/result ordering and empty-field preservation.
- a called Test expanded into deterministic steps with warnings retained.
- Manual/Generic/Cucumber classification.
- folder creation/reuse and cross-workspace isolation.
- transaction rollback when any step insert fails.
- exact partial-failure progress and no work-item fallback.
- item-to-test coverage links.
- cleanup and re-import without duplicate cases or folders.
- denial when the Jira user can browse an issue but the Xray API-key user
  cannot read its Xray data.

## Recommendation

Build the first slice around Manual Tests only, using Jira REST plus Xray
enrichment. Cloud GraphQL should be the primary path and Data Center's Raven
REST endpoint the deployment-specific adapter. Make the destination explicit
in the wizard, validate by API rather than issue-type name, and expose every
unsupported Xray concept in preview. This gives Windshift a useful import
without presenting a lossy migration as complete.
