package database

import (
	"context"
	"errors"
	"sync/atomic"
)

var requestQueryOutcomes struct {
	canceled  atomic.Uint64
	deadlines atomic.Uint64
	errors    atomic.Uint64
}

// RequestQueryOutcomeStats separates client cancellation and local deadline
// expiry from genuine database/server failures on request-owned SQL paths.
type RequestQueryOutcomeStats struct {
	Canceled  uint64 `json:"canceled"`
	Deadlines uint64 `json:"deadlines"`
	Errors    uint64 `json:"errors"`
}

// ObserveRequestQueryError records the terminal outcome of request-owned
// database work. A nil error is deliberately ignored.
func ObserveRequestQueryError(err error) {
	switch {
	case errors.Is(err, context.Canceled):
		requestQueryOutcomes.canceled.Add(1)
	case errors.Is(err, context.DeadlineExceeded):
		requestQueryOutcomes.deadlines.Add(1)
	case err != nil:
		requestQueryOutcomes.errors.Add(1)
	}
}

// RequestQueryStats returns a process-local lifetime snapshot.
func RequestQueryStats() RequestQueryOutcomeStats {
	return RequestQueryOutcomeStats{
		Canceled:  requestQueryOutcomes.canceled.Load(),
		Deadlines: requestQueryOutcomes.deadlines.Load(),
		Errors:    requestQueryOutcomes.errors.Load(),
	}
}
