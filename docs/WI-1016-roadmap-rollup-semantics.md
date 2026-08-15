# WI-1016 roadmap hierarchy date semantics

Status: accepted design decision for the scheduling specification.

This document defines the optional roll-up and roll-down behavior in the
roadmap. Dependency equations, calendars, constraints, and preview/apply
behavior remain separate decisions.

## Decision

Work-item `start_date` and `end_date` are the canonical scheduling dates. The
feature does not create another persisted set of dates.

The roadmap Settings menu provides three local presentation modes:

- **Off** shows every item's stored dates and preserves existing behavior.
- **Roll up** derives summary ranges from descendants.
- **Roll down** constrains descendant ranges to their parents.

Roll up and Roll down are available only when the roadmap uses `start_date` as
its start field and `end_date` as its end field. Roadmaps using `due_date` or
custom date fields keep their current behavior. Selecting the canonical fields
does not migrate values from other fields.

The separate **Adjust related dates** control determines whether a roadmap
date edit also persists the mode's related-date changes. Switching modes or
switching adjustment on or off never writes item data.

Both controls are view-local. They do not enable scheduling behavior in boards,
backlogs, lists, calendars, analytics, time tracking, or any other workspace
module.

## Roll-up behavior

An item without dated descendants uses its own stored dates. A parent with
dated descendants is a summary task:

- its effective start is the earliest descendant start;
- its effective end is the latest descendant end;
- nested summaries contribute recursively; and
- its own stored dates are retained but ignored in the roll-up display.

The summary bar is read-only. It cannot be moved or resized directly; users
change the range by editing descendants.

With date adjustment off, the summary exists only in the roadmap projection.
With date adjustment on, a roadmap edit to a child persists the edited child
and the recalculated ranges of each affected ancestor in one atomic operation.
The bar remains a calculated summary while Roll up is active, even when its
calculated range equals the newly stored parent range.

## Roll-down behavior

Roll down evaluates each descendant against its effective parent range,
recursively. It changes only boundaries outside the parent:

- a child start before the parent start moves to the parent start;
- a child end after the parent end moves to the parent end;
- a boundary already inside the parent remains unchanged; and
- a child wholly before or after the parent collapses to the nearest parent
  boundary, preserving a valid inclusive range.

For example, shrinking a parent to 2026-08-10 through 2026-08-20 produces:

| Child stored range | Effective range |
| --- | --- |
| 2026-08-12 through 2026-08-18 | unchanged |
| 2026-08-05 through 2026-08-15 | 2026-08-10 through 2026-08-15 |
| 2026-08-16 through 2026-08-25 | 2026-08-16 through 2026-08-20 |
| 2026-08-25 through 2026-08-28 | 2026-08-20 through 2026-08-20 |

With date adjustment off, these effective ranges exist only in the roadmap
projection. With date adjustment on, a roadmap edit to a parent persists the
parent edit and only the descendant boundaries that the recursive calculation
must move. The full set of changes is atomic: validation or permission failure
on any item leaves every item unchanged.

## Date rules

- Values are Gregorian civil dates formatted as `YYYY-MM-DD`.
- Dates are not converted through browser, user, or workspace timezones.
- Start and end are inclusive. An item from 2026-08-10 through 2026-08-12
  occupies three calendar dates.
- A single populated boundary is a one-day range for hierarchy calculations.
- An item with neither date does not contribute to a roll-up range and is not
  changed by roll-down.

## View scope and permissions

The hierarchy defines the contributor universe. For every displayed item, the
roadmap obtains the complete same-workspace descendant set that the viewer may
see. Collection membership, transient filters, pagination, and collapsed tree
state do not change an effective range. Descendants loaded only for calculation
are not inserted into the visible collection.

Any roadmap viewer may select a mode because selection is read-only. Persisted
adjustment uses the normal item-edit authorization, validation, history,
notification, workflow, and automation contracts for every changed item.
Hierarchy modes grant no additional access and do not reveal inaccessible
descendant identities.

## Compatibility and isolation

- Off is the default for every roadmap.
- Enabling, disabling, or changing a hierarchy mode emits no item history,
  audit entry, notification, workflow, or automation event.
- Direct edits without adjustment continue through the ordinary single-item
  update path.
- Related-date adjustment is performed only after a direct roadmap date edit.
- Related updates are submitted as one atomic batch; partial persistence is not
  allowed.
- Existing dependency links gain no scheduling semantics from these modes.
- Other modules always read stored dates and never receive the roadmap's
  calculated projection as replacement item values.
- The hierarchy response contains minimal date data and never shadows the
  standard item API fields.

## Required verification

Implementation must cover:

- Off defaults to existing roadmap behavior;
- switching modes changes no persisted data;
- nested descendants roll up recursively;
- roll-up summaries are read-only;
- roll-up adjustment persists a leaf and all affected ancestors atomically;
- roll-down leaves in-range child boundaries alone;
- roll-down clamps only overflowing boundaries, including nested descendants;
- a wholly out-of-range child collapses to the nearest parent boundary;
- filters, pagination, and collapse state do not change the projection;
- unauthorized descendants are neither returned nor persisted;
- a failed related-item validation or permission check rolls back the entire
  adjustment; and
- SQLite and PostgreSQL produce the same inclusive civil-date results.

## Non-goals

This decision does not define dependency types, lag, calendars, duration,
constraints, critical-path calculations, named plans, scenario schedules, or
workspace-wide scheduling enablement.
