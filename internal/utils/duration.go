package utils

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseDuration parses friendly time duration strings into a time.Duration.
//
// Supported formats:
//   - "1h", "0.5h"     hours (fractional ok)
//   - "30m", "15m"     minutes (fractional ok)
//   - "1h30m"          combined hours + minutes
//   - "1d", "2d"       days (1d = 8 hours)
//
// Returns an error if the input is empty or doesn't match.
func ParseDuration(input string) (time.Duration, error) {
	input = strings.TrimSpace(strings.ToLower(input))
	if strings.HasSuffix(input, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(input, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid day format: %s", input)
		}
		return durationFromFloat(days, 8*time.Hour, input)
	}

	re := regexp.MustCompile(`(?:(\d+(?:\.\d+)?)h)?(?:(\d+(?:\.\d+)?)m)?`)
	matches := re.FindStringSubmatch(input)
	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid duration format: %s", input)
	}

	var total time.Duration
	if matches[1] != "" {
		hours, err := strconv.ParseFloat(matches[1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid hour format: %s", matches[1])
		}
		d, err := durationFromFloat(hours, time.Hour, matches[1]+"h")
		if err != nil {
			return 0, err
		}
		total, err = addDuration(total, d, input)
		if err != nil {
			return 0, err
		}
	}
	if matches[2] != "" {
		minutes, err := strconv.ParseFloat(matches[2], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid minute format: %s", matches[2])
		}
		d, err := durationFromFloat(minutes, time.Minute, matches[2]+"m")
		if err != nil {
			return 0, err
		}
		total, err = addDuration(total, d, input)
		if err != nil {
			return 0, err
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("no time duration found in: %s", input)
	}
	return total, nil
}

func addDuration(a, b time.Duration, input string) (time.Duration, error) {
	if (b > 0 && a > time.Duration(1<<63-1)-b) || (b < 0 && a < time.Duration(-1<<63)-b) {
		return 0, fmt.Errorf("duration out of range: %s", input)
	}
	return a + b, nil
}

func durationFromFloat(value float64, unit time.Duration, input string) (time.Duration, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("duration out of range: %s", input)
	}
	limit := float64(1<<63-1) / float64(unit)
	if value > limit || value < -limit {
		return 0, fmt.Errorf("duration out of range: %s", input)
	}
	return time.Duration(value * float64(unit)), nil
}
