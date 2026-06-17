package repository

import (
	"database/sql"
	"errors"
	"fmt"
)

// Common repository errors
var (
	// ErrNotFound is returned when a requested entity does not exist
	ErrNotFound = errors.New("entity not found")

	// ErrInvalidInput is returned when input validation fails
	ErrInvalidInput = errors.New("invalid input")

	// ErrDuplicateEntry is returned when a unique constraint is violated
	ErrDuplicateEntry = errors.New("duplicate entry")
)

// notFoundOrWrap maps a row-scan error to a repository sentinel: sql.ErrNoRows
// becomes ErrNotFound, and any other error is wrapped with context. It collapses
// the repeated two-branch idiom
//
//	if errors.Is(err, sql.ErrNoRows) {
//		return ErrNotFound
//	}
//	if err != nil {
//		return fmt.Errorf("context: %w", err)
//	}
//
// into a single call inside the caller's `if err != nil` guard. Only use it where
// a missing row genuinely means "not found"; callers that treat absence as a soft
// nil/zero (returning nil, nil) must keep their own branch.
func notFoundOrWrap(err error, context string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", context, err)
}
