package database

import "fmt"

// InitializeDurableEventInfrastructure installs the shared journal, delivery,
// cutover, and frozen-target tables on a database that owns its own schema.
func InitializeDurableEventInfrastructure(db Database) error {
	eventSchema := eventsSchema
	targetSchema := actionEventTargetsSchema
	if db.GetDriverName() == "postgres" {
		eventSchema = eventsSchemaPostgres
		targetSchema = actionEventTargetsSchemaPostgres
	}
	if _, err := db.ExecWrite(eventSchema); err != nil {
		return fmt.Errorf("initialize durable event journal: %w", err)
	}
	if _, err := db.ExecWrite(targetSchema); err != nil {
		return fmt.Errorf("initialize durable action targets: %w", err)
	}
	return nil
}
