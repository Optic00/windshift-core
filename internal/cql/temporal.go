package cql

import (
	"fmt"
	"strconv"
	"time"
)

const maxDuration = time.Duration(1<<63 - 1)

// CurrentStatusTransitionAtExpr returns the latest history timestamp for the
// current item status. The prefix is an internal SQL alias prefix: "" or
// "inner_".
func CurrentStatusTransitionAtExpr(aliasPrefix string) string {
	itemAlias := aliasPrefix + "i"
	return fmt.Sprintf(`(SELECT ih.changed_at
		FROM item_history ih
		WHERE ih.item_id = %s.id
			AND ih.field_name = 'status_id'
			AND ih.new_value = CAST(%s.status_id AS TEXT)
		ORDER BY ih.changed_at DESC
		LIMIT 1)`, itemAlias, itemAlias)
}

// CurrentCompletedAtExpr returns the virtual completion timestamp expression
// for the current item status. The prefix is an internal SQL alias prefix.
func CurrentCompletedAtExpr(aliasPrefix string) string {
	itemAlias := aliasPrefix + "i"
	categoryAlias := aliasPrefix + "sc"
	return fmt.Sprintf(`(CASE
		WHEN COALESCE(%s.is_completed, FALSE)
		THEN COALESCE(%s, %s.created_at)
		ELSE NULL
	END)`, categoryAlias, CurrentStatusTransitionAtExpr(aliasPrefix), itemAlias)
}

func parseRelativeLiteral(value string) (time.Duration, error) {
	if len(value) < 2 {
		return 0, fmt.Errorf("invalid relative date literal %q", value)
	}

	amountText := value
	sign := int64(1)
	if value[0] == '-' {
		sign = -1
		amountText = value[1:]
	}
	if amountText == "" {
		return 0, fmt.Errorf("relative date literal %q requires an amount and unit", value)
	}

	unit := amountText[len(amountText)-1]
	amountText = amountText[:len(amountText)-1]
	var unitDuration time.Duration
	switch unit {
	case 'd':
		unitDuration = 24 * time.Hour
	case 'h':
		unitDuration = time.Hour
	case 'm':
		unitDuration = time.Minute
	default:
		return 0, fmt.Errorf("invalid relative date literal %q", value)
	}
	if amountText == "" {
		return 0, fmt.Errorf("invalid relative date literal %q", value)
	}

	amount, err := strconv.ParseInt(amountText, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid relative date literal %q: amount must be a whole number", value)
	}
	if amount > int64(maxDuration/unitDuration) {
		return 0, fmt.Errorf("invalid relative date literal %q: duration overflows", value)
	}

	duration := time.Duration(amount) * unitDuration
	if sign < 0 {
		duration = -duration
	}
	return duration, nil
}
