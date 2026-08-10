package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"windshift/internal/database"
)

type PortalCustomerRepository struct {
	db database.Database
}

func NewPortalCustomerRepository(db database.Database) *PortalCustomerRepository {
	return &PortalCustomerRepository{db: db}
}

func (r *PortalCustomerRepository) FindIDByEmail(email string) (int, error) {
	var id int
	err := r.db.QueryRow(
		"SELECT id FROM portal_customers WHERE LOWER(email) = LOWER(?)",
		email,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("find portal customer by email: %w", err)
	}
	return id, nil
}

func (r *PortalCustomerRepository) Create(name, email string, organisationID int) (int, error) {
	var id int
	err := r.db.QueryRow(`
		INSERT INTO portal_customers
			(name, email, customer_organisation_id, created_at, updated_at)
		VALUES (?, ?, NULLIF(?, 0), ?, ?) RETURNING id
	`, name, email, organisationID, time.Now(), time.Now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create portal customer: %w", err)
	}
	return id, nil
}

func (r *PortalCustomerRepository) OrganisationID(customerID int) (*int, error) {
	var id sql.NullInt64
	err := r.db.QueryRow(
		"SELECT customer_organisation_id FROM portal_customers WHERE id = ?",
		customerID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get portal customer organisation: %w", err)
	}
	if !id.Valid {
		return nil, nil
	}
	value := int(id.Int64)
	return &value, nil
}

func (r *PortalCustomerRepository) AssignOrganisationIfUnset(customerID, organisationID int) (bool, error) {
	result, err := r.db.ExecWrite(`
		UPDATE portal_customers
		SET customer_organisation_id = ?, updated_at = ?
		WHERE id = ? AND customer_organisation_id IS NULL
	`, organisationID, time.Now(), customerID)
	if err != nil {
		return false, fmt.Errorf("assign portal customer organisation: %w", err)
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}

func (r *PortalCustomerRepository) EnsureChannelAccess(customerID, channelID int) (bool, error) {
	var existed bool
	if err := r.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM portal_customer_channels
			WHERE portal_customer_id = ? AND channel_id = ?
		)
	`, customerID, channelID).Scan(&existed); err != nil {
		return false, fmt.Errorf("check portal customer channel access: %w", err)
	}
	if _, err := r.db.ExecWrite(`
		INSERT INTO portal_customer_channels (portal_customer_id, channel_id, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(portal_customer_id, channel_id) DO NOTHING
	`, customerID, channelID, time.Now()); err != nil {
		return false, fmt.Errorf("grant portal customer channel access: %w", err)
	}
	return !existed, nil
}

func (r *PortalCustomerRepository) EnsureRole(customerID int, roleName string) (roleID int, created bool, err error) {
	if err = r.db.QueryRow(
		"SELECT id FROM contact_roles WHERE name = ?",
		roleName,
	).Scan(&roleID); err != nil {
		err = fmt.Errorf("find portal customer role: %w", err)
		return
	}
	var existed bool
	if err = r.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM portal_customer_roles
			WHERE portal_customer_id = ? AND contact_role_id = ?
		)
	`, customerID, roleID).Scan(&existed); err != nil {
		err = fmt.Errorf("check portal customer role: %w", err)
		return
	}
	if _, err = r.db.ExecWrite(`
		INSERT INTO portal_customer_roles (portal_customer_id, contact_role_id, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(portal_customer_id, contact_role_id) DO NOTHING
	`, customerID, roleID, time.Now()); err != nil {
		err = fmt.Errorf("assign portal customer role: %w", err)
		return
	}
	return roleID, !existed, nil
}
