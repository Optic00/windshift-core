package wscli

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseRelativeDate expands today, week, month, year, or -Nd to ISO ranges.
func parseRelativeDate(input string) (from, to string, err error) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	switch strings.ToLower(input) {
	case "today":
		from = today.Format("2006-01-02")
		to = today.AddDate(0, 0, 1).Format("2006-01-02")
	case "week":
		from = today.AddDate(0, 0, -7).Format("2006-01-02")
		to = today.AddDate(0, 0, 1).Format("2006-01-02")
	case "month":
		from = today.AddDate(0, -1, 0).Format("2006-01-02")
		to = today.AddDate(0, 0, 1).Format("2006-01-02")
	case "year":
		from = today.AddDate(-1, 0, 0).Format("2006-01-02")
		to = today.AddDate(0, 0, 1).Format("2006-01-02")
	default:
		// Parse -Nd ranges.
		if strings.HasPrefix(input, "-") && strings.HasSuffix(strings.ToLower(input), "d") {
			daysStr := input[1 : len(input)-1]
			days, parseErr := strconv.Atoi(daysStr)
			if parseErr != nil {
				return "", "", fmt.Errorf("invalid date format: %s (use today, week, month, year, or -Nd)", input)
			}
			from = today.AddDate(0, 0, -days).Format("2006-01-02")
			to = today.AddDate(0, 0, 1).Format("2006-01-02")
		} else {
			return "", "", fmt.Errorf("invalid date format: %s (use today, week, month, year, or -Nd)", input)
		}
	}
	return from, to, nil
}

// isNegatedFilter checks if a filter value is negated (prefixed with ~)
func isNegatedFilter(value string) bool {
	return strings.HasPrefix(value, "~")
}

// stripNegation removes the ~ prefix from a filter value
func stripNegation(value string) string {
	return strings.TrimPrefix(value, "~")
}
