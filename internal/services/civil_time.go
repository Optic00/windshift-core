package services

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"windshift/internal/database"
)

// ResolveTimezone validates an IANA timezone name. Empty user timezone values
// retain the historical UTC default, but request-supplied values should be
// checked for emptiness before calling this function when they are required.
func ResolveTimezone(name string) (string, *time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "UTC"
	}
	if name == "Local" {
		return "", nil, fmt.Errorf("timezone must be an IANA timezone name")
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return "", nil, fmt.Errorf("invalid IANA timezone %q", name)
	}
	return name, location, nil
}

// ResolveTimezoneOrUTC validates stored display preferences and falls back to
// UTC when legacy data is missing or invalid.
func ResolveTimezoneOrUTC(name string) (string, *time.Location) {
	resolved, location, err := ResolveTimezone(name)
	if err != nil {
		return "UTC", time.UTC
	}
	return resolved, location
}

// LookupUserTimezone returns a validated timezone for the acting user.
func LookupUserTimezone(db database.Database, userID int) (string, error) {
	var timezone sql.NullString
	if err := db.QueryRow("SELECT timezone FROM users WHERE id = ?", userID).Scan(&timezone); err != nil {
		return "", err
	}
	name := "UTC"
	if timezone.Valid && strings.TrimSpace(timezone.String) != "" {
		name = timezone.String
	}
	name, _ = ResolveTimezoneOrUTC(name)
	return name, nil
}

// ParseCivilDate parses a calendar date in location without changing the
// calendar components. The returned value is for civil-time calculations;
// persist WorklogDateUnix(date), not date.Unix().
func ParseCivilDate(value string, location *time.Location) (time.Time, error) {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date format, use YYYY-MM-DD")
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, location), nil
}

// CivilDateRangeUTC converts an inclusive local date range to half-open UTC
// bounds. Calendar-day arithmetic preserves 23-hour and 25-hour DST days.
func CivilDateRangeUTC(startValue, endValue string, location *time.Location) (startUTC, endExclusiveUTC time.Time, err error) {
	if location == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("timezone location is required")
	}

	start, err := ParseCivilDate(startValue, location)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start date: %w", err)
	}
	end, err := ParseCivilDate(endValue, location)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end date: %w", err)
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end date must not precede start date")
	}

	return start.UTC(), end.AddDate(0, 0, 1).UTC(), nil
}

// WorklogDateUnix encodes a business date as its established UTC-midnight key.
func WorklogDateUnix(date time.Time) int64 {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC).Unix()
}

// ResolveCivilClock resolves an exact wall clock in date's location. It
// rejects DST gaps and folds instead of allowing time.Date to silently
// normalize or arbitrarily choose an occurrence.
func ResolveCivilClock(date time.Time, clock string) (time.Time, error) {
	parsed, err := time.Parse("15:04", clock)
	if err != nil {
		return time.Time{}, fmt.Errorf("time must be in HH:MM format")
	}
	candidates := civilTimeCandidates(date.Year(), date.Month(), date.Day(), parsed.Hour(), parsed.Minute(), date.Location())
	switch len(candidates) {
	case 0:
		return time.Time{}, fmt.Errorf("time %s does not exist on %s in timezone %s", clock, date.Format(time.DateOnly), date.Location())
	case 1:
		return candidates[0], nil
	default:
		return time.Time{}, fmt.Errorf("time %s is ambiguous on %s in timezone %s", clock, date.Format(time.DateOnly), date.Location())
	}
}

// onCallHandoffBoundary implements the on-call DST policy: the earliest
// occurrence wins in a fold, and a gap advances to the first valid minute.
func onCallHandoffBoundary(date time.Time, clock string, location *time.Location) (time.Time, error) {
	parsed, err := time.Parse("15:04", clock)
	if err != nil {
		return time.Time{}, err
	}
	wall := time.Date(date.Year(), date.Month(), date.Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
	for minute := 0; minute <= 180; minute++ {
		candidateWall := wall.Add(time.Duration(minute) * time.Minute)
		if candidateWall.Year() != date.Year() || candidateWall.Month() != date.Month() || candidateWall.Day() != date.Day() {
			break
		}
		candidates := civilTimeCandidates(candidateWall.Year(), candidateWall.Month(), candidateWall.Day(), candidateWall.Hour(), candidateWall.Minute(), location)
		if len(candidates) > 0 {
			return candidates[0], nil
		}
	}
	return time.Time{}, fmt.Errorf("could not resolve handoff time %s on %s", clock, date.Format(time.DateOnly))
}

func civilTimeCandidates(year int, month time.Month, day, hour, minute int, location *time.Location) []time.Time {
	wallUTC := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	offsets := make(map[int]struct{})
	for delta := -48; delta <= 48; delta += 6 {
		_, offset := wallUTC.Add(time.Duration(delta) * time.Hour).In(location).Zone()
		offsets[offset] = struct{}{}
	}

	seen := make(map[int64]struct{})
	var candidates []time.Time
	for offset := range offsets {
		candidate := wallUTC.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(location)
		if local.Year() != year || local.Month() != month || local.Day() != day || local.Hour() != hour || local.Minute() != minute {
			continue
		}
		if _, ok := seen[candidate.Unix()]; ok {
			continue
		}
		seen[candidate.Unix()] = struct{}{}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	return candidates
}
