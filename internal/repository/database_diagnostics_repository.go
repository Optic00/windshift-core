package repository

import "windshift/internal/database"

// DatabasePoolStats is a point-in-time view of database/sql pool state.
// Wait and close counters are cumulative since the pool was created.
type DatabasePoolStats struct {
	Driver             string
	MaxOpenConnections int
	OpenConnections    int
	InUse              int
	Idle               int
	WaitCount          int64
	WaitDurationMillis int64
	MaxIdleClosed      int64
	MaxIdleTimeClosed  int64
	MaxLifetimeClosed  int64
}

// DatabaseDiagnosticsRepository exposes database runtime state without leaking
// the database abstraction into HTTP handlers.
type DatabaseDiagnosticsRepository struct {
	db database.Database
}

func NewDatabaseDiagnosticsRepository(db database.Database) *DatabaseDiagnosticsRepository {
	return &DatabaseDiagnosticsRepository{db: db}
}

func (r *DatabaseDiagnosticsRepository) PoolStats() DatabasePoolStats {
	stats := r.db.GetDB().Stats()
	return DatabasePoolStats{
		Driver:             r.db.GetDriverName(),
		MaxOpenConnections: stats.MaxOpenConnections,
		OpenConnections:    stats.OpenConnections,
		InUse:              stats.InUse,
		Idle:               stats.Idle,
		WaitCount:          stats.WaitCount,
		WaitDurationMillis: stats.WaitDuration.Milliseconds(),
		MaxIdleClosed:      stats.MaxIdleClosed,
		MaxIdleTimeClosed:  stats.MaxIdleTimeClosed,
		MaxLifetimeClosed:  stats.MaxLifetimeClosed,
	}
}
