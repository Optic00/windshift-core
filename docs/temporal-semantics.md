# Temporal semantics

Windshift uses four distinct temporal types. Callers must identify which type
they have before parsing, formatting, comparing, or persisting it.

## Instants

An instant identifies one point on the UTC timeline. API timestamps and Unix
timestamps are instants.

- Persist and exchange instants as UTC timestamps.
- Authenticated surfaces display instants in the acting user's validated IANA
  timezone.
- If a stored user timezone is missing or invalid, display the instant in UTC.
- Public surfaces without an acting user display instants in UTC unless the
  surface explicitly owns another timezone.
- Browser and server local timezones are never implicit presentation defaults.

Frontend code formats instants through `formatInstant` or a formatter returned
by `createTemporalFormatter`. Backend code uses `ResolveTimezoneOrUTC` only at
boundaries where invalid stored user data must safely fall back.

## Date-only values

A date-only value is a Gregorian calendar label in `YYYY-MM-DD` form. Due
dates, iteration dates, leave dates, and custom date fields are date-only unless
their API explicitly says otherwise.

- Do not convert date-only values through browser or user timezones.
- Preserve the stored year, month, and day when formatting.
- Do not infer a midnight instant from a date-only value except at a boundary
  that explicitly converts a civil range to instants.

Frontend code formats these values through `formatDateOnly`.

## Schedule-local civil time

A civil time combines calendar fields with an IANA timezone. Recurrences,
on-call handoffs, and other schedules retain their own timezone even when the
viewer uses a different timezone.

- Validate schedule and request-supplied timezone names strictly.
- Reject nonexistent or ambiguous wall-clock times unless that feature has a
  documented DST policy.
- Do not replace a schedule timezone with the user's display timezone.

Backend code uses `ResolveTimezone`, `ParseCivilDate`, and the feature's civil
clock resolver for these values.

## Civil date ranges

User-facing inclusive date ranges become half-open instant ranges before they
reach timestamp queries:

```text
[start date at 00:00 local, day after end date at 00:00 local)
```

Convert both boundaries to UTC after constructing them in the relevant IANA
timezone. Advance the exclusive boundary with calendar-day arithmetic, not a
24-hour duration, so DST transition days remain correct. Backend code uses
`CivilDateRangeUTC` for this conversion.

## Durations

A duration is elapsed time and has no timezone. Format and compare durations
without calendar conversion. Labels such as "3 hours ago" compare instants but
express the resulting duration.

For worklogs, a duration submitted without explicit start and end clocks is
anchored at the start of the submitted civil date in the resolved worklog
timezone. Cookie, REST, and MCP entry points use this same deterministic rule.
