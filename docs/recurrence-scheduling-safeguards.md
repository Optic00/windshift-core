# Recurrence scheduling safeguards

Windshift limits recurrence-rule cardinality and exposes scheduler pressure in
System Administration. These safeguards bound the number of rules the
scheduler must inspect, but they do not yet impose a per-rule instance budget.

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

1. Expands occurrences from the last completed boundary through the configured
   lead-time window, honoring RRULE `COUNT` and `UNTIL` boundaries.
2. Skips dates that already have an instance.
3. Creates each item and its recurrence-instance record in one transaction.
4. Advances generation progress through the last successful or already-known
   occurrence.

A failure stops processing that rule at the failed occurrence so the next pass
can retry without losing a date. Other rules in the selected batch continue.
Failed rules are scheduled for the next five-minute pass, and the run is
recorded as failed in scheduler diagnostics. A clean rule is checked again
after 24 hours.

## Remaining per-run safeguards

The 100-rule workspace limit and 100-rule scheduler batch limit constrain rule
cardinality and scheduler selection. They do not cap the number of occurrences
expanded or items created while processing one rule. A dense rule combined
with a large lead-time window can therefore still do substantial work in a
single pass.

There is currently no per-rule occurrence cap, per-pass item-generation cap,
execution deadline, or cancellation boundary inside recurrence expansion.
Those controls should be added before supporting substantially denser
frequencies or larger lead-time windows. Until then, administrators should use
the recurrence-volume and scheduler-run diagnostics to identify sustained
backlogs or failing rules.
