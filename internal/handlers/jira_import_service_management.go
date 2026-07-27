package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/jira"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"

	"github.com/google/uuid"
)

const jiraRequestTypeFieldType = "com.atlassian.servicedesk:vp-origin"

var jiraPortalSlugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

type jiraServiceManagementImport struct {
	ServiceDeskID         string
	ChannelID             int
	RequestTypes          map[string]int
	OrganizationCustomers []JiraUserSummary
	CustomerOrganizations map[string]int
}

type jiraCustomerOrganizationClient interface {
	ListServiceDeskOrganizations(ctx context.Context, serviceDeskID string) ([]jira.JiraServiceDeskOrganization, error)
	ListServiceDeskOrganizationUsers(ctx context.Context, organizationID string) ([]jira.JiraUser, error)
}

func (h *JiraImportHandler) prepareJiraServiceManagementImport(
	ctx context.Context,
	jobID, projectKey string,
	workspaceID int,
	itemTypeMap map[string]int,
	client jira.Client,
	createdByUserID int,
	importOrganizations bool,
) (*jiraServiceManagementImport, error) {
	project, err := client.GetProject(ctx, projectKey)
	if err != nil {
		return nil, fmt.Errorf("load Jira project: %w", err)
	}
	if project == nil || !strings.EqualFold(project.ProjectType, "service_desk") {
		return nil, nil
	}

	serviceDesks, err := client.ListServiceDesks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Jira service desks: %w", err)
	}
	var serviceDesk *jira.JiraServiceDesk
	for i := range serviceDesks {
		if serviceDesks[i].ProjectID == project.ID || strings.EqualFold(serviceDesks[i].ProjectKey, project.Key) {
			serviceDesk = &serviceDesks[i]
			break
		}
	}
	if serviceDesk == nil {
		return nil, fmt.Errorf("no Jira service desk found for project %s", projectKey)
	}

	channelID, err := h.ensureJiraPortal(ctx, jobID, *project, *serviceDesk, workspaceID, createdByUserID)
	if err != nil {
		return nil, err
	}
	requestTypes, err := client.ListServiceDeskRequestTypes(ctx, serviceDesk.ID)
	if err != nil {
		return nil, fmt.Errorf("list Jira service desk request types: %w", err)
	}
	requestTypeMap, err := h.ensureJiraRequestTypes(jobID, channelID, workspaceID, requestTypes, itemTypeMap)
	if err != nil {
		return nil, err
	}
	if err := h.ensureJiraPortalRequestTypeSection(channelID, requestTypeMap); err != nil {
		return nil, err
	}

	organizationCustomers, customerOrganizations, err := h.ensureJiraCustomerOrganizations(
		ctx, jobID, project.Key, serviceDesk.ID, client, importOrganizations,
	)
	if err != nil {
		return nil, err
	}

	return &jiraServiceManagementImport{
		ServiceDeskID:         serviceDesk.ID,
		ChannelID:             channelID,
		RequestTypes:          requestTypeMap,
		OrganizationCustomers: organizationCustomers,
		CustomerOrganizations: customerOrganizations,
	}, nil
}

func (h *JiraImportHandler) ensureJiraPortal(
	ctx context.Context,
	jobID string,
	project jira.JiraProject,
	serviceDesk jira.JiraServiceDesk,
	workspaceID, createdByUserID int,
) (int, error) {
	var mappedID int
	if err := h.db.QueryRow(`
		SELECT windshift_id FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type = 'portal' AND jira_id = ?
	`, jobID, serviceDesk.ID).Scan(&mappedID); err == nil {
		return mappedID, nil
	}

	baseSlug := "jira-" + strings.Trim(jiraPortalSlugUnsafe.ReplaceAllString(strings.ToLower(project.Key), "-"), "-")
	if baseSlug == "jira-" {
		baseSlug = "jira-service-desk"
	}
	slug := baseSlug
	for suffix := 2; ; suffix++ {
		var existingID int
		var rawConfig string
		err := h.db.QueryRow(`
			SELECT id, config FROM channels
			WHERE type = 'portal' AND direction = 'inbound' AND public_slug = ?
		`, slug).Scan(&existingID, &rawConfig)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("find Jira portal slug: %w", err)
		}
		var config models.ChannelConfig
		if json.Unmarshal([]byte(rawConfig), &config) == nil && jiraPortalContainsWorkspace(config.PortalWorkspaceIDs, workspaceID) {
			h.recordMapping(jobID, "portal", serviceDesk.ID, project.Key, existingID, map[string]any{
				"was_created": false,
				"project_id":  project.ID,
			})
			return existingID, nil
		}
		slug = baseSlug + "-" + strconv.Itoa(suffix)
	}

	portalTitle := strings.TrimSpace(project.Name)
	if portalTitle == "" {
		portalTitle = project.Key
	}
	configBytes, err := json.Marshal(models.ChannelConfig{
		PortalSlug:             slug,
		PortalWorkspaceIDs:     []int{workspaceID},
		PortalTitle:            portalTitle,
		PortalDescription:      "Imported from Jira Service Management",
		PortalRegistrationMode: "manual",
	})
	if err != nil {
		return 0, fmt.Errorf("marshal Jira portal config: %w", err)
	}

	channel := &models.Channel{
		Name:        portalTitle,
		Type:        "portal",
		Direction:   "inbound",
		Description: "Imported from Jira Service Management",
		Status:      "enabled",
		Config:      string(configBytes),
	}
	channelID, err := database.WithTxResult(h.db, func(tx database.Tx) (int, error) {
		id, createErr := repository.NewChannelRepository(h.db).Create(ctx, tx, channel)
		if createErr != nil {
			return 0, createErr
		}
		if createdByUserID > 0 {
			if _, createErr = tx.Exec(`
				INSERT INTO channel_managers (channel_id, manager_type, manager_id, added_by, created_at, updated_at)
				VALUES (?, 'user', ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
				ON CONFLICT(channel_id, manager_type, manager_id) DO NOTHING
			`, id, createdByUserID, createdByUserID); createErr != nil {
				return 0, createErr
			}
		}
		return id, nil
	})
	if err != nil {
		return 0, fmt.Errorf("create Jira portal: %w", err)
	}
	h.recordMapping(jobID, "portal", serviceDesk.ID, project.Key, channelID, map[string]any{
		"was_created": true,
		"project_id":  project.ID,
	})
	return channelID, nil
}

func (h *JiraImportHandler) ensureJiraRequestTypes(
	jobID string,
	channelID, workspaceID int,
	requestTypes []jira.JiraServiceDeskRequestType,
	itemTypeMap map[string]int,
) (map[string]int, error) {
	result := make(map[string]int, len(requestTypes))
	repo := repository.NewRequestTypeRepository(h.db)
	for order, requestType := range requestTypes {
		itemTypeID, ok := itemTypeMap[requestType.IssueTypeID]
		if !ok {
			continue
		}

		var existingID int
		err := h.db.QueryRow(`
			SELECT id FROM request_types WHERE channel_id = ? AND name = ?
		`, channelID, requestType.Name).Scan(&existingID)
		if err == nil {
			result[requestType.ID] = existingID
			h.recordMapping(jobID, "request_type", requestType.ID, requestType.Name, existingID, map[string]any{
				"was_created":        false,
				"service_desk_id":    requestType.ServiceDeskID,
				"jira_issue_type_id": requestType.IssueTypeID,
			})
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("find Jira request type %s: %w", requestType.ID, err)
		}

		description := strings.TrimSpace(requestType.Description)
		if description == "" {
			description = strings.TrimSpace(requestType.HelpText)
		}
		id, err := repo.Create(&models.RequestType{
			ChannelID:    channelID,
			Name:         sanitize.PlainTextField.Sanitize(requestType.Name),
			Description:  sanitize.PlainTextField.Sanitize(description),
			ItemTypeID:   itemTypeID,
			Icon:         "LifeBuoy",
			Color:        "#0ea5e9",
			DisplayOrder: order + 1,
			IsActive:     !strings.EqualFold(requestType.RestrictionStatus, "CLOSED"),
			WorkspaceID:  &workspaceID,
		})
		if err != nil {
			return nil, fmt.Errorf("create Jira request type %s: %w", requestType.ID, err)
		}
		requestTypeConfig, _ := json.Marshal(models.RequestTypeConfig{
			RequireAuth:      true,
			AllowAttachments: true,
		})
		if _, err := h.db.ExecWrite(`UPDATE request_types SET config = ? WHERE id = ?`, string(requestTypeConfig), id); err != nil {
			return nil, fmt.Errorf("configure Jira request type %s: %w", requestType.ID, err)
		}
		if _, err := h.db.ExecWrite(`
			INSERT INTO request_type_fields
				(request_type_id, field_identifier, field_type, display_order, is_required, step_number, created_at, updated_at)
			VALUES
				(?, 'title', 'default', 1, true, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
				(?, 'description', 'default', 2, false, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		`, id, id); err != nil {
			return nil, fmt.Errorf("create default fields for Jira request type %s: %w", requestType.ID, err)
		}
		result[requestType.ID] = int(id)
		h.recordMapping(jobID, "request_type", requestType.ID, requestType.Name, int(id), map[string]any{
			"was_created":        true,
			"service_desk_id":    requestType.ServiceDeskID,
			"jira_issue_type_id": requestType.IssueTypeID,
			"jira_group_ids":     requestType.GroupIDs,
		})
	}
	return result, nil
}

func (h *JiraImportHandler) ensureJiraPortalRequestTypeSection(channelID int, requestTypes map[string]int) error {
	var rawConfig string
	if err := h.db.QueryRow(`SELECT config FROM channels WHERE id = ?`, channelID).Scan(&rawConfig); err != nil {
		return fmt.Errorf("load Jira portal config: %w", err)
	}
	var config models.ChannelConfig
	if err := json.Unmarshal([]byte(rawConfig), &config); err != nil {
		return fmt.Errorf("decode Jira portal config: %w", err)
	}

	requestTypeIDs := make([]int, 0, len(requestTypes))
	for _, id := range requestTypes {
		requestTypeIDs = append(requestTypeIDs, id)
	}
	sort.Ints(requestTypeIDs)
	if len(config.PortalSections) == 0 {
		config.PortalSections = []models.PortalSection{{
			ID:             uuid.NewString(),
			Title:          "Requests",
			DisplayOrder:   0,
			RequestTypeIDs: requestTypeIDs,
			AssetReportIDs: []int{},
		}}
	} else {
		seen := make(map[int]struct{}, len(config.PortalSections[0].RequestTypeIDs))
		for _, id := range config.PortalSections[0].RequestTypeIDs {
			seen[id] = struct{}{}
		}
		for _, id := range requestTypeIDs {
			if _, ok := seen[id]; !ok {
				config.PortalSections[0].RequestTypeIDs = append(config.PortalSections[0].RequestTypeIDs, id)
			}
		}
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode Jira portal config: %w", err)
	}
	if _, err := h.db.ExecWrite(`UPDATE channels SET config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(configBytes), channelID); err != nil {
		return fmt.Errorf("update Jira portal request type section: %w", err)
	}
	return nil
}

func jiraIsPortalCustomer(accountID, accountType string) bool {
	return strings.EqualFold(strings.TrimSpace(accountType), "customer") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(accountID)), "qm:")
}

func jiraUserIdentityMetadata(user *jira.JiraUser) map[string]any {
	if user == nil {
		return nil
	}
	identity := map[string]any{}
	if accountID := strings.TrimSpace(user.GetIdentifier()); accountID != "" {
		identity["account_id"] = accountID
	}
	if accountType := strings.TrimSpace(user.AccountType); accountType != "" {
		identity["account_type"] = accountType
	}
	if displayName := strings.TrimSpace(user.DisplayName); displayName != "" {
		identity["display_name"] = displayName
	}
	if email := strings.TrimSpace(user.EmailAddress); email != "" {
		identity["email"] = email
	}
	if len(identity) == 0 {
		return nil
	}
	return identity
}

func splitJiraImportUsers(users []JiraUserSummary) (internalUsers, portalCustomers []JiraUserSummary) {
	for _, user := range users {
		if jiraIsPortalCustomer(user.AccountID, user.AccountType) {
			portalCustomers = append(portalCustomers, user)
		} else {
			internalUsers = append(internalUsers, user)
		}
	}
	return internalUsers, portalCustomers
}

func (h *JiraImportHandler) ensurePortalCustomers(
	jobID string,
	channelID int,
	customers []JiraUserSummary,
	customerOrganizations map[string]int,
) (map[string]int, error) {
	result := make(map[string]int, len(customers))
	for _, customer := range customers {
		if customer.AccountID == "" {
			continue
		}
		var customerID int
		mappingExists := false
		err := h.db.QueryRow(`
			SELECT windshift_id FROM jira_import_id_mappings
			WHERE job_id = ? AND entity_type = 'portal_customer' AND jira_id = ?
		`, jobID, customer.AccountID).Scan(&customerID)
		if err == nil {
			mappingExists = true
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}

		wasCreated := false
		organizationID := customerOrganizations[customer.AccountID]
		if !mappingExists {
			email := strings.TrimSpace(customer.Email)
			if email == "" {
				email = jiraPortalCustomerSyntheticEmail(customer.AccountID)
			}
			err = h.db.QueryRow(`SELECT id FROM portal_customers WHERE LOWER(email) = LOWER(?)`, email).Scan(&customerID)
			if errors.Is(err, sql.ErrNoRows) {
				name := sanitize.PlainTextField.Sanitize(strings.TrimSpace(customer.DisplayName))
				if name == "" {
					name = "Imported Jira Customer"
				}
				var newID int64
				err = h.db.QueryRow(`
					INSERT INTO portal_customers (name, email, customer_organisation_id, created_at, updated_at)
					VALUES (?, ?, NULLIF(?, 0), CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
				`, name, email, organizationID).Scan(&newID)
				customerID = int(newID)
				wasCreated = err == nil
			}
			if err != nil {
				return nil, fmt.Errorf("ensure Jira portal customer: %w", err)
			}
		}

		var previousOrganizationID sql.NullInt64
		if err = h.db.QueryRow(`
			SELECT customer_organisation_id FROM portal_customers WHERE id = ?
		`, customerID).Scan(&previousOrganizationID); err != nil {
			return nil, fmt.Errorf("load Jira portal customer organization: %w", err)
		}
		organizationWasAssigned := false
		if organizationID > 0 && !previousOrganizationID.Valid {
			if _, err = h.db.ExecWrite(`
				UPDATE portal_customers
				SET customer_organisation_id = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ? AND customer_organisation_id IS NULL
			`, organizationID, customerID); err != nil {
				return nil, fmt.Errorf("assign Jira portal customer organization: %w", err)
			}
			organizationWasAssigned = true
		}

		if err := h.ensureJiraPortalCustomerAccess(jobID, customer.AccountID, customerID, channelID); err != nil {
			return nil, err
		}

		result[customer.AccountID] = customerID
		if !mappingExists {
			previousID := 0
			if previousOrganizationID.Valid {
				previousID = int(previousOrganizationID.Int64)
			}
			h.recordMapping(jobID, "portal_customer", customer.AccountID, "", customerID, map[string]any{
				"was_created":                       wasCreated,
				"customer_organisation_id":          organizationID,
				"organization_was_assigned":         organizationWasAssigned,
				"previous_customer_organisation_id": previousID,
			})
		}
	}
	return result, nil
}

func (h *JiraImportHandler) ensureJiraPortalCustomerAccess(
	jobID, accountID string,
	customerID, channelID int,
) error {
	var channelAccessExisted bool
	if err := h.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM portal_customer_channels
			WHERE portal_customer_id = ? AND channel_id = ?
		)
	`, customerID, channelID).Scan(&channelAccessExisted); err != nil {
		return fmt.Errorf("check Jira portal customer channel access: %w", err)
	}
	if _, err := h.db.ExecWrite(`
		INSERT INTO portal_customer_channels (portal_customer_id, channel_id, created_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(portal_customer_id, channel_id) DO NOTHING
	`, customerID, channelID); err != nil {
		return fmt.Errorf("grant Jira portal customer channel access: %w", err)
	}
	h.recordMapping(jobID, "portal_customer_channel", accountID+":"+strconv.Itoa(channelID), "", customerID, map[string]any{
		"was_created": !channelAccessExisted,
		"channel_id":  channelID,
	})

	var roleID int
	if err := h.db.QueryRow(`SELECT id FROM contact_roles WHERE name = 'Portal Customer'`).Scan(&roleID); err != nil {
		return fmt.Errorf("find Jira portal customer role: %w", err)
	}
	var roleExisted bool
	if err := h.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM portal_customer_roles
			WHERE portal_customer_id = ? AND contact_role_id = ?
		)
	`, customerID, roleID).Scan(&roleExisted); err != nil {
		return fmt.Errorf("check Jira portal customer role: %w", err)
	}
	if _, err := h.db.ExecWrite(`
		INSERT INTO portal_customer_roles (portal_customer_id, contact_role_id, created_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(portal_customer_id, contact_role_id) DO NOTHING
	`, customerID, roleID); err != nil {
		return fmt.Errorf("assign Jira portal customer role: %w", err)
	}
	h.recordMapping(jobID, "portal_customer_role", accountID+":"+strconv.Itoa(roleID), "", customerID, map[string]any{
		"was_created":     !roleExisted,
		"contact_role_id": roleID,
	})
	return nil
}

func (h *JiraImportHandler) ensureJiraCustomerOrganizations(
	ctx context.Context,
	jobID, projectKey, serviceDeskID string,
	client jiraCustomerOrganizationClient,
	importOrganizations bool,
) ([]JiraUserSummary, map[string]int, error) {
	organizations, err := client.ListServiceDeskOrganizations(ctx, serviceDeskID)
	if err != nil {
		return nil, nil, fmt.Errorf("list Jira customer organizations: %w", err)
	}
	sort.SliceStable(organizations, func(i, j int) bool {
		if strings.EqualFold(organizations[i].Name, organizations[j].Name) {
			return organizations[i].ID < organizations[j].ID
		}
		return strings.ToLower(organizations[i].Name) < strings.ToLower(organizations[j].Name)
	})

	customersByAccountID := make(map[string]JiraUserSummary)
	customerOrganizations := make(map[string]int)
	repo := repository.NewCustomerOrganisationRepository(h.db)
	for _, organization := range organizations {
		var organizationID int
		if importOrganizations {
			wasCreated := false
			mappingErr := h.db.QueryRow(`
				SELECT windshift_id FROM jira_import_id_mappings
				WHERE job_id = ? AND entity_type = 'customer_organisation' AND jira_id = ?
			`, jobID, organization.ID).Scan(&organizationID)
			if errors.Is(mappingErr, sql.ErrNoRows) {
				mappingErr = h.db.QueryRow(`
					SELECT id FROM customer_organisations WHERE LOWER(name) = LOWER(?)
				`, organization.Name).Scan(&organizationID)
				if errors.Is(mappingErr, sql.ErrNoRows) {
					organizationID, _, mappingErr = repo.Create(&models.CustomerOrganisation{
						Name:        sanitize.PlainTextField.Sanitize(organization.Name),
						Description: "Imported from Jira Service Management",
						Active:      true,
					})
					wasCreated = mappingErr == nil
				}
				if mappingErr == nil {
					h.recordMapping(jobID, "customer_organisation", organization.ID, projectKey, organizationID, map[string]any{
						"was_created":     wasCreated,
						"service_desk_id": serviceDeskID,
						"jira_uuid":       organization.UUID,
						"scim_managed":    organization.SCIMManaged,
					})
				}
			}
			if mappingErr != nil {
				return nil, nil, fmt.Errorf("ensure Jira customer organization %s: %w", organization.ID, mappingErr)
			}
		}

		users, usersErr := client.ListServiceDeskOrganizationUsers(ctx, organization.ID)
		if usersErr != nil {
			return nil, nil, fmt.Errorf("list customers for Jira organization %s: %w", organization.ID, usersErr)
		}
		for _, user := range users {
			accountID := user.GetIdentifier()
			if accountID == "" {
				continue
			}
			if organizationID > 0 {
				if _, assigned := customerOrganizations[accountID]; !assigned {
					customerOrganizations[accountID] = organizationID
				}
			}
			if _, seen := customersByAccountID[accountID]; !seen {
				avatarURL := ""
				if user.AvatarURLs != nil {
					avatarURL = user.AvatarURLs["48x48"]
				}
				customersByAccountID[accountID] = JiraUserSummary{
					AccountID:   accountID,
					AccountType: user.AccountType,
					Email:       user.EmailAddress,
					DisplayName: user.DisplayName,
					AvatarURL:   avatarURL,
				}
			}
		}
	}

	customers := make([]JiraUserSummary, 0, len(customersByAccountID))
	for _, customer := range customersByAccountID {
		customers = append(customers, customer)
	}
	sort.SliceStable(customers, func(i, j int) bool {
		return customers[i].AccountID < customers[j].AccountID
	})
	return customers, customerOrganizations, nil
}

func jiraPortalCustomerSyntheticEmail(accountID string) string {
	safe := strings.ReplaceAll(strings.TrimSpace(accountID), ":", "-")
	if safe == "" {
		safe = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return safe + "@jira-customer.invalid"
}

func jiraRequestTypeID(issue *jira.JiraIssue, mappings []CustomFieldMapping) string {
	if issue == nil {
		return ""
	}
	for _, mapping := range mappings {
		if mapping.JiraType != jiraRequestTypeFieldType && !strings.EqualFold(strings.TrimSpace(mapping.JiraName), "Request Type") {
			continue
		}
		value, ok := issue.Fields.CustomFields[mapping.JiraID].(map[string]interface{})
		if !ok {
			continue
		}
		requestType, _ := value["requestType"].(map[string]interface{})
		if id := firstStringKey(requestType, "id"); id != "" {
			return id
		}
		if id := firstStringKey(value, "requestTypeId", "id"); id != "" {
			return id
		}
	}
	return ""
}

func jiraPortalContainsWorkspace(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
