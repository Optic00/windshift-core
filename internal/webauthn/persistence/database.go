package persistence

import "database/sql"

// Database is the subset of the application database used by WebAuthn
// persistence. The concrete credential and session schemas are selected by
// the fixed constructors in this package, never by a caller-provided value.
type Database interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	ExecWrite(query string, args ...interface{}) (sql.Result, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Query(query string, args ...interface{}) (*sql.Rows, error)
}
