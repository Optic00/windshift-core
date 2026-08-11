package services

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"windshift/internal/database"
	"windshift/internal/repository"
)

// windshiftExport is the deterministic post-import snapshot consumed by the capture diff harness.
type windshiftExport struct {
	JobID         string                `json:"job_id"`
	GeneratedAt   string                `json:"generated_at"`
	SchemaVersion int                   `json:"schema_version"`
	Items         []windshiftExportItem `json:"items"`
	Warnings      []string              `json:"warnings"`
}

type windshiftExportItem struct {
	JiraKey          string                      `json:"jira_key"`
	WindshiftID      int                         `json:"windshift_id"`
	Title            string                      `json:"title"`
	Description      string                      `json:"description"`
	StatusName       string                      `json:"status_name"`
	ItemTypeName     string                      `json:"item_type_name"`
	PriorityName     string                      `json:"priority_name,omitempty"`
	AssigneeUsername string                      `json:"assignee_username,omitempty"`
	ReporterUsername string                      `json:"reporter_username,omitempty"`
	CreatorUsername  string                      `json:"creator_username,omitempty"`
	ParentJiraKey    string                      `json:"parent_jira_key,omitempty"`
	StoryPoints      *float64                    `json:"story_points,omitempty"`
	DueDate          string                      `json:"due_date,omitempty"`
	CreatedAt        string                      `json:"created_at"`
	UpdatedAt        string                      `json:"updated_at"`
	Labels           []string                    `json:"labels"`
	Milestones       []string                    `json:"milestones"`
	CustomFields     map[string]json.RawMessage  `json:"custom_fields"`
	Comments         []windshiftExportComment    `json:"comments"`
	Attachments      []windshiftExportAttachment `json:"attachments"`
	Links            []windshiftExportLink       `json:"links"`
	Worklogs         []windshiftExportWorklog    `json:"worklogs"`
}

type windshiftExportComment struct {
	JiraID         string `json:"jira_id"`
	AuthorUsername string `json:"author_username,omitempty"`
	Content        string `json:"content"`
	CreatedAt      string `json:"created_at"`
}

type windshiftExportAttachment struct {
	JiraID           string `json:"jira_id"`
	OriginalFilename string `json:"original_filename"`
	MimeType         string `json:"mime_type"`
	FileSize         int64  `json:"file_size"`
	UploaderUsername string `json:"uploader_username,omitempty"`
}

type windshiftExportLink struct {
	LinkType      string `json:"link_type"`
	TargetJiraKey string `json:"target_jira_key"`
}

type windshiftExportWorklog struct {
	JiraID           string `json:"jira_id"`
	AuthorUsername   string `json:"author_username,omitempty"`
	TimeSpentSeconds int    `json:"time_spent_seconds"`
	Started          string `json:"started"`
}

// WriteWindshiftExport assembles a deterministic snapshot of everything imported
// under jobID and writes it to <dir>/windshift_export.json. Always filters from
// jira_import_id_mappings WHERE job_id = ? so a partial re-import on the same
// workspace cannot leak rows from a prior completed job.
func WriteWindshiftExport(db database.Database, jobID, dir string) error {
	exp := windshiftExport{
		JobID:         jobID,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		SchemaVersion: 1,
		Items:         []windshiftExportItem{},
		Warnings:      []string{},
	}

	// First pass: gather every item mapping for this job and a windshift_id ->
	// jira_key index that the link resolver needs later.
	itemMappings, idToKey, warnings, err := loadItemMappings(db, jobID)
	if err != nil {
		return fmt.Errorf("load item mappings: %w", err)
	}
	exp.Warnings = append(exp.Warnings, warnings...)
	entityMappings, err := loadCaptureEntityMappings(db, jobID)
	if err != nil {
		return fmt.Errorf("load capture entity mappings: %w", err)
	}

	for _, im := range itemMappings {
		item, ok, warn := loadItemRow(db, im.windshiftID, im.jiraKey)
		if warn != "" {
			exp.Warnings = append(exp.Warnings, warn)
		}
		if !ok {
			continue
		}
		item.JiraKey = im.jiraKey
		item.WindshiftID = im.windshiftID
		item.ParentJiraKey = im.parentKey

		item.Labels = loadItemLabels(db, im.windshiftID)
		item.Milestones = loadItemMilestones(db, im.windshiftID)
		item.Comments = loadItemComments(db, entityMappings.forItem("comment", im.jiraKey), im.windshiftID)
		item.Attachments = loadItemAttachments(db, entityMappings.forItem("attachment", im.jiraKey), im.windshiftID)
		item.Links = loadItemLinks(db, im.windshiftID, idToKey)
		item.Worklogs = loadItemWorklogs(db, entityMappings.forItem("worklog", im.jiraKey), im.windshiftID)

		exp.Items = append(exp.Items, item)
	}

	sort.Slice(exp.Items, func(i, j int) bool { return exp.Items[i].JiraKey < exp.Items[j].JiraKey })

	data, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal export: %w", err)
	}

	path := filepath.Join(dir, "windshift_export.json")
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // path built from filepath.Join with operator-supplied dir
		return fmt.Errorf("write %s: %w", path, err)
	}

	slog.Info("Saved windshift export snapshot", slog.String("component", "jira"),
		slog.String("path", path), slog.Int("items", len(exp.Items)))
	return nil
}

type itemMapping struct {
	jiraKey     string
	windshiftID int
	parentKey   string
}

type captureEntityMapping struct {
	jiraID       string
	metadataJSON string
}

type captureEntityMappings map[string]map[string]map[int]captureEntityMapping

func (m captureEntityMappings) forItem(entityType, jiraKey string) map[int]captureEntityMapping {
	if byKey := m[entityType]; byKey != nil {
		return byKey[jiraKey]
	}
	return nil
}

func loadCaptureEntityMappings(db database.Database, jobID string) (captureEntityMappings, error) {
	rows, err := db.Query(`
		SELECT entity_type, jira_key, jira_id, windshift_id, COALESCE(metadata_json, '{}')
		FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type IN ('comment', 'attachment', 'worklog')
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := captureEntityMappings{}
	for rows.Next() {
		var entityType, jiraKey, jiraID, metadataJSON string
		var windshiftID int
		if err := rows.Scan(&entityType, &jiraKey, &jiraID, &windshiftID, &metadataJSON); err != nil {
			return nil, err
		}
		if out[entityType] == nil {
			out[entityType] = map[string]map[int]captureEntityMapping{}
		}
		if out[entityType][jiraKey] == nil {
			out[entityType][jiraKey] = map[int]captureEntityMapping{}
		}
		out[entityType][jiraKey][windshiftID] = captureEntityMapping{jiraID: jiraID, metadataJSON: metadataJSON}
	}
	return out, rows.Err()
}

//nolint:gocritic // result names would shadow loop locals; positional return is clearer
func loadItemMappings(db database.Database, jobID string) ([]itemMapping, map[int]string, []string, error) {
	rows, err := db.Query(`
		SELECT jira_key, windshift_id, COALESCE(metadata_json, '{}')
		FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type = 'item'
	`, jobID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	var mappings []itemMapping
	idToKey := map[int]string{}
	var warnings []string
	for rows.Next() {
		var (
			jiraKey  string
			wsID     int
			metaJSON string
		)
		if err := rows.Scan(&jiraKey, &wsID, &metaJSON); err != nil {
			warnings = append(warnings, fmt.Sprintf("scan item mapping: %v", err))
			continue
		}
		parent := ""
		if metaJSON != "" {
			var meta map[string]any
			if err := json.Unmarshal([]byte(metaJSON), &meta); err == nil {
				if pk, ok := meta["parent_key"].(string); ok {
					parent = pk
				}
			}
		}
		mappings = append(mappings, itemMapping{jiraKey: jiraKey, windshiftID: wsID, parentKey: parent})
		idToKey[wsID] = jiraKey
	}
	return mappings, idToKey, warnings, rows.Err()
}

//nolint:gocritic // positional return: (row, ok, warning); naming wouldn't add clarity
func loadItemRow(db database.Database, itemID int, jiraKey string) (windshiftExportItem, bool, string) {
	snapshot, err := repository.NewItemRepository(db).GetCaptureSnapshot(itemID)
	if err != nil {
		return windshiftExportItem{}, false, fmt.Sprintf("item %s (id=%d) missing: %v", jiraKey, itemID, err)
	}

	out := windshiftExportItem{
		Title:            snapshot.Title,
		Description:      snapshot.Description,
		StatusName:       snapshot.StatusName,
		ItemTypeName:     snapshot.ItemTypeName,
		PriorityName:     snapshot.PriorityName,
		AssigneeUsername: snapshot.AssigneeUsername,
		ReporterUsername: snapshot.ReporterUsername,
		CreatorUsername:  snapshot.CreatorUsername,
		StoryPoints:      snapshot.StoryPoints,
		DueDate:          snapshot.DueDate,
		CreatedAt:        snapshot.CreatedAt,
		UpdatedAt:        snapshot.UpdatedAt,
		CustomFields:     map[string]json.RawMessage{},
	}
	if snapshot.CustomFieldValues != "" {
		var bag map[string]json.RawMessage
		if err := json.Unmarshal([]byte(snapshot.CustomFieldValues), &bag); err == nil {
			out.CustomFields = bag
		}
	}
	return out, true, ""
}

func loadItemLabels(db database.Database, itemID int) []string {
	labels, err := repository.NewLabelRepository(db).ListForItem(itemID)
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		out = append(out, label.Name)
	}
	sort.Strings(out)
	if out == nil {
		return []string{}
	}
	return out
}

func loadItemMilestones(db database.Database, itemID int) []string {
	out, err := repository.NewPlanningRepository(db).ListMilestoneNamesForItem(itemID)
	if err != nil {
		return []string{}
	}
	sort.Strings(out)
	if out == nil {
		return []string{}
	}
	return out
}

func loadItemComments(db database.Database, mappings map[int]captureEntityMapping, itemID int) []windshiftExportComment {
	ids := captureMappingIDs(mappings)
	comments, err := NewCommentService(db).ListCaptureComments(itemID, ids)
	if err != nil {
		return []windshiftExportComment{}
	}
	out := make([]windshiftExportComment, 0, len(comments))
	for _, comment := range comments {
		out = append(out, windshiftExportComment{
			JiraID: mappings[comment.ID].jiraID, AuthorUsername: comment.AuthorUsername,
			Content: comment.Content, CreatedAt: comment.CreatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JiraID < out[j].JiraID })
	if out == nil {
		return []windshiftExportComment{}
	}
	return out
}

func loadItemAttachments(db database.Database, mappings map[int]captureEntityMapping, itemID int) []windshiftExportAttachment {
	attachments, err := repository.NewAttachmentRepository(db).ListCaptureAttachments(itemID, captureMappingIDs(mappings))
	if err != nil {
		return []windshiftExportAttachment{}
	}
	out := make([]windshiftExportAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		out = append(out, windshiftExportAttachment{
			JiraID: mappings[attachment.ID].jiraID, OriginalFilename: attachment.OriginalFilename,
			MimeType: attachment.MimeType, FileSize: attachment.FileSize, UploaderUsername: attachment.UploaderUsername,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JiraID < out[j].JiraID })
	if out == nil {
		return []windshiftExportAttachment{}
	}
	return out
}

func loadItemLinks(db database.Database, itemID int, idToKey map[int]string) []windshiftExportLink {
	links, err := repository.NewItemLinkRepository(db).ListCaptureItemLinks(itemID)
	if err != nil {
		return []windshiftExportLink{}
	}
	out := []windshiftExportLink{}
	for _, link := range links {
		targetKey, ok := idToKey[link.TargetID]
		if !ok {
			// Link points outside this job's imported items; skip — the diff
			// harness treats these as expected gaps and the importer logs them.
			continue
		}
		out = append(out, windshiftExportLink{LinkType: link.LinkType, TargetJiraKey: targetKey})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LinkType != out[j].LinkType {
			return out[i].LinkType < out[j].LinkType
		}
		return out[i].TargetJiraKey < out[j].TargetJiraKey
	})
	if out == nil {
		return []windshiftExportLink{}
	}
	return out
}

func loadItemWorklogs(db database.Database, mappings map[int]captureEntityMapping, itemID int) []windshiftExportWorklog {
	worklogs, err := repository.NewTimeWorklogRepository(db).ListCaptureWorklogs(itemID, captureMappingIDs(mappings))
	if err != nil {
		return []windshiftExportWorklog{}
	}
	out := make([]windshiftExportWorklog, 0, len(worklogs))
	for _, worklog := range worklogs {
		mapping := mappings[worklog.ID]
		w := windshiftExportWorklog{JiraID: mapping.jiraID, AuthorUsername: worklog.AuthorUsername}
		var meta struct {
			TimeSpentSeconds int    `json:"time_spent_seconds"`
			Started          string `json:"started"`
		}
		if mapping.metadataJSON != "" {
			_ = json.Unmarshal([]byte(mapping.metadataJSON), &meta)
		}
		if meta.TimeSpentSeconds > 0 {
			w.TimeSpentSeconds = meta.TimeSpentSeconds
		} else {
			w.TimeSpentSeconds = worklog.DurationMinutes * 60
		}
		if meta.Started != "" {
			w.Started = meta.Started
		} else if worklog.StartedUnix > 0 {
			w.Started = time.Unix(worklog.StartedUnix, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JiraID < out[j].JiraID })
	if out == nil {
		return []windshiftExportWorklog{}
	}
	return out
}

func captureMappingIDs(mappings map[int]captureEntityMapping) []int {
	ids := make([]int, 0, len(mappings))
	for id := range mappings {
		ids = append(ids, id)
	}
	return ids
}
