package repository

import (
	"log/slog"

	"windshift/internal/database"
)

// NameMaps holds id→name lookup tables for the small reference entities whose
// rows are routinely referenced by id in history/audit values. The tables are
// small enough to load in full.
type NameMaps struct {
	Statuses   map[int]string
	Priorities map[int]string
	Milestones map[int]string
	Iterations map[int]string
	Users      map[int]string
}

// LookupRepository loads id→name maps for reference entities. It centralizes the
// "SELECT id, name FROM ..." loads that previously lived inline in callers.
type LookupRepository struct {
	db database.Database
}

// NewLookupRepository creates a new LookupRepository.
func NewLookupRepository(db database.Database) *LookupRepository {
	return &LookupRepository{db: db}
}

// LoadNameMaps loads id→name maps for statuses, priorities, milestones,
// iterations, and users. It never returns an error: a failed individual load is
// logged and yields an empty map for that entity so callers can fall through to
// the raw value. The returned struct and all its maps are always non-nil.
func (r *LookupRepository) LoadNameMaps() *NameMaps {
	m := &NameMaps{
		Statuses:   map[int]string{},
		Priorities: map[int]string{},
		Milestones: map[int]string{},
		Iterations: map[int]string{},
		Users:      map[int]string{},
	}
	r.load("statuses", "SELECT id, name FROM statuses", m.Statuses)
	r.load("priorities", "SELECT id, name FROM priorities", m.Priorities)
	r.load("milestones", "SELECT id, name FROM milestones", m.Milestones)
	r.load("iterations", "SELECT id, name FROM iterations", m.Iterations)
	r.load("users", "SELECT id, COALESCE(first_name || ' ' || last_name, '') FROM users", m.Users)
	return m
}

func (r *LookupRepository) load(name, query string, target map[int]string) {
	rows, err := r.db.Query(query)
	if err != nil {
		slog.Warn("lookup: query failed", slog.String("lookup", name), slog.Any("error", err))
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int
		var nameVal string
		if err := rows.Scan(&id, &nameVal); err != nil {
			continue
		}
		target[id] = nameVal
	}
	if err := rows.Err(); err != nil {
		slog.Warn("lookup: iteration failed", slog.String("lookup", name), slog.Any("error", err))
	}
}
