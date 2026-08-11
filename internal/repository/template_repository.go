package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

// Template-specific validation errors. Handlers map these to client errors
// (422/409); they are distinct from the generic ErrNotFound/ErrDuplicateEntry.
var (
	// ErrMandatoryRequiresOneType is returned when a mandatory template does
	// not target exactly one item type.
	ErrMandatoryRequiresOneType = errors.New("a mandatory template must target exactly one item type")

	// ErrMandatoryConflict is returned when saving an active mandatory template
	// for an item type that already has one.
	ErrMandatoryConflict = errors.New("another active mandatory template already exists for this item type")

	// ErrInvalidTemplateMode is returned for an unrecognized mode value.
	ErrInvalidTemplateMode = errors.New("invalid template mode")
)

// TemplateRepository persists work item templates (item_templates) and their
// optional target item-type filter (item_template_item_types). The mandatory
// application seam in services.CreateItem reads through GetMandatoryForType.
type TemplateRepository struct {
	db database.Database
}

// NewTemplateRepository creates a TemplateRepository.
func NewTemplateRepository(db database.Database) *TemplateRepository {
	return &TemplateRepository{db: db}
}

const templateColumns = "id, workspace_id, name, description_body, mode, is_active, created_by, updated_by, created_at, updated_at"

// ListByWorkspace returns every template in the workspace, ordered by name,
// with each template's target item-type ids populated.
func (r *TemplateRepository) ListByWorkspace(workspaceID int) ([]models.ItemTemplate, error) {
	rows, err := r.db.Query(
		"SELECT "+templateColumns+" FROM item_templates WHERE workspace_id = ? ORDER BY name",
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list templates for workspace %d: %w", workspaceID, err)
	}
	templates, err := scanTemplates(rows)
	if err != nil {
		return nil, err
	}
	if err := r.loadItemTypeIDs(templates); err != nil {
		return nil, err
	}
	return templates, nil
}

// ListForType returns the active templates a creator may pick for the given
// item type: those targeting the type plus untyped (global) templates. The
// mandatory template for the type (if any) is included so callers can flag it.
// Ordered by name.
func (r *TemplateRepository) ListForType(workspaceID, itemTypeID int) ([]models.ItemTemplate, error) {
	rows, err := r.db.Query(`
		SELECT `+prefixedTemplateColumns("t")+`
		FROM item_templates t
		WHERE t.workspace_id = ? AND t.is_active = true
		  AND (
		    NOT EXISTS (SELECT 1 FROM item_template_item_types j WHERE j.template_id = t.id)
		    OR EXISTS (SELECT 1 FROM item_template_item_types j WHERE j.template_id = t.id AND j.item_type_id = ?)
		  )
		ORDER BY t.name
	`, workspaceID, itemTypeID)
	if err != nil {
		return nil, fmt.Errorf("list templates for workspace %d type %d: %w", workspaceID, itemTypeID, err)
	}
	templates, err := scanTemplates(rows)
	if err != nil {
		return nil, err
	}
	if err := r.loadItemTypeIDs(templates); err != nil {
		return nil, err
	}
	return templates, nil
}

// GetByID loads a single template (with its target item-type ids). Returns
// ErrNotFound when missing.
func (r *TemplateRepository) GetByID(id int) (*models.ItemTemplate, error) {
	t, err := r.scanOne(r.db.QueryRow("SELECT "+templateColumns+" FROM item_templates WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	ids, err := r.itemTypeIDsFor(t.ID)
	if err != nil {
		return nil, err
	}
	t.ItemTypeIDs = ids
	return t, nil
}

// NameExistsInWorkspace reports whether a template with the given name already
// exists in the workspace. excludeID > 0 excludes that row (so an Update does
// not collide with itself).
func (r *TemplateRepository) NameExistsInWorkspace(workspaceID int, name string, excludeID int) (bool, error) {
	var count int
	var err error
	if excludeID > 0 {
		err = r.db.QueryRow(
			"SELECT COUNT(*) FROM item_templates WHERE name = ? AND workspace_id = ? AND id != ?",
			name, workspaceID, excludeID,
		).Scan(&count)
	} else {
		err = r.db.QueryRow(
			"SELECT COUNT(*) FROM item_templates WHERE name = ? AND workspace_id = ?",
			name, workspaceID,
		).Scan(&count)
	}
	if err != nil {
		return false, fmt.Errorf("check template name %q in workspace %d: %w", name, workspaceID, err)
	}
	return count > 0, nil
}

// GetMandatoryForType returns the active mandatory template enforced for the
// given (workspace, item_type), or ErrNotFound when none. The service-layer
// invariant guarantees at most one, so LIMIT 1 is exact.
func (r *TemplateRepository) GetMandatoryForType(workspaceID, itemTypeID int) (*models.ItemTemplate, error) {
	row := r.db.QueryRow(`
		SELECT `+prefixedTemplateColumns("t")+`
		FROM item_templates t
		JOIN item_template_item_types j ON j.template_id = t.id
		WHERE t.workspace_id = ? AND t.mode = ? AND t.is_active = true AND j.item_type_id = ?
		ORDER BY t.id
		LIMIT 1
	`, workspaceID, models.TemplateModeMandatory, itemTypeID)
	t, err := r.scanOne(row)
	if err != nil {
		return nil, err
	}
	t.ItemTypeIDs = []int{itemTypeID}
	return t, nil
}

// Create inserts a template and its target item-type rows atomically, after
// enforcing the mandatory invariants. Returns the persisted template.
func (r *TemplateRepository) Create(t *models.ItemTemplate) (*models.ItemTemplate, error) {
	if err := validateMode(t.Mode); err != nil {
		return nil, err
	}
	now := time.Now()
	newID, err := database.WithTxResult(r.db, func(tx database.Tx) (int64, error) {
		if err := enforceMandatoryInvariants(tx, r.db.GetDriverName(), t.WorkspaceID, t.Mode, t.IsActive, t.ItemTypeIDs, 0); err != nil {
			return 0, err
		}
		var id int64
		if err := tx.QueryRow(`
			INSERT INTO item_templates (workspace_id, name, description_body, mode, is_active, created_by, updated_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id
		`, t.WorkspaceID, t.Name, t.DescriptionBody, t.Mode, t.IsActive,
			nullableInt(t.CreatedBy), nullableInt(t.UpdatedBy), now, now).Scan(&id); err != nil {
			if database.IsUniqueConstraintError(err) {
				return 0, ErrDuplicateEntry
			}
			return 0, fmt.Errorf("insert template: %w", err)
		}
		if err := insertItemTypeRows(tx, id, t.ItemTypeIDs); err != nil {
			return 0, err
		}
		return id, nil
	})
	if err != nil {
		return nil, err
	}
	return r.GetByID(int(newID))
}

// Update overwrites a template's fields and replaces its target item-type set
// atomically, after enforcing the mandatory invariants.
func (r *TemplateRepository) Update(t *models.ItemTemplate) error {
	if err := validateMode(t.Mode); err != nil {
		return err
	}
	return database.WithTx(r.db, func(tx database.Tx) error {
		if err := enforceMandatoryInvariants(tx, r.db.GetDriverName(), t.WorkspaceID, t.Mode, t.IsActive, t.ItemTypeIDs, t.ID); err != nil {
			return err
		}
		res, err := tx.Exec(`
			UPDATE item_templates
			SET name = ?, description_body = ?, mode = ?, is_active = ?, updated_by = ?, updated_at = ?
			WHERE id = ?
		`, t.Name, t.DescriptionBody, t.Mode, t.IsActive, nullableInt(t.UpdatedBy), time.Now(), t.ID)
		if err != nil {
			if database.IsUniqueConstraintError(err) {
				return ErrDuplicateEntry
			}
			return fmt.Errorf("update template %d: %w", t.ID, err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec("DELETE FROM item_template_item_types WHERE template_id = ?", t.ID); err != nil {
			return fmt.Errorf("clear template %d types: %w", t.ID, err)
		}
		return insertItemTypeRows(tx, int64(t.ID), t.ItemTypeIDs)
	})
}

// Delete removes a template (cascading item_template_item_types via FK).
func (r *TemplateRepository) Delete(id int) error {
	res, err := r.db.ExecWrite("DELETE FROM item_templates WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete template %d: %w", id, err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// --- helpers ---

func validateMode(mode string) error {
	switch mode {
	case models.TemplateModeSelectable, models.TemplateModeMandatory:
		return nil
	default:
		return ErrInvalidTemplateMode
	}
}

// enforceMandatoryInvariants checks, inside the write transaction, that a
// mandatory template targets exactly one type and that no other active
// mandatory template already targets that type. excludeID skips the template
// being updated. Selectable templates and inactive mandatory drafts are exempt
// from the conflict check.
//
// The "at most one active mandatory per (workspace, item_type)" rule can't be a
// DB unique constraint — the mode/is_active flags live on item_templates while
// the item_type lives on the join table, so no single-table partial index
// covers it (the plan's documented limitation). To keep the count-then-write
// check race-free on Postgres, concurrent writers for the same (workspace,
// item_type) are serialized with a transaction-scoped advisory lock; SQLite
// already serializes writers at the database level so it needs none.
func enforceMandatoryInvariants(tx database.Tx, driver string, workspaceID int, mode string, isActive bool, itemTypeIDs []int, excludeID int) error {
	if mode != models.TemplateModeMandatory {
		return nil
	}
	if len(itemTypeIDs) != 1 {
		return ErrMandatoryRequiresOneType
	}
	if !isActive {
		return nil
	}
	if driver == "postgres" {
		// Single-bigint advisory keyspace (distinct from the two-int32 keyspace
		// used for item-number allocation); key uniquely on (workspace, type).
		lockKey := int64(workspaceID)<<32 | int64(itemTypeIDs[0])
		if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, lockKey); err != nil {
			return fmt.Errorf("acquire mandatory-template lock: %w", err)
		}
	}
	var count int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM item_templates t
		JOIN item_template_item_types j ON j.template_id = t.id
		WHERE t.workspace_id = ? AND t.mode = ? AND t.is_active = true
		  AND j.item_type_id = ? AND t.id != ?
	`, workspaceID, models.TemplateModeMandatory, itemTypeIDs[0], excludeID).Scan(&count); err != nil {
		return fmt.Errorf("check mandatory conflict: %w", err)
	}
	if count > 0 {
		return ErrMandatoryConflict
	}
	return nil
}

func insertItemTypeRows(tx database.Tx, templateID int64, itemTypeIDs []int) error {
	for _, typeID := range itemTypeIDs {
		if _, err := tx.Exec(
			"INSERT INTO item_template_item_types (template_id, item_type_id) VALUES (?, ?)",
			templateID, typeID,
		); err != nil {
			return fmt.Errorf("add type %d to template %d: %w", typeID, templateID, err)
		}
	}
	return nil
}

// loadItemTypeIDs bulk-loads target item-type ids for a slice of templates and
// attaches them to each template's ItemTypeIDs field (avoids an N+1 lookup).
func (r *TemplateRepository) loadItemTypeIDs(templates []models.ItemTemplate) error {
	if len(templates) == 0 {
		return nil
	}
	ids := make([]any, len(templates))
	placeholders := make([]string, len(templates))
	for i, t := range templates {
		ids[i] = t.ID
		placeholders[i] = "?"
	}
	rows, err := r.db.Query(fmt.Sprintf(`
		SELECT template_id, item_type_id FROM item_template_item_types
		WHERE template_id IN (%s) ORDER BY item_type_id
	`, strings.Join(placeholders, ",")), ids...)
	if err != nil {
		return fmt.Errorf("load template item types: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byTemplate := make(map[int][]int)
	for rows.Next() {
		var templateID, typeID int
		if err := rows.Scan(&templateID, &typeID); err != nil {
			return fmt.Errorf("scan template item type: %w", err)
		}
		byTemplate[templateID] = append(byTemplate[templateID], typeID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate template item types: %w", err)
	}
	for i := range templates {
		templates[i].ItemTypeIDs = byTemplate[templates[i].ID]
	}
	return nil
}

func (r *TemplateRepository) itemTypeIDsFor(templateID int) ([]int, error) {
	rows, err := r.db.Query(
		"SELECT item_type_id FROM item_template_item_types WHERE template_id = ? ORDER BY item_type_id",
		templateID,
	)
	if err != nil {
		return nil, fmt.Errorf("load template %d item types: %w", templateID, err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan template item type: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func prefixedTemplateColumns(alias string) string {
	cols := strings.Split(templateColumns, ", ")
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

// scanOne scans a single template row, mapping sql.ErrNoRows to ErrNotFound.
func (r *TemplateRepository) scanOne(row *sql.Row) (*models.ItemTemplate, error) {
	var t models.ItemTemplate
	var createdBy, updatedBy sql.NullInt64
	err := row.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.DescriptionBody, &t.Mode, &t.IsActive,
		&createdBy, &updatedBy, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan template: %w", err)
	}
	t.CreatedBy = nullInt64ToPtr(createdBy)
	t.UpdatedBy = nullInt64ToPtr(updatedBy)
	t.ItemTypeIDs = []int{}
	return &t, nil
}

func scanTemplates(rows *sql.Rows) ([]models.ItemTemplate, error) {
	defer func() { _ = rows.Close() }()
	templates := []models.ItemTemplate{}
	for rows.Next() {
		var t models.ItemTemplate
		var createdBy, updatedBy sql.NullInt64
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.DescriptionBody, &t.Mode, &t.IsActive,
			&createdBy, &updatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		t.CreatedBy = nullInt64ToPtr(createdBy)
		t.UpdatedBy = nullInt64ToPtr(updatedBy)
		t.ItemTypeIDs = []int{}
		templates = append(templates, t)
	}
	return templates, rows.Err()
}

func nullInt64ToPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}
