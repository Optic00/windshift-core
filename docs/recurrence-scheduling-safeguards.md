# Recurrence scheduling safeguards

Windshift limits recurrence-rule cardinality, look-ahead, and per-pass
expansion while exposing scheduler pressure in System Administration.

## Workspace rule limit

Each workspace can contain at most 100 recurrence rules. Active and inactive
rules both count toward the limit. Deleting a rule releases one place.

The cookie API and REST v1 creation paths delegate to the same recurrence
service. Creation locks the workspace, checks item uniqueness and the workspace
count in one transaction, then inserts the rule. Concurrent requests therefore
cannot both create a 101st rule.

When the workspace is full, both creation paths return HTTP `409` with code
`CONFLICT` and this stable message:

> This workspace has reached the limit of 100 recurrence rules

The rejected rule is not persisted.

## Administrator diagnostics

System administrators can inspect recurrence volume under **System
Administration → Diagnostics → Recurrence**. The diagnostic reports:

- total, active, and currently due rules;
- whether the due queue exceeds one scheduler batch;
- the total and active rule count for each workspace;
- workspaces at the warning threshold or the hard limit.

The warning diagnostic is enabled by default at 80 rules per workspace.
Administrators can set the threshold from 1 through 100 or disable warnings.
Disabling warnings does not disable the hard limit or hide the underlying
counts.

The matching endpoints are:

- `GET /api/admin/diagnostics/recurrence-volume`
- `PUT /api/admin/diagnostics/recurrence-volume`

Both endpoints require the system-administrator permission.

## Scheduler batch behavior

The recurrence scheduler runs once at startup and then every five minutes. A
pass selects at most 100 due active rules, prioritizing rules that have never
been checked and then the oldest scheduled check. Diagnostics marks the queue
as backlogged when more than 100 rules are due.

For each selected rule, the scheduler:

1. Expands occurrences incrementally from the last completed boundary through
   the configured lead-time window, honoring RRULE `COUNT` and `UNTIL`
   boundaries.
2. Skips dates that already have an instance.
3. Examines and creates at most 100 occurrences for that rule in one pass.
4. Creates each item and its recurrence-instance record in one transaction.
5. Advances generation progress through the last successful or already-known
   occurrence.

A failure stops processing that rule at the failed occurrence so the next pass
can retry without losing a date. A rule that reaches the 100-occurrence budget
also remains due and resumes on the next five-minute pass. Other rules in the
selected batch continue. Failed rules are recorded as failed in scheduler
diagnostics. A clean rule that reaches its look-ahead horizon is checked again
after 24 hours.

## Lead-time limit

`lead_time_days` must be between 0 and 365. The shared recurrence service
enforces this range for cookie API and REST v1 create and update requests.

The limits bound work per pass rather than total instances over a rule's
lifetime. Ending conditions remain preferable for high-frequency rules, and
administrators should use recurrence-volume and scheduler-run diagnostics to
identify sustained backlogs or failing rules.
