package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"windshift/internal/jira"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// executeImport runs the actual import process in the background
func (h *JiraImportHandler) executeImport(jobID string, req StartImportRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	// Update job status to running
	h.updateJobStatus(jobID, "running", "initializing", nil, "")

	// Look up the user who initiated this job so imported workspaces can grant
	// them admin access. Without this the importer would create workspaces with
	// no user_workspace_roles rows, making them invisible to non-system-admins.
	var createdBy sql.NullInt64
	if err := h.db.QueryRow(`SELECT created_by FROM jira_import_jobs WHERE id = ?`, jobID).Scan(&createdBy); err != nil {
		slog.Warn("Failed to look up job creator", slog.String("component", "jira"), slog.String("job_id", jobID), slog.Any("error", err))
	}

	// Get the Jira client
	client, err := h.getClientForConnection(ctx, req.ConnectionID)
	if err != nil {
		h.updateJobStatus(jobID, "failed", "", nil, fmt.Sprintf("Failed to connect to Jira: %v", err))
		return
	}

	// When JIRA_CAPTURE_PAYLOADS is configured, save the request and wrap the client
	captureDir := h.capturePayloadsDir
	if captureDir != "" {
		if err := os.MkdirAll(captureDir, 0o750); err != nil { //nolint:gosec // path from server operator env var JIRA_CAPTURE_PAYLOADS
			slog.Error("Failed to create capture directory", slog.String("component", "jira"), slog.Any("error", err))
		} else {
			// Save import_request.json
			capturedReq := req
			capturedReq.Xray.ClientSecret = ""
			reqData, _ := json.MarshalIndent(capturedReq, "", "  ")
			if err := os.WriteFile(captureDir+"/import_request.json", reqData, 0o600); err != nil { //nolint:gosec // G703: captureDir from server operator env var
				slog.Error("Failed to save import request", slog.String("component", "jira"), slog.Any("error", err))
			}

			// Wrap client in recording client
			rc := newRecordingClient(client, captureDir)
			client = rc

			// Save responses + post-import windshift snapshot when import
			// completes (deferred so partial/failed runs still get a snapshot —
			// that's the diff signal we want).
			defer func() {
				if err := rc.saveToFile(captureDir); err != nil {
					slog.Error("Failed to save captured payloads", slog.String("component", "jira"), slog.Any("error", err))
				}
				if err := services.WriteWindshiftExport(h.db, jobID, captureDir); err != nil {
					slog.Error("Failed to save windshift export", slog.String("component", "jira"), slog.Any("error", err))
				}
			}()
		}
	}

	createdByID := 0
	if createdBy.Valid {
		createdByID = int(createdBy.Int64)
	}
	h.executeImportWithClientContext(ctx, jobID, req, client, createdByID)
}

// executeImportWithClient runs the import using the provided Jira client.
// Extracted from executeImport to allow testing with a mock client.
// createdByUserID is the ID of the user who initiated the import (0 if unknown),
// used to grant workspace admin access on imported workspaces.
//
//nolint:unused // Kept for importer tests that inject a mock Jira client.
func (h *JiraImportHandler) executeImportWithClient(jobID string, req StartImportRequest, client jira.Client, createdByUserID int) {
	h.executeImportWithClientContext(context.Background(), jobID, req, client, createdByUserID)
}

func (h *JiraImportHandler) executeImportWithClientContext(ctx context.Context, jobID string, req StartImportRequest, client jira.Client, createdByUserID int) {
	progress := &ImportProgress{
		Phase:         "initializing",
		TotalProjects: len(req.ProjectKeys),
	}

	// Calculate total issues
	for _, projectKey := range req.ProjectKeys {
		for _, ws := range req.Mappings.Workspaces {
			if ws.JiraKey == projectKey {
				progress.TotalIssues += ws.IssueCount
				break
			}
		}
	}

	xrayPlan, err := prepareXrayImport(ctx, client, req.Xray, req.ProjectKeys, req.OpenIssuesOnly)
	if err != nil {
		h.updateJobStatus(jobID, "failed", "", progress, fmt.Sprintf("Failed to prepare Xray import: %v", err))
		return
	}
	if xrayPlan != nil {
		progress.TotalTests = xrayPlan.total
		progress.TotalIssues -= xrayPlan.total
		if progress.TotalIssues < 0 {
			progress.TotalIssues = 0
		}
	}

	// Create statuses and item types once (global model - shared across all workspaces)
	statusMap, err := h.ensureStatuses(ctx, jobID, req.Mappings.Statuses)
	if err != nil {
		slog.Error("Failed to ensure statuses", slog.String("component", "jira"), slog.Any("error", err))
	}

	itemTypeMap, err := h.ensureItemTypes(ctx, jobID, req.Mappings.IssueTypes)
	if err != nil {
		slog.Error("Failed to ensure item types", slog.String("component", "jira"), slog.Any("error", err))
	}

	h.importJiraAssets(ctx, jobID, client, createdByUserID)

	issueKeysByProject, assetFieldSetIDs, assetFieldsByProject := h.preflightJiraAssetFields(
		ctx, jobID, client, req.ProjectKeys, req.OpenIssuesOnly, req.Mappings.CustomFields,
	)
	customFieldIDMap, err := h.ensureCustomFields(ctx, jobID, req.Mappings.CustomFields, assetFieldSetIDs)
	if err != nil {
		slog.Error("Failed to ensure custom fields", slog.String("component", "jira"), slog.Any("error", err))
		customFieldIDMap = make(map[string]int)
	}
	affectsVersionField, err := h.ensureAffectsVersionCustomField(ctx, jobID, req.Mappings.Versions)
	if err != nil {
		slog.Error("Failed to ensure Affects Version custom field", slog.String("component", "jira"), slog.Any("error", err))
	}

	// Process each project
	for i, projectKey := range req.ProjectKeys {
		progress.CurrentProject = projectKey
		progress.Phase = "importing_project"
		h.updateJobProgress(jobID, progress)

		// Find the workspace mapping for this project
		var wsMapping *WorkspaceMapping
		for j := range req.Mappings.Workspaces {
			if req.Mappings.Workspaces[j].JiraKey == projectKey {
				wsMapping = &req.Mappings.Workspaces[j]
				break
			}
		}
		if wsMapping == nil {
			slog.Warn("No workspace mapping found for project", slog.String("component", "jira"), slog.String("project", projectKey))
			continue
		}

		// Create or use existing workspace
		workspaceID, err := h.ensureWorkspace(ctx, jobID, wsMapping, createdByUserID)
		if err != nil {
			slog.Error("Failed to ensure workspace", slog.String("component", "jira"), slog.String("project", projectKey), slog.Any("error", err))
			continue
		}

		jsmImport, err := h.prepareJiraServiceManagementImport(
			ctx, jobID, projectKey, workspaceID, itemTypeMap, client, createdByUserID,
			req.Mappings.ServiceManagement.ImportOrganizations,
		)
		if err != nil {
			slog.Error("Failed to prepare Jira Service Management portal",
				slog.String("component", "jira"),
				slog.String("project", projectKey),
				slog.Any("error", err))
			continue
		}

		// Create workflows and configuration set for this project
		if err = h.ensureWorkflowsAndConfigSet(ctx, jobID, projectKey, workspaceID, statusMap, itemTypeMap, client); err != nil {
			slog.Error("Failed to create workflows/config set", slog.String("component", "jira"), slog.String("project", projectKey), slog.Any("error", err))
			// Non-fatal: continue importing
		}
		if err = h.bindJiraAssetFieldsToWorkspace(
			workspaceID, projectKey, customFieldIDMap, req.Mappings.CustomFields, assetFieldsByProject[projectKey],
		); err != nil {
			slog.Error("Failed to bind Jira Assets fields to workspace screens",
				slog.String("component", "jira"),
				slog.String("project", projectKey),
				slog.Any("error", err))
		}

		// Create milestones from version mappings for this project
		var projectVersionMappings []VersionMapping
		for _, vm := range req.Mappings.Versions {
			if vm.ProjectKey == projectKey {
				projectVersionMappings = append(projectVersionMappings, vm)
			}
		}
		versionMap, err := h.ensureMilestones(ctx, jobID, workspaceID, projectVersionMappings)
		if err != nil {
			slog.Error("Failed to ensure milestones", slog.String("component", "jira"), slog.String("project", projectKey), slog.Any("error", err))
		}

		iterationMap, err := h.ensureJiraIterations(ctx, jobID, workspaceID, projectKey, client)
		if err != nil {
			slog.Error("Failed to ensure Jira iterations", slog.String("component", "jira"), slog.String("project", projectKey), slog.Any("error", err))
			iterationMap = make(map[string]int)
		}

		h.importJiraBoardsAndFilters(ctx, jobID, projectKey, workspaceID, statusMap, client, createdByUserID)

		timeProjectID, err := h.ensureJiraTimeProject(jobID, workspaceID, projectKey, wsMapping.NewWorkspaceName)
		if err != nil {
			slog.Error("Failed to ensure Jira time project", slog.String("component", "jira"), slog.String("project", projectKey), slog.Any("error", err))
			timeProjectID = nil
		}

		// Import issues for this project
		jql := fmt.Sprintf("project = %s ORDER BY created ASC", projectKey)
		if req.OpenIssuesOnly {
			jql = fmt.Sprintf("project = %s AND statusCategory != Done ORDER BY created ASC", projectKey)
		}

		issueKeys, prefetched := issueKeysByProject[projectKey]
		if !prefetched {
			issueKeys, err = client.GetAllIssueKeys(ctx, jql)
			if err != nil {
				slog.Error("Failed to get issue keys", slog.String("component", "jira"), slog.String("project", projectKey), slog.Any("error", err))
				continue
			}
		}

		// Fetch and import issues in batches
		// Track user map across all batches for this project. usernameMap holds
		// the same accountID keys mapped to Windshift usernames so the ADF
		// converter can render @mentions as `@<username>` rather than display
		// text — letting MentionService pick them up via its standard regex.
		userMap := make(map[string]int)
		usernameMap := make(map[string]string)
		portalCustomerMap := make(map[string]int)
		if jsmImport != nil && len(jsmImport.OrganizationCustomers) > 0 {
			organizationMappings, organizationErr := h.ensurePortalCustomers(
				jobID, jsmImport.ChannelID, jsmImport.OrganizationCustomers, jsmImport.CustomerOrganizations,
			)
			if organizationErr != nil {
				slog.Error("Failed to ensure Jira organization customers", slog.String("component", "jira"), slog.Any("error", organizationErr))
			}
			for accountID, customerID := range organizationMappings {
				portalCustomerMap[accountID] = customerID
			}
		}

		batchSize := 100
		for j := 0; j < len(issueKeys); j += batchSize {
			end := j + batchSize
			if end > len(issueKeys) {
				end = len(issueKeys)
			}
			batch := issueKeys[j:end]

			// Bulk fetch issues
			fetchResult, err := client.BulkFetchIssues(ctx, jira.BulkFetchRequest{
				IssueIdsOrKeys: batch,
				Fields:         []string{"*all"},
				Expand:         []string{"renderedFields"},
			})
			if err != nil {
				slog.Error("Failed to fetch issues batch", slog.String("component", "jira"), slog.Any("error", err))
				for _, issueKey := range batch {
					if xrayPlan.isTest(projectKey, issueKey) {
						progress.FailedTests++
					} else {
						progress.FailedIssues++
					}
				}
				continue
			}

			xrayDefinitions, xrayDefinitionErrors := xrayPlan.definitions(ctx, projectKey, fetchResult.Issues)

			// Complete paginated issue subresources before collecting users/importing
			// rows. Jira embeds only the first comment/worklog page in issue payloads;
			// fetching the rest here lets author mapping include every referenced user.
			for idx := range fetchResult.Issues {
				if xrayPlan.isTest(projectKey, fetchResult.Issues[idx].Key) {
					continue
				}
				if err := h.completePagedIssueContainers(ctx, &fetchResult.Issues[idx], client); err != nil {
					slog.Warn("Failed to complete paged Jira issue containers",
						slog.String("component", "jira"),
						slog.String("issue", fetchResult.Issues[idx].Key),
						slog.Any("error", err))
				}
				if jsmImport != nil {
					if err := h.annotateJiraServiceDeskCommentVisibility(ctx, &fetchResult.Issues[idx], client); err != nil {
						slog.Warn("Failed to load Jira Service Management comment visibility",
							slog.String("component", "jira"),
							slog.String("issue", fetchResult.Issues[idx].Key),
							slog.Any("error", err))
					}
				}
			}

			// Collect users from this batch
			var usersToProcess []JiraUserSummary
			usersSeen := make(map[string]bool)
			knownIdentityMap := make(map[string]int, len(userMap)+len(portalCustomerMap))
			for accountID, userID := range userMap {
				knownIdentityMap[accountID] = userID
			}
			for accountID := range portalCustomerMap {
				knownIdentityMap[accountID] = 0
			}
			for _, issue := range fetchResult.Issues {
				if xrayPlan.isTest(projectKey, issue.Key) {
					continue
				}
				// Collect every first-class user reference that can be written during
				// issue import. If we only pre-collect assignee/reporter, creator,
				// comment author, update author, and attachment uploader references
				// degrade to nil or the shared fallback user even though Jira supplied
				// enough identity data in the issue payload.
				addJiraUserSummaryFromUser(issue.Fields.Assignee, knownIdentityMap, &usersToProcess, usersSeen)
				addJiraUserSummaryFromUser(issue.Fields.Reporter, knownIdentityMap, &usersToProcess, usersSeen)
				addJiraUserSummaryFromUser(issue.Fields.Creator, knownIdentityMap, &usersToProcess, usersSeen)
				collectUsersFromADF(issue.Fields.Description, knownIdentityMap, &usersToProcess, usersSeen)
				if issue.Fields.Comment != nil {
					for _, comment := range issue.Fields.Comment.Comments {
						addJiraUserSummaryFromUser(comment.Author, knownIdentityMap, &usersToProcess, usersSeen)
						addJiraUserSummaryFromUser(comment.UpdateAuthor, knownIdentityMap, &usersToProcess, usersSeen)
						collectUsersFromADF(comment.Body, knownIdentityMap, &usersToProcess, usersSeen)
					}
				}
				for _, attachment := range issue.Fields.Attachment {
					addJiraUserSummaryFromUser(attachment.Author, knownIdentityMap, &usersToProcess, usersSeen)
				}
				if issue.Fields.Worklog != nil {
					for _, worklog := range issue.Fields.Worklog.Worklogs {
						addJiraUserSummaryFromUser(worklog.Author, knownIdentityMap, &usersToProcess, usersSeen)
						collectUsersFromADF(worklog.Comment, knownIdentityMap, &usersToProcess, usersSeen)
					}
				}
				for _, value := range issue.Fields.CustomFields {
					collectUsersFromADF(value, knownIdentityMap, &usersToProcess, usersSeen)
				}

				// Collect users from custom user fields (single and multi-user pickers)
				for _, mapping := range req.Mappings.CustomFields {
					if mapping.WindshiftType != "user" && mapping.WindshiftType != "multi_user" {
						continue
					}
					if mapping.Action == "skip" {
						continue
					}

					value, exists := issue.Fields.CustomFields[mapping.JiraID]
					if !exists || value == nil {
						continue
					}

					collectUsersFromCustomField(value, mapping.WindshiftType, knownIdentityMap, &usersToProcess, usersSeen)
				}
			}

			// Ensure users are created/matched
			if len(usersToProcess) > 0 {
				internalUsers := usersToProcess
				var portalCustomers []JiraUserSummary
				if jsmImport != nil {
					internalUsers, portalCustomers = splitJiraImportUsers(usersToProcess)
				}
				newUserMappings, newUsernameMappings, err := h.ensureUsers(ctx, jobID, internalUsers, client)
				if err != nil {
					slog.Error("Failed to ensure users", slog.String("component", "jira"), slog.Any("error", err))
				}
				// Merge new mappings into userMap and usernameMap
				for k, v := range newUserMappings {
					userMap[k] = v
				}
				for k, v := range newUsernameMappings {
					usernameMap[k] = v
				}
				if len(portalCustomers) > 0 {
					newPortalMappings, portalErr := h.ensurePortalCustomers(
						jobID, jsmImport.ChannelID, portalCustomers, jsmImport.CustomerOrganizations,
					)
					if portalErr != nil {
						slog.Error("Failed to ensure Jira portal customers", slog.String("component", "jira"), slog.Any("error", portalErr))
					}
					for k, v := range newPortalMappings {
						portalCustomerMap[k] = v
					}
				}
			}

			// Import each issue
			for _, issue := range fetchResult.Issues {
				if xrayPlan.isTest(projectKey, issue.Key) {
					if definitionErr, failed := xrayDefinitionErrors[issue.Key]; failed {
						slog.Error("Failed to load Xray Test definition",
							slog.String("component", "jira"),
							slog.String("issue", issue.Key),
							slog.Any("error", definitionErr))
						progress.FailedTests++
						continue
					}
					definition, exists := xrayDefinitions[issue.ID]
					if !exists {
						slog.Error("Missing Xray Test definition",
							slog.String("component", "jira"),
							slog.String("issue", issue.Key))
						progress.FailedTests++
						continue
					}
					if _, err := h.importXrayTestCase(jobID, workspaceID, &issue, definition); err != nil {
						slog.Error("Failed to import Xray Test",
							slog.String("component", "jira"),
							slog.String("issue", issue.Key),
							slog.Any("error", err))
						progress.FailedTests++
					} else {
						progress.ImportedTests++
					}
					continue
				}
				err := h.importIssue(ctx, jobID, workspaceID, &issue, statusMap, itemTypeMap, userMap, usernameMap, portalCustomerMap, versionMap, iterationMap, customFieldIDMap, timeProjectID, affectsVersionField, req.Mappings.CustomFields, jsmImport, client, progress)
				if err != nil {
					slog.Error("Failed to import issue", slog.String("component", "jira"), slog.String("issue", issue.Key), slog.Any("error", err))
					progress.FailedIssues++
				} else {
					progress.ImportedIssues++
				}
			}

			h.updateJobProgress(jobID, progress)
		}

		// After all issues imported for this project, link parents
		h.linkParents(jobID)

		// After all issues imported for this project, import issue links
		h.importIssueLinks(jobID)

		progress.CompletedProjects = i + 1
	}

	// Mark job as completed
	progress.Phase = "completed"
	h.updateJobStatus(jobID, "completed", "completed", progress, "")
}

// preflightJiraAssetFields resolves the Jira custom-field → Assets schema
// relationship from all populated issues before any work item is written.
// Jira permits one Assets schema per custom field while allowing that field on
// multiple projects, so this resolution is global by Jira field ID rather than
// per project. All source calls are read-only searches.
func (h *JiraImportHandler) preflightJiraAssetFields(
	ctx context.Context,
	jobID string,
	client jira.Client,
	projectKeys []string,
	openIssuesOnly bool,
	mappings []CustomFieldMapping,
) (
	issueKeysByProject map[string][]string,
	resolved map[string]int,
	assetFieldsByProject map[string]map[string]bool,
) {
	issueKeysByProject = make(map[string][]string, len(projectKeys))
	assetFieldsByProject = make(map[string]map[string]bool, len(projectKeys))
	assetMappings := make([]CustomFieldMapping, 0)
	assetFieldIDs := make([]string, 0)
	for _, mapping := range mappings {
		if mapping.Action == "skip" || mapping.WindshiftType != string(jira.FieldTypeAsset) {
			continue
		}
		assetMappings = append(assetMappings, mapping)
		assetFieldIDs = append(assetFieldIDs, mapping.JiraID)
	}

	for _, projectKey := range projectKeys {
		project, projectErr := client.GetProject(ctx, projectKey)
		if projectErr == nil && project != nil && !project.Simplified && project.Style != "next-gen" {
			if fields, fieldsErr := client.GetProjectFields(ctx, []string{project.ID}); fieldsErr == nil {
				for _, field := range fields {
					for _, mapping := range assetMappings {
						if field.ID == mapping.JiraID {
							if assetFieldsByProject[projectKey] == nil {
								assetFieldsByProject[projectKey] = make(map[string]bool)
							}
							assetFieldsByProject[projectKey][mapping.JiraID] = true
						}
					}
				}
			}
		}
		jql := fmt.Sprintf("project = %s ORDER BY created ASC", projectKey)
		if openIssuesOnly {
			jql = fmt.Sprintf("project = %s AND statusCategory != Done ORDER BY created ASC", projectKey)
		}
		keys, err := client.GetAllIssueKeys(ctx, jql)
		if err != nil {
			slog.Warn("Jira Assets preflight could not list issue keys",
				slog.String("component", "jira"),
				slog.String("project", projectKey),
				slog.Any("error", err))
			continue
		}
		issueKeysByProject[projectKey] = keys
	}
	if len(assetMappings) == 0 {
		return issueKeysByProject, map[string]int{}, assetFieldsByProject
	}

	setCandidates := make(map[string]map[int]struct{}, len(assetMappings))
	for projectKey, keys := range issueKeysByProject {
		for start := 0; start < len(keys); start += 100 {
			end := start + 100
			if end > len(keys) {
				end = len(keys)
			}
			result, err := client.BulkFetchIssues(ctx, jira.BulkFetchRequest{
				IssueIdsOrKeys: keys[start:end],
				Fields:         assetFieldIDs,
			})
			if err != nil {
				slog.Warn("Jira Assets preflight could not read issue fields",
					slog.String("component", "jira"),
					slog.Any("error", err))
				continue
			}
			for _, issue := range result.Issues {
				for _, mapping := range assetMappings {
					value := issue.Fields.CustomFields[mapping.JiraID]
					if value != nil {
						if assetFieldsByProject[projectKey] == nil {
							assetFieldsByProject[projectKey] = make(map[string]bool)
						}
						assetFieldsByProject[projectKey][mapping.JiraID] = true
					}
					for _, ref := range h.resolveJiraIssueAssetReferences(jobID, value) {
						if ref.SetID == 0 {
							continue
						}
						if setCandidates[mapping.JiraID] == nil {
							setCandidates[mapping.JiraID] = make(map[int]struct{})
						}
						setCandidates[mapping.JiraID][ref.SetID] = struct{}{}
					}
				}
			}
		}
	}

	resolved = make(map[string]int, len(assetMappings))
	for _, mapping := range assetMappings {
		switch strings.TrimSpace(mapping.AssetSchemaID) {
		case "text":
			continue
		case "", "auto":
			if len(setCandidates[mapping.JiraID]) == 1 {
				for setID := range setCandidates[mapping.JiraID] {
					resolved[mapping.JiraID] = setID
				}
			}
		default:
			if setID, ok := h.importedJiraAssetSetID(jobID, mapping.AssetSchemaID); ok {
				resolved[mapping.JiraID] = setID
			}
		}
	}
	return issueKeysByProject, resolved, assetFieldsByProject
}

func (h *JiraImportHandler) bindJiraAssetFieldsToWorkspace(
	workspaceID int,
	projectKey string,
	customFieldIDMap map[string]int,
	mappings []CustomFieldMapping,
	applicableFields map[string]bool,
) error {
	fieldIDs := make([]int, 0)
	seen := make(map[int]struct{})
	for _, mapping := range mappings {
		if mapping.WindshiftType != string(jira.FieldTypeAsset) || !applicableFields[mapping.JiraID] {
			continue
		}
		fieldID := customFieldIDMap[mapping.JiraID]
		if fieldID == 0 || h.customFieldType(fieldID) != string(jira.FieldTypeAsset) {
			continue
		}
		if _, exists := seen[fieldID]; exists {
			continue
		}
		seen[fieldID] = struct{}{}
		fieldIDs = append(fieldIDs, fieldID)
	}
	if len(fieldIDs) == 0 {
		return nil
	}
	sort.Ints(fieldIDs)

	var configSetID int
	if err := h.db.QueryRow(`
		SELECT configuration_set_id
		FROM workspace_configuration_sets
		WHERE workspace_id = ?
	`, workspaceID).Scan(&configSetID); err != nil {
		return fmt.Errorf("resolve workspace configuration set: %w", err)
	}

	defaultScreens := make(map[string]int)
	rows, err := h.db.Query(`
		SELECT context, screen_id
		FROM configuration_set_screens
		WHERE configuration_set_id = ?
	`, configSetID)
	if err != nil {
		return fmt.Errorf("load configuration screens: %w", err)
	}
	for rows.Next() {
		var contextName string
		var screenID int
		if scanErr := rows.Scan(&contextName, &screenID); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("scan configuration screen: %w", scanErr)
		}
		defaultScreens[contextName] = screenID
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate configuration screens: %w", err)
	}
	if err = rows.Close(); err != nil {
		return fmt.Errorf("close configuration screens: %w", err)
	}

	for _, contextName := range []string{"create", "edit", "view"} {
		sourceID := defaultScreens[contextName]
		if sourceID == 0 {
			sourceID = defaultScreens["create"]
		}
		if sourceID == 0 {
			sourceID = 1
		}
		name := fmt.Sprintf("%s Jira Import %s Screen (%d)", projectKey, strings.ToUpper(contextName[:1])+contextName[1:], workspaceID)
		screenID, ensureErr := h.ensureJiraImportedFieldScreen(name, sourceID, fieldIDs)
		if ensureErr != nil {
			return ensureErr
		}
		if _, err = h.db.ExecWrite(`
			INSERT INTO configuration_set_screens (configuration_set_id, screen_id, context, created_at)
			VALUES (?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(configuration_set_id, context) DO UPDATE SET screen_id = excluded.screen_id
		`, configSetID, screenID, contextName); err != nil {
			return fmt.Errorf("assign %s Jira import screen: %w", contextName, err)
		}
	}

	type itemTypeScreens struct {
		ID                       int
		CreateID, EditID, ViewID sql.NullInt64
	}
	itemRows, err := h.db.Query(`
		SELECT id, create_screen_id, edit_screen_id, view_screen_id
		FROM configuration_set_item_types
		WHERE configuration_set_id = ?
	`, configSetID)
	if err != nil {
		return fmt.Errorf("load item type screen overrides: %w", err)
	}
	var itemTypes []itemTypeScreens
	for itemRows.Next() {
		var item itemTypeScreens
		if scanErr := itemRows.Scan(&item.ID, &item.CreateID, &item.EditID, &item.ViewID); scanErr != nil {
			_ = itemRows.Close()
			return fmt.Errorf("scan item type screen overrides: %w", scanErr)
		}
		itemTypes = append(itemTypes, item)
	}
	if err = itemRows.Err(); err != nil {
		_ = itemRows.Close()
		return fmt.Errorf("iterate item type screen overrides: %w", err)
	}
	if err = itemRows.Close(); err != nil {
		return fmt.Errorf("close item type screen overrides: %w", err)
	}

	for _, item := range itemTypes {
		overrides := []struct {
			column string
			source sql.NullInt64
		}{
			{column: "create_screen_id", source: item.CreateID},
			{column: "edit_screen_id", source: item.EditID},
			{column: "view_screen_id", source: item.ViewID},
		}
		for _, override := range overrides {
			if !override.source.Valid || override.source.Int64 <= 0 {
				continue
			}
			name := fmt.Sprintf("%s Jira Import Item Screen (%d-%d-%s)", projectKey, workspaceID, item.ID, override.column)
			screenID, ensureErr := h.ensureJiraImportedFieldScreen(name, int(override.source.Int64), fieldIDs)
			if ensureErr != nil {
				return ensureErr
			}
			query := fmt.Sprintf(`UPDATE configuration_set_item_types SET %s = ? WHERE id = ?`, override.column)
			if _, err = h.db.ExecWrite(query, screenID, item.ID); err != nil {
				return fmt.Errorf("assign item type Jira import screen: %w", err)
			}
		}
	}
	return nil
}

func (h *JiraImportHandler) ensureJiraImportedFieldScreen(name string, sourceScreenID int, fieldIDs []int) (int, error) {
	var screenID int
	err := h.db.QueryRow(`SELECT id FROM screens WHERE name = ?`, name).Scan(&screenID)
	if errors.Is(err, sql.ErrNoRows) {
		var newID int64
		if err = h.db.QueryRow(`
			INSERT INTO screens (name, description, created_at, updated_at)
			VALUES (?, 'Imported Jira field layout', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			RETURNING id
		`, name).Scan(&newID); err != nil {
			return 0, fmt.Errorf("create Jira import screen: %w", err)
		}
		screenID = int(newID)
		if sourceScreenID > 0 {
			if _, err = h.db.ExecWrite(`
				INSERT INTO screen_fields (screen_id, field_type, field_identifier, display_order, is_required, field_width)
				SELECT ?, field_type, field_identifier, display_order, is_required, field_width
				FROM screen_fields
				WHERE screen_id = ?
			`, screenID, sourceScreenID); err != nil {
				return 0, fmt.Errorf("copy Jira import screen fields: %w", err)
			}
			if _, err = h.db.ExecWrite(`
				INSERT INTO screen_system_fields (screen_id, field_name)
				SELECT ?, field_name
				FROM screen_system_fields
				WHERE screen_id = ?
				ON CONFLICT(screen_id, field_name) DO NOTHING
			`, screenID, sourceScreenID); err != nil {
				return 0, fmt.Errorf("copy Jira import system fields: %w", err)
			}
		}
	} else if err != nil {
		return 0, fmt.Errorf("find Jira import screen: %w", err)
	}

	var displayOrder int
	_ = h.db.QueryRow(`SELECT COALESCE(MAX(display_order), 0) FROM screen_fields WHERE screen_id = ?`, screenID).Scan(&displayOrder)
	for _, fieldID := range fieldIDs {
		identifier := strconv.Itoa(fieldID)
		var exists int
		if queryErr := h.db.QueryRow(`
			SELECT COUNT(*) FROM screen_fields
			WHERE screen_id = ? AND field_type = 'custom' AND field_identifier = ?
		`, screenID, identifier).Scan(&exists); queryErr != nil {
			return 0, fmt.Errorf("check Jira import screen field: %w", queryErr)
		}
		if exists > 0 {
			continue
		}
		displayOrder++
		if _, err = h.db.ExecWrite(`
			INSERT INTO screen_fields (screen_id, field_type, field_identifier, display_order, is_required, field_width)
			VALUES (?, 'custom', ?, ?, false, 'full')
		`, screenID, identifier, displayOrder); err != nil {
			return 0, fmt.Errorf("add Jira Assets field to screen: %w", err)
		}
	}
	return screenID, nil
}

func (h *JiraImportHandler) completePagedIssueContainers(ctx context.Context, issue *jira.JiraIssue, client jira.Client) error {
	if issue == nil || issue.Key == "" {
		return nil
	}
	var errs []error
	if err := h.completeIssueComments(ctx, issue, client); err != nil {
		errs = append(errs, err)
	}
	if err := h.completeIssueWorklogs(ctx, issue, client); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (h *JiraImportHandler) completeIssueComments(ctx context.Context, issue *jira.JiraIssue, client jira.Client) error {
	container := issue.Fields.Comment
	if container == nil || container.Total <= len(container.Comments) {
		return nil
	}
	maxResults := container.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}
	comments := append([]jira.JiraComment{}, container.Comments...)
	for startAt := len(comments); startAt < container.Total; startAt = len(comments) {
		page, err := client.GetIssueComments(ctx, issue.Key, startAt, maxResults)
		if err != nil {
			return fmt.Errorf("fetch comments page startAt=%d: %w", startAt, err)
		}
		if page == nil || len(page.Comments) == 0 {
			break
		}
		comments = append(comments, page.Comments...)
		if page.Total > 0 {
			container.Total = page.Total
		}
	}
	container.Comments = comments
	return nil
}

func (h *JiraImportHandler) annotateJiraServiceDeskCommentVisibility(ctx context.Context, issue *jira.JiraIssue, client jira.Client) error {
	if issue == nil || issue.Key == "" || issue.Fields.Comment == nil || len(issue.Fields.Comment.Comments) == 0 {
		return nil
	}
	// Fail closed: if JSM visibility cannot be fetched or a comment is absent
	// from that response, importing it as an internal note cannot expose an
	// agent-only Jira comment to portal customers.
	for idx := range issue.Fields.Comment.Comments {
		public := false
		issue.Fields.Comment.Comments[idx].ServiceDeskPublic = &public
	}
	serviceDeskComments, err := client.ListServiceDeskRequestComments(ctx, issue.Key)
	if err != nil {
		return err
	}
	visibilityByID := make(map[string]bool, len(serviceDeskComments))
	for _, comment := range serviceDeskComments {
		visibilityByID[comment.ID] = comment.Public
	}
	for idx := range issue.Fields.Comment.Comments {
		if public, ok := visibilityByID[issue.Fields.Comment.Comments[idx].ID]; ok {
			issue.Fields.Comment.Comments[idx].ServiceDeskPublic = &public
		}
	}
	return nil
}

func (h *JiraImportHandler) completeIssueWorklogs(ctx context.Context, issue *jira.JiraIssue, client jira.Client) error {
	container := issue.Fields.Worklog
	if container == nil || container.Total <= len(container.Worklogs) {
		return nil
	}
	maxResults := container.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}
	worklogs := append([]jira.JiraWorklog{}, container.Worklogs...)
	for startAt := len(worklogs); startAt < container.Total; startAt = len(worklogs) {
		page, err := client.GetIssueWorklogs(ctx, issue.Key, startAt, maxResults)
		if err != nil {
			return fmt.Errorf("fetch worklogs page startAt=%d: %w", startAt, err)
		}
		if page == nil || len(page.Worklogs) == 0 {
			break
		}
		worklogs = append(worklogs, page.Worklogs...)
		if page.Total > 0 {
			container.Total = page.Total
		}
	}
	container.Worklogs = worklogs
	return nil
}

// ensureWorkflowsAndConfigSet fetches per-issue-type statuses from Jira,
// creates Windshift workflow(s) with transitions, and assigns a configuration set to the workspace.
func (h *JiraImportHandler) ensureWorkflowsAndConfigSet(
	ctx context.Context, jobID string, projectKey string, workspaceID int,
	statusMap map[string]int, itemTypeMap map[string]int, client jira.Client,
) error {
	// Check if workspace already has a configuration set
	csRepo := repository.NewConfigurationSetRepository(h.db)
	existingCSID, err := csRepo.GetWorkspaceConfigSetID(workspaceID)
	if err != nil {
		return fmt.Errorf("failed to check existing config set: %w", err)
	}
	if existingCSID != nil {
		slog.Info("Workspace already has a configuration set, skipping",
			slog.String("component", "jira"), slog.Int("workspaceID", workspaceID), slog.Int("configSetID", *existingCSID))
		return nil
	}

	// Fetch per-issue-type statuses from Jira
	issueTypeStatuses, err := client.GetProjectIssueTypeStatuses(ctx, projectKey)
	if err != nil {
		return fmt.Errorf("failed to get project issue type statuses: %w", err)
	}

	// Map Jira issue types and statuses to Windshift IDs
	// Group item types by their set of statuses
	type issueTypeInfo struct {
		windshiftItemTypeID int
		windshiftStatusIDs  []int
		jiraName            string
	}
	var issueTypeInfos []issueTypeInfo

	for _, its := range issueTypeStatuses {
		wsItemTypeID, ok := itemTypeMap[its.ID]
		if !ok {
			continue
		}

		// Map statuses to Windshift IDs
		statusIDSet := make(map[int]bool)
		for _, s := range its.Statuses {
			if wsStatusID, ok := statusMap[s.ID]; ok {
				statusIDSet[wsStatusID] = true
			}
		}
		if len(statusIDSet) == 0 {
			continue
		}

		var statusIDs []int
		for id := range statusIDSet {
			statusIDs = append(statusIDs, id)
		}
		sort.Ints(statusIDs)

		issueTypeInfos = append(issueTypeInfos, issueTypeInfo{
			windshiftItemTypeID: wsItemTypeID,
			windshiftStatusIDs:  statusIDs,
			jiraName:            its.Name,
		})
	}

	if len(issueTypeInfos) == 0 {
		slog.Warn("No issue types with mapped statuses found, skipping workflow creation",
			slog.String("component", "jira"), slog.String("project", projectKey))
		return nil
	}

	// Group item types by status set (sorted comma-joined IDs as key)
	type workflowGroup struct {
		statusIDs   []int
		itemTypeIDs []int
		typeNames   []string
	}
	groups := make(map[string]*workflowGroup)

	for _, info := range issueTypeInfos {
		// Build key from sorted status IDs
		parts := make([]string, len(info.windshiftStatusIDs))
		for i, id := range info.windshiftStatusIDs {
			parts[i] = strconv.Itoa(id)
		}
		key := strings.Join(parts, ",")

		if g, ok := groups[key]; ok {
			g.itemTypeIDs = append(g.itemTypeIDs, info.windshiftItemTypeID)
			g.typeNames = append(g.typeNames, info.jiraName)
		} else {
			groups[key] = &workflowGroup{
				statusIDs:   info.windshiftStatusIDs,
				itemTypeIDs: []int{info.windshiftItemTypeID},
				typeNames:   []string{info.jiraName},
			}
		}
	}

	// Determine which status IDs have category_id = 1 (To Do/New) for initial transitions
	newStatusIDs := make(map[int]bool)
	for _, statusIDs := range groups {
		for _, sid := range statusIDs.statusIDs {
			var catID int
			err = h.db.QueryRow("SELECT category_id FROM statuses WHERE id = ?", sid).Scan(&catID)
			if err == nil && catID == 1 {
				newStatusIDs[sid] = true
			}
		}
	}

	// Create workflow(s)
	multipleWorkflows := len(groups) > 1
	type createdWorkflow struct {
		workflowID  int
		itemTypeIDs []int
	}
	var workflows []createdWorkflow

	for _, group := range groups {
		// Build workflow name
		var wfName string
		if multipleWorkflows {
			wfName = projectKey + " - " + strings.Join(group.typeNames, ", ") + " Workflow"
		} else {
			wfName = projectKey + " Workflow"
		}

		// Insert workflow
		var workflowID int
		err = h.db.QueryRow(`
			INSERT INTO workflows (name, description, is_default, created_at, updated_at)
			VALUES (?, '', false, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
		`, wfName).Scan(&workflowID)
		if err != nil {
			return fmt.Errorf("failed to create workflow: %w", err)
		}

		// Create transitions
		order := 0

		// Initial transitions: NULL -> status where category_id = 1
		for _, sid := range group.statusIDs {
			if newStatusIDs[sid] {
				order++
				_, _ = h.db.ExecWrite(`
					INSERT INTO workflow_transitions (workflow_id, from_status_id, to_status_id, display_order, source_handle, target_handle, created_at)
					VALUES (?, NULL, ?, ?, '', '', CURRENT_TIMESTAMP)
				`, workflowID, sid, order)
			}
		}

		// All-to-all transitions
		for _, fromID := range group.statusIDs {
			for _, toID := range group.statusIDs {
				if fromID != toID {
					order++
					_, _ = h.db.ExecWrite(`
						INSERT INTO workflow_transitions (workflow_id, from_status_id, to_status_id, display_order, source_handle, target_handle, created_at)
						VALUES (?, ?, ?, ?, '', '', CURRENT_TIMESTAMP)
					`, workflowID, fromID, toID, order)
				}
			}
		}

		h.recordMapping(jobID, "workflow", fmt.Sprintf("wf-%s-%d", projectKey, workflowID), wfName, workflowID, nil)
		workflows = append(workflows, createdWorkflow{workflowID: workflowID, itemTypeIDs: group.itemTypeIDs})
	}

	// Pick default workflow (the one used by the most item types)
	defaultWfIdx := 0
	maxTypes := 0
	for i, wf := range workflows {
		if len(wf.itemTypeIDs) > maxTypes {
			maxTypes = len(wf.itemTypeIDs)
			defaultWfIdx = i
		}
	}
	defaultWfID := workflows[defaultWfIdx].workflowID

	// Create configuration set in a transaction
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	csName := projectKey + " Configuration"
	cs := &models.ConfigurationSet{
		Name:                    csName,
		WorkflowID:              &defaultWfID,
		DifferentiateByItemType: multipleWorkflows,
	}
	csID, err := csRepo.Create(tx, cs)
	if err != nil {
		return fmt.Errorf("failed to create configuration set: %w", err)
	}
	configSetID := int(csID)

	// Save item type configs with per-type workflow overrides
	var itemTypeConfigs []models.ItemTypeConfig
	for _, wf := range workflows {
		for _, itemTypeID := range wf.itemTypeIDs {
			config := models.ItemTypeConfig{
				ItemTypeID: itemTypeID,
			}
			// Only set workflow override if it differs from default
			if wf.workflowID != defaultWfID {
				wfID := wf.workflowID
				config.WorkflowID = &wfID
			}
			itemTypeConfigs = append(itemTypeConfigs, config)
		}
	}
	if err := csRepo.SaveItemTypeConfigs(tx, configSetID, itemTypeConfigs); err != nil {
		return fmt.Errorf("failed to save item type configs: %w", err)
	}

	// Assign workspace
	if err := csRepo.SaveWorkspaceAssignments(tx, configSetID, []int{workspaceID}); err != nil {
		return fmt.Errorf("failed to save workspace assignment: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit configuration set: %w", err)
	}

	h.recordMapping(jobID, "configuration_set", fmt.Sprintf("cs-%s", projectKey), csName, configSetID, nil)

	slog.Info("Created workflows and configuration set for import",
		slog.String("component", "jira"),
		slog.String("project", projectKey),
		slog.Int("workflows", len(workflows)),
		slog.Int("configSetID", configSetID))

	return nil
}

func (h *JiraImportHandler) ensureJiraIterations(ctx context.Context, jobID string, workspaceID int, projectKey string, client jira.Client) (map[string]int, error) {
	result := make(map[string]int)
	boards, err := client.ListBoards(ctx, projectKey)
	if err != nil {
		return result, err
	}
	if boards == nil || len(boards.Values) == 0 {
		return result, nil
	}

	typeID, err := h.ensureIterationType("Sprint", "#3b82f6", "Imported Jira Software sprint")
	if err != nil {
		return result, err
	}

	seen := make(map[string]struct{})
	for _, board := range boards.Values {
		if !jiraBoardSupportsSprints(board) {
			continue
		}
		sprints, err := client.GetBoardSprints(ctx, board.ID)
		if err != nil {
			slog.Warn("Failed to fetch Jira board sprints",
				slog.String("component", "jira"),
				slog.String("project", projectKey),
				slog.Int("boardID", board.ID),
				slog.Any("error", err))
			continue
		}
		if sprints == nil {
			continue
		}
		for _, sprint := range sprints.Values {
			sprintID := strconv.Itoa(sprint.ID)
			if _, ok := seen[sprintID]; ok {
				continue
			}
			seen[sprintID] = struct{}{}

			iterationID, ok := h.ensureJiraSprintIteration(workspaceID, typeID, sprint)
			if !ok {
				slog.Warn("Skipping Jira sprint without usable dates",
					slog.String("component", "jira"),
					slog.String("project", projectKey),
					slog.Int("sprintID", sprint.ID),
					slog.String("sprint", sprint.Name))
				continue
			}
			result[sprintID] = iterationID
			h.recordMapping(jobID, "iteration", sprintID, sprint.Name, iterationID, map[string]any{
				"jira_board_id":   board.ID,
				"jira_board_name": board.Name,
				"jira_state":      sprint.State,
				"jira_goal":       sprint.Goal,
			})
		}
	}
	return result, nil
}

func jiraBoardSupportsSprints(board jira.JiraBoard) bool {
	return strings.EqualFold(strings.TrimSpace(board.Type), "scrum")
}

func (h *JiraImportHandler) ensureIterationType(name, color, description string) (int, error) {
	var id int
	err := h.db.QueryRow(`SELECT id FROM iteration_types WHERE name = ?`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var newID int64
	if err := h.db.QueryRow(`
		INSERT INTO iteration_types (name, color, description, created_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
	`, name, color, description).Scan(&newID); err != nil {
		return 0, err
	}
	return int(newID), nil
}

func (h *JiraImportHandler) ensureJiraSprintIteration(workspaceID, typeID int, sprint jira.JiraSprint) (int, bool) {
	start, end, ok := jiraSprintDates(sprint)
	if !ok {
		return 0, false
	}
	status := "planned"
	switch strings.ToLower(sprint.State) {
	case "active":
		status = "active"
	case "closed":
		status = "completed"
	}

	name := strings.TrimSpace(sprint.Name)
	if name == "" {
		name = fmt.Sprintf("Jira Sprint %d", sprint.ID)
	}

	var existingID int
	err := h.db.QueryRow(`
		SELECT id FROM iterations
		WHERE workspace_id = ? AND is_global = 0 AND name = ?
	`, workspaceID, name).Scan(&existingID)
	if err == nil {
		return existingID, true
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}

	description := strings.TrimSpace(sprint.Goal)
	if description != "" {
		description = "Jira sprint goal: " + description
	}
	var newID int64
	err = h.db.QueryRow(`
		INSERT INTO iterations (name, description, start_date, end_date, status, type_id, is_global, workspace_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
	`, name, description, start, end, status, typeID, workspaceID).Scan(&newID)
	if err != nil {
		slog.Error("Failed to create Jira sprint iteration",
			slog.String("component", "jira"),
			slog.Int("sprintID", sprint.ID),
			slog.String("sprint", name),
			slog.Any("error", err))
		return 0, false
	}
	return int(newID), true
}

func jiraSprintDates(sprint jira.JiraSprint) (start, end string, ok bool) {
	start = jiraDateOnly(sprint.StartDate)
	end = jiraDateOnly(sprint.EndDate)
	if start == "" {
		start = jiraDateOnly(sprint.CompleteDate)
	}
	if end == "" {
		end = jiraDateOnly(sprint.CompleteDate)
	}
	if start == "" && end == "" {
		return "", "", false
	}
	if start == "" {
		start = end
	}
	if end == "" {
		end = start
	}
	return start, end, true
}

func jiraDateOnly(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if t := jira.ParseJiraTimestamp(value); t != nil {
		return t.UTC().Format("2006-01-02")
	}
	if len(value) >= len("2006-01-02") {
		candidate := value[:len("2006-01-02")]
		if _, err := time.Parse("2006-01-02", candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func (h *JiraImportHandler) ensureJiraTimeProject(jobID string, workspaceID int, projectKey, projectName string) (*int, error) {
	var existingWorkspaceProject sql.NullInt64
	if err := h.db.QueryRow(`SELECT time_project_id FROM workspaces WHERE id = ?`, workspaceID).Scan(&existingWorkspaceProject); err == nil && existingWorkspaceProject.Valid {
		id := int(existingWorkspaceProject.Int64)
		h.recordMapping(jobID, "time_project", "project:"+projectKey+":worklogs", projectKey, id, map[string]any{
			"workspace_id": workspaceID,
			"action":       "reuse_workspace_default",
			"was_created":  false,
		})
		return &id, nil
	}

	customerID, err := h.ensureJiraImportCustomer()
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(projectName)
	if name == "" {
		name = projectKey
	}
	if name == "" {
		name = "Jira Import"
	}
	timeProjectName := fmt.Sprintf("%s Jira Worklogs", name)

	var projectID int
	wasCreated := false
	err = h.db.QueryRow(`SELECT id FROM time_projects WHERE name = ? AND customer_id = ?`, timeProjectName, customerID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		description := fmt.Sprintf("Imported Jira worklogs for project %s", projectKey)
		var newID int64
		err = h.db.QueryRow(`
			INSERT INTO time_projects (customer_id, name, description, status, color, settings, active, created_at, updated_at)
			VALUES (?, ?, ?, 'Active', '#3b82f6', '{}', true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
		`, customerID, timeProjectName, description).Scan(&newID)
		projectID = int(newID)
		wasCreated = err == nil
	}
	if err != nil {
		return nil, err
	}

	if _, err := h.db.ExecWrite(`UPDATE workspaces SET time_project_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND time_project_id IS NULL`, projectID, workspaceID); err != nil {
		return nil, err
	}
	h.recordMapping(jobID, "time_project", "project:"+projectKey+":worklogs", projectKey, projectID, map[string]any{
		"workspace_id": workspaceID,
		"was_created":  wasCreated,
	})
	return &projectID, nil
}

func (h *JiraImportHandler) ensureJiraImportCustomer() (int, error) {
	const name = "Jira Imports"
	var id int
	err := h.db.QueryRow(`SELECT id FROM customer_organisations WHERE name = ?`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var newID int64
	if err := h.db.QueryRow(`
		INSERT INTO customer_organisations (name, description, active, created_at, updated_at)
		VALUES (?, 'Synthetic customer used for imported Jira worklogs', true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
	`, name).Scan(&newID); err != nil {
		return 0, err
	}
	return int(newID), nil
}

// ensureWorkspace creates a dedicated workspace for an imported Jira project.
// createdByUserID grants the import initiator workspace admin access; pass 0 if unknown.
func (h *JiraImportHandler) ensureWorkspace(_ context.Context, jobID string, mapping *WorkspaceMapping, createdByUserID int) (int, error) {
	if !mapping.CreateNew || mapping.WindshiftID != nil {
		return 0, fmt.Errorf("jira project %s must create a new workspace; existing workspaces cannot be reused", mapping.JiraKey)
	}

	workspaceSvc := services.NewWorkspaceService(h.db)

	// Create new workspace using service
	result, err := workspaceSvc.Create(services.CreateWorkspaceParams{
		Name:        mapping.NewWorkspaceName,
		Key:         mapping.NewWorkspaceKey,
		Description: "Imported from Jira",
		CreatorID:   createdByUserID,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create workspace: %w", err)
	}

	// Record the mapping
	h.recordMapping(jobID, "workspace", mapping.JiraKey, mapping.JiraKey, result.Workspace.ID, map[string]any{
		"action":      "create",
		"was_created": true,
	})

	return result.Workspace.ID, nil
}

type jiraAffectsVersionCustomField struct {
	FieldID              int
	OptionIDsByJiraID    map[string]int
	OptionLabelsByJiraID map[string]string
}

type jiraImportCustomFieldOption struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

type jiraImportCustomFieldOptions struct {
	NextID int                           `json:"next_id"`
	Items  []jiraImportCustomFieldOption `json:"items"`
}

func (h *JiraImportHandler) ensureAffectsVersionCustomField(_ context.Context, jobID string, mappings []VersionMapping) (*jiraAffectsVersionCustomField, error) {
	if len(mappings) == 0 {
		return nil, nil
	}
	const (
		jiraID    = "system:versions"
		name      = "Jira Affects Version/s"
		fieldType = "multiselect"
	)

	options := jiraImportCustomFieldOptions{NextID: 1}
	optionIDByLabel := make(map[string]int)
	optionIDsByJiraID := make(map[string]int)
	optionLabelsByJiraID := make(map[string]string)
	ensureOption := func(label string) int {
		label = strings.TrimSpace(label)
		if label == "" {
			return 0
		}
		key := strings.ToLower(label)
		if id, ok := optionIDByLabel[key]; ok {
			return id
		}
		id := options.NextID
		if id <= 0 {
			id = len(options.Items) + 1
		}
		options.Items = append(options.Items, jiraImportCustomFieldOption{ID: id, Label: label})
		optionIDByLabel[key] = id
		options.NextID = id + 1
		return id
	}

	var fieldID int
	var existingType string
	var existingOptions sql.NullString
	err := h.db.QueryRow(`
		SELECT id, field_type, options FROM custom_field_definitions
		WHERE LOWER(name) = LOWER(?)
		ORDER BY CASE WHEN field_type = ? THEN 0 WHEN field_type = 'milestone' THEN 1 ELSE 2 END, id
		LIMIT 1
	`, name, fieldType).Scan(&fieldID, &existingType, &existingOptions)
	if err == nil && strings.TrimSpace(existingOptions.String) != "" {
		var parsed jiraImportCustomFieldOptions
		if json.Unmarshal([]byte(existingOptions.String), &parsed) == nil {
			options = parsed
			if options.NextID <= 0 {
				options.NextID = 1
			}
			for _, item := range options.Items {
				if item.ID >= options.NextID {
					options.NextID = item.ID + 1
				}
				if strings.TrimSpace(item.Label) != "" {
					optionIDByLabel[strings.ToLower(strings.TrimSpace(item.Label))] = item.ID
				}
			}
		}
	}

	for _, m := range mappings {
		label := strings.TrimSpace(m.JiraName)
		if label == "" || m.JiraID == "" {
			continue
		}
		optionID := ensureOption(label)
		if optionID == 0 {
			continue
		}
		optionIDsByJiraID[m.JiraID] = optionID
		optionLabelsByJiraID[m.JiraID] = label
	}

	optionBytes, marshalErr := json.Marshal(options)
	if marshalErr != nil {
		return nil, marshalErr
	}
	description := "Imported from Jira system field versions (Affects Version/s). Stores all affected versions as multiselect values; Jira version IDs and metadata are also preserved in item metadata."

	wasCreated := errors.Is(err, sql.ErrNoRows)
	if wasCreated {
		now := time.Now()
		var newID int64
		err = h.db.QueryRow(`
			INSERT INTO custom_field_definitions (name, field_type, description, required, options, display_order,
			                                      applies_to_portal_customers, applies_to_customer_organisations,
			                                      created_at, updated_at)
			VALUES (?, ?, ?, false, ?, 0, false, false, ?, ?) RETURNING id
		`, name, fieldType, description, string(optionBytes), now, now).Scan(&newID)
		fieldID = int(newID)
	} else if err == nil {
		_, err = h.db.ExecWrite(`
			UPDATE custom_field_definitions
			SET field_type = ?, description = ?, options = ?, updated_at = ?
			WHERE id = ?
		`, fieldType, description, string(optionBytes), time.Now(), fieldID)
	}
	if err != nil {
		return nil, err
	}

	meta := map[string]any{
		"action":          "create_or_reuse",
		"jira_field_type": "system:versions",
		"windshift_type":  fieldType,
		"option_count":    len(options.Items),
		"was_created":     wasCreated,
	}
	if existingType != "" && existingType != fieldType {
		meta["previous_windshift_type"] = existingType
	}
	h.recordMapping(jobID, "custom_field", jiraID, name, fieldID, meta)
	return &jiraAffectsVersionCustomField{FieldID: fieldID, OptionIDsByJiraID: optionIDsByJiraID, OptionLabelsByJiraID: optionLabelsByJiraID}, nil
}

// ensureCustomFields creates or maps global Windshift custom fields selected
// in the Jira mapping step. The returned map is Jira customfield_* ID →
// Windshift custom_field_definitions.id and is used when writing an item's
// custom_field_values JSON so imported values are keyed by Windshift field IDs,
// not transient Jira keys.
//
// Story Points is intentionally excluded: it maps to items.story_points as a
// first-class field during issue import.
//
//nolint:unparam // context kept for symmetry with the other ensure* helpers.
func (h *JiraImportHandler) ensureCustomFields(_ context.Context, jobID string, mappings []CustomFieldMapping, assetFieldSetIDs map[string]int) (map[string]int, error) {
	result := make(map[string]int)
	now := time.Now()

	for _, m := range mappings {
		if m.Action == "skip" || m.JiraID == "" || isJiraStoryPointsField(m) || isJiraSprintField(m) ||
			m.JiraType == jiraRequestTypeFieldType {
			continue
		}

		fieldType := strings.TrimSpace(m.WindshiftType)
		if fieldType == "" || fieldType == string(jira.FieldTypeUnmapped) {
			continue
		}
		fieldOptions := ""
		if fieldType == string(jira.FieldTypeAsset) {
			assetSetID, ok := assetFieldSetIDs[m.JiraID]
			if ok && assetSetID > 0 {
				fieldOptions = fmt.Sprintf(`{"asset_set_id":%d,"ql_query":"","multi":true}`, assetSetID)
			} else {
				// An Assets custom field has one configured Jira schema, but an empty
				// field does not expose that schema in issue payloads. Unless the
				// operator selected a schema explicitly, preserve its values as text
				// rather than inventing an asset-set relationship.
				fieldType = "textarea"
			}
		}
		if !isValidFieldType(fieldType) {
			slog.Warn("Skipping Jira custom field with unsupported Windshift type",
				slog.String("component", "jira"),
				slog.String("jiraFieldID", m.JiraID),
				slog.String("jiraFieldName", m.JiraName),
				slog.String("windshiftType", fieldType))
			continue
		}

		if m.Action == "map" && m.WindshiftID != nil {
			result[m.JiraID] = *m.WindshiftID
			h.recordMapping(jobID, "custom_field", m.JiraID, m.JiraName, *m.WindshiftID, map[string]any{"action": "map"})
			continue
		}

		name := strings.TrimSpace(m.JiraName)
		if name == "" {
			name = m.JiraID
		}

		var fieldID int
		wasCreated := false
		baseName := name
		var err error
		for attempt := 0; attempt < 10; attempt++ {
			var existingType string
			var existingOptions sql.NullString
			err = h.db.QueryRow(`
				SELECT id, field_type, options
				FROM custom_field_definitions
				WHERE LOWER(name) = LOWER(?)
			`, name).Scan(&fieldID, &existingType, &existingOptions)
			if err == nil {
				compatible := existingType == fieldType
				if compatible && fieldType == string(jira.FieldTypeAsset) {
					var existingConfig struct {
						AssetSetID int `json:"asset_set_id"`
					}
					var requestedConfig struct {
						AssetSetID int `json:"asset_set_id"`
					}
					compatible = json.Unmarshal([]byte(existingOptions.String), &existingConfig) == nil &&
						json.Unmarshal([]byte(fieldOptions), &requestedConfig) == nil &&
						existingConfig.AssetSetID > 0 &&
						existingConfig.AssetSetID == requestedConfig.AssetSetID
				}
				if compatible {
					break
				}
				// Same field name but different type: keep both fields by creating a
				// deterministic Jira-specific name instead of silently writing values
				// into an incompatible Windshift custom field.
				if attempt == 9 {
					err = fmt.Errorf("custom field name %q exists with incompatible type %q", name, existingType)
					break
				}
				name = fmt.Sprintf("%s (Jira %s%s)", baseName, strings.TrimPrefix(m.JiraID, "customfield_"), strings.Repeat("-", attempt))
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				break
			}
			description := fmt.Sprintf("Imported from Jira field %s (%s)", m.JiraID, m.JiraType)
			err = h.db.QueryRow(`
				INSERT INTO custom_field_definitions (name, field_type, description, required, options, display_order,
				                                      applies_to_portal_customers, applies_to_customer_organisations,
				                                      created_at, updated_at)
				VALUES (?, ?, ?, false, ?, 0, false, false, ?, ?) RETURNING id
			`, name, fieldType, description, fieldOptions, now, now).Scan(&fieldID)
			wasCreated = err == nil
			break
		}
		if err != nil {
			slog.Error("Failed to ensure Jira custom field",
				slog.String("component", "jira"),
				slog.String("jiraFieldID", m.JiraID),
				slog.String("jiraFieldName", name),
				slog.Any("error", err))
			continue
		}

		result[m.JiraID] = fieldID
		h.recordMapping(jobID, "custom_field", m.JiraID, name, fieldID, map[string]any{
			"action":          "create_or_reuse",
			"jira_field_type": m.JiraType,
			"windshift_type":  fieldType,
			"was_created":     wasCreated,
		})
	}

	return result, nil
}

// ensureMilestones creates milestones for Jira versions in a workspace
// Returns a map from Jira version ID to Windshift milestone ID
//
//nolint:unparam // error return kept for interface consistency with other ensure* methods
func (h *JiraImportHandler) ensureMilestones(_ context.Context, jobID string, workspaceID int, mappings []VersionMapping) (map[string]int, error) {
	result := make(map[string]int)
	planningSvc := services.NewPlanningService(h.db)

	for _, m := range mappings {
		if !m.CreateNew {
			continue
		}

		// Check if milestone already exists by name in this workspace
		var existingID int
		err := h.db.QueryRow(`SELECT id FROM milestones WHERE name = ? AND workspace_id = ?`, m.JiraName, workspaceID).Scan(&existingID)
		if err == nil {
			result[m.JiraID] = existingID
			h.recordMapping(jobID, "milestone", m.JiraID, m.JiraName, existingID, map[string]any{
				"action":      "reuse_existing",
				"was_created": false,
			})
			continue
		}

		// Determine status based on released flag
		status := "planning"
		if m.Released {
			status = "completed"
		}

		// Create milestone
		var jiraTargetDate *string
		if m.ReleaseDate != "" {
			jiraTargetDate = &m.ReleaseDate
		}
		milestone, err := planningSvc.CreateMilestone(services.CreateMilestoneParams{
			Name:        m.JiraName,
			TargetDate:  jiraTargetDate,
			Status:      status,
			IsGlobal:    false,
			WorkspaceID: &workspaceID,
		})
		if err != nil {
			slog.Error("Failed to create milestone", slog.String("component", "jira"), slog.String("version", m.JiraName), slog.Any("error", err))
			continue
		}

		result[m.JiraID] = milestone.ID
		h.recordMapping(jobID, "milestone", m.JiraID, m.JiraName, milestone.ID, map[string]any{
			"action":      "create",
			"was_created": true,
		})
	}

	return result, nil
}

// ensureStatuses creates or maps statuses (global model - shared across workspaces)
//
//nolint:unparam // error return kept for interface consistency with other ensure* methods
func (h *JiraImportHandler) ensureStatuses(_ context.Context, jobID string, mappings []StatusMapping) (map[string]int, error) {
	result := make(map[string]int)
	statusSvc := services.NewEnumService(h.db, services.NewStatusConfig())

	for _, m := range mappings {
		if !m.CreateNew && m.WindshiftID != nil {
			for _, jiraID := range m.JiraIDs {
				result[jiraID] = *m.WindshiftID
			}
			continue
		}

		// Map Jira category to Windshift category ID
		// Default category IDs: 1="To Do", 2="In Progress", 3="Done"
		categoryID := 1
		switch m.CategoryKey {
		case "new":
			categoryID = 1
		case "indeterminate":
			categoryID = 2
		case "done":
			categoryID = 3
		}

		// Check if status already exists by name
		var existingID int
		err := h.db.QueryRow(`SELECT id FROM statuses WHERE name = ?`, m.JiraName).Scan(&existingID)
		if err == nil {
			// Status exists, use existing ID
			for _, jiraID := range m.JiraIDs {
				result[jiraID] = existingID
			}
			if len(m.JiraIDs) > 0 {
				h.recordMapping(jobID, "status", m.JiraIDs[0], m.JiraName, existingID, map[string]any{
					"action":      "reuse_existing",
					"was_created": false,
				})
			}
			continue
		}

		// Create new status using service
		status := &models.Status{
			Name:       m.JiraName,
			CategoryID: categoryID,
		}
		entity, err := statusSvc.Create(status, nil)
		if err != nil {
			slog.Error("Failed to create status", slog.String("component", "jira"), slog.String("status", m.JiraName), slog.Any("error", err))
			continue
		}

		statusID := entity.GetID()
		for _, jiraID := range m.JiraIDs {
			result[jiraID] = statusID
		}

		// Record the mapping
		if len(m.JiraIDs) > 0 {
			h.recordMapping(jobID, "status", m.JiraIDs[0], m.JiraName, statusID, map[string]any{
				"action":      "create",
				"was_created": true,
			})
		}
	}

	return result, nil
}

// ensureItemTypes creates or maps item types (global model - shared across workspaces)
//
//nolint:unparam // error return kept for interface consistency with other ensure* methods
func (h *JiraImportHandler) ensureItemTypes(_ context.Context, jobID string, mappings []IssueTypeMapping) (map[string]int, error) {
	result := make(map[string]int)
	itemTypeSvc := services.NewEnumService(h.db, services.NewItemTypeConfig())

	for _, m := range mappings {
		if !m.CreateNew && m.WindshiftID != nil {
			for _, jiraID := range m.JiraIDs {
				result[jiraID] = *m.WindshiftID
			}
			continue
		}

		// Check if item type already exists by name
		var existingID int
		err := h.db.QueryRow(`SELECT id FROM item_types WHERE name = ?`, m.JiraName).Scan(&existingID)
		if err == nil {
			// Item type exists, use existing ID
			for _, jiraID := range m.JiraIDs {
				result[jiraID] = existingID
			}
			if len(m.JiraIDs) > 0 {
				h.recordMapping(jobID, "item_type", m.JiraIDs[0], m.JiraName, existingID, map[string]any{
					"action":      "reuse_existing",
					"was_created": false,
				})
			}
			continue
		}

		// Create new item type using service
		itemType := &models.ItemType{
			Name:           m.JiraName,
			Icon:           "Circle",
			Color:          "#3B82F6",
			HierarchyLevel: m.HierarchyLevel,
		}
		entity, err := itemTypeSvc.Create(itemType, nil)
		if err != nil {
			slog.Error("Failed to create item type", slog.String("component", "jira"), slog.String("itemType", m.JiraName), slog.Any("error", err))
			continue
		}

		itemTypeID := entity.GetID()
		for _, jiraID := range m.JiraIDs {
			result[jiraID] = itemTypeID
		}

		// Record the mapping
		if len(m.JiraIDs) > 0 {
			h.recordMapping(jobID, "item_type", m.JiraIDs[0], m.JiraName, itemTypeID, map[string]any{
				"action":      "create",
				"was_created": true,
			})
		}
	}

	return result, nil
}
