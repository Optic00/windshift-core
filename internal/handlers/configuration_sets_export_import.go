package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// sanitizeConfigSetTemplate applies the same field policies as live CRUD.
// JSON option blobs are size-validated, not scrubbed, to preserve their shape.
func sanitizeConfigSetTemplate(w http.ResponseWriter, r *http.Request, tpl *services.ConfigSetTemplate) bool {
	if tpl.ExportedBy != nil {
		sanitizeConfigSetExportBy(tpl.ExportedBy)
	}
	p := &tpl.Payload
	sanitize.ApplyAll(
		sanitize.Pair{Target: &p.ConfigurationSet.Name, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &p.ConfigurationSet.Description, Policy: sanitize.RichText},
		sanitize.Pair{Target: &p.ConfigurationSet.DefaultItemTypeName, Policy: sanitize.PlainTextField},
	)
	for i := range p.StatusCategories {
		sanitize.Apply(&p.StatusCategories[i].Name, sanitize.PlainTextField)
	}
	for i := range p.CustomFields {
		sanitize.ApplyAll(
			sanitize.Pair{Target: &p.CustomFields[i].Name, Policy: sanitize.ShortIdentifier},
			sanitize.Pair{Target: &p.CustomFields[i].FieldType, Policy: sanitize.ShortIdentifier},
			sanitize.Pair{Target: &p.CustomFields[i].Description, Policy: sanitize.Comment},
		)
		if err := sanitize.ValidateJSONPayload(
			fmt.Sprintf("custom_fields[%d].options", i), p.CustomFields[i].Options,
		); err != nil {
			respondValidationError(w, r, err.Error())
			return false
		}
	}
	for i := range p.Statuses {
		sanitize.ApplyAll(
			sanitize.Pair{Target: &p.Statuses[i].Name, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &p.Statuses[i].Description, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &p.Statuses[i].CategoryName, Policy: sanitize.PlainTextField},
		)
	}
	for i := range p.ItemTypes {
		sanitize.ApplyAll(
			sanitize.Pair{Target: &p.ItemTypes[i].Name, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &p.ItemTypes[i].Description, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &p.ItemTypes[i].Icon, Policy: sanitize.ShortIdentifier},
			sanitize.Pair{Target: &p.ItemTypes[i].Color, Policy: sanitize.ShortIdentifier},
		)
	}
	for i := range p.Priorities {
		sanitize.ApplyAll(
			sanitize.Pair{Target: &p.Priorities[i].Name, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &p.Priorities[i].Description, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &p.Priorities[i].Icon, Policy: sanitize.ShortIdentifier},
			sanitize.Pair{Target: &p.Priorities[i].Color, Policy: sanitize.ShortIdentifier},
		)
	}
	for i := range p.Screens {
		sc := &p.Screens[i]
		sanitize.ApplyAll(
			sanitize.Pair{Target: &sc.Name, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &sc.Description, Policy: sanitize.RichText},
		)
		for j := range sc.Fields {
			sanitize.ApplyAll(
				sanitize.Pair{Target: &sc.Fields[j].FieldKind, Policy: sanitize.ShortIdentifier},
				sanitize.Pair{Target: &sc.Fields[j].FieldIdentifier, Policy: sanitize.ShortIdentifier},
				sanitize.Pair{Target: &sc.Fields[j].CustomFieldName, Policy: sanitize.ShortIdentifier},
				sanitize.Pair{Target: &sc.Fields[j].FieldWidth, Policy: sanitize.ShortIdentifier},
			)
		}
		for j := range sc.SystemFields {
			sanitize.Apply(&sc.SystemFields[j], sanitize.ShortIdentifier)
		}
	}
	for i := range p.Workflows {
		wf := &p.Workflows[i]
		sanitize.ApplyAll(
			sanitize.Pair{Target: &wf.Name, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &wf.Description, Policy: sanitize.RichText},
		)
		for j := range wf.Transitions {
			t := &wf.Transitions[j]
			sanitize.ApplyAll(
				sanitize.Pair{Target: t.FromStatusName, Policy: sanitize.PlainTextField},
				sanitize.Pair{Target: &t.ToStatusName, Policy: sanitize.PlainTextField},
				sanitize.Pair{Target: &t.SourceHandle, Policy: sanitize.ShortIdentifier},
				sanitize.Pair{Target: &t.TargetHandle, Policy: sanitize.ShortIdentifier},
			)
		}
	}
	for i := range p.ConditionSets {
		cs := &p.ConditionSets[i]
		sanitize.ApplyAll(
			sanitize.Pair{Target: &cs.Name, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &cs.Description, Policy: sanitize.RichText},
			sanitize.Pair{Target: &cs.WorkflowName, Policy: sanitize.PlainTextField},
		)
		for j := range cs.TransitionConditions {
			tc := &cs.TransitionConditions[j]
			sanitize.ApplyAll(
				sanitize.Pair{Target: tc.FromStatusName, Policy: sanitize.PlainTextField},
				sanitize.Pair{Target: &tc.ToStatusName, Policy: sanitize.PlainTextField},
				sanitize.Pair{Target: &tc.LogicMode, Policy: sanitize.ShortIdentifier},
			)
			for k := range tc.Conditions {
				sanitizeConfigSetCondition(&tc.Conditions[k])
			}
		}
	}
	for i := range p.ApprovalSets {
		as := &p.ApprovalSets[i]
		sanitize.ApplyAll(
			sanitize.Pair{Target: &as.Name, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &as.Description, Policy: sanitize.RichText},
			sanitize.Pair{Target: &as.WorkflowName, Policy: sanitize.PlainTextField},
		)
		for j := range as.SetStatuses {
			ss := &as.SetStatuses[j]
			sanitize.ApplyAll(
				sanitize.Pair{Target: &ss.StatusName, Policy: sanitize.PlainTextField},
				sanitize.Pair{Target: &ss.StepMode, Policy: sanitize.ShortIdentifier},
				sanitize.Pair{Target: ss.ApproveTransition.FromStatusName, Policy: sanitize.PlainTextField},
				sanitize.Pair{Target: &ss.ApproveTransition.ToStatusName, Policy: sanitize.PlainTextField},
				sanitize.Pair{Target: ss.DenyTransition.FromStatusName, Policy: sanitize.PlainTextField},
				sanitize.Pair{Target: &ss.DenyTransition.ToStatusName, Policy: sanitize.PlainTextField},
			)
			for k := range ss.Steps {
				sanitizeConfigSetApprovalStep(&ss.Steps[k])
			}
		}
	}
	sanitizeConfigSetLinks(&p.Links)
	return true
}

// configSetConditionScriptMaxBytes mirrors the live validateCondition cap on
// script-condition bodies (condition_sets.go) so an imported bundle cannot
// plant a script larger than the condition editor would ever accept.
const configSetConditionScriptMaxBytes = 10240

// configSetConditionConfigMaxBytes bounds the serialized size of one
// condition's free-form Config blob. Legitimate configs are tiny (a couple
// of IDs / a field ref + pattern / a <=10KB script), so 64 KiB is generous
// headroom while keeping a hostile bundle from parking megabytes in
// conditions.config.
const configSetConditionConfigMaxBytes = 64 * 1024

// sanitizeConfigSetCondition bounds one workflow condition: the type / mode
// machine tokens (live path allowlists these; import must at least bound
// them), the user-facing error message, and the free-form Config blob. A
// Config that still exceeds the byte cap after the script trim is dropped
// wholesale — there is no legitimate config that large, and an empty config
// fails closed in the condition engine rather than persisting megabytes.
func sanitizeConfigSetCondition(c *services.ConfigSetTplCondition) {
	sanitize.ApplyAll(
		sanitize.Pair{Target: &c.Type, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &c.Mode, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &c.ErrorMessage, Policy: sanitize.PlainTextField},
	)
	if c.Type == models.ConditionTypeScript {
		if script, ok := c.Config["script"].(string); ok && len(script) > configSetConditionScriptMaxBytes {
			c.Config["script"] = script[:configSetConditionScriptMaxBytes]
		}
	}
	if raw, err := json.Marshal(c.Config); err != nil || len(raw) > configSetConditionConfigMaxBytes {
		c.Config = map[string]any{}
	}
}

// sanitizeConfigSetApprovalStep scrubs the name / identifier / email refs on
// one approval step. Role + group names render in the approval chain editor
// and echo back via UnresolvedRef on a 422; field identifiers + emails are
// identifier-shaped. The quorum / policy / source machine tokens get
// ShortIdentifier because the import path bypasses the ApprovalSetService
// allowlists that bound them on the live path.
func sanitizeConfigSetApprovalStep(st *services.ConfigSetTplApprovalStep) {
	sanitize.ApplyAll(
		sanitize.Pair{Target: &st.Name, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &st.QuorumMode, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.RejectionPolicy, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.ApproverSource, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.OnLeaveStrategy, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.EscalationAction, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.EscalationTargetSource, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.ApproverFieldIdentifier, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.ApproverCustomFieldName, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.ApproverRoleName, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &st.ApproverGroupName, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &st.ApproverUserEmail, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.EscalationTargetFieldIdentifier, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.EscalationTargetCustomFieldName, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &st.EscalationTargetRoleName, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &st.EscalationTargetGroupName, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &st.EscalationTargetUserEmail, Policy: sanitize.ShortIdentifier},
	)
}

// sanitizeConfigSetLinks scrubs the by-name glue section so its references
// keep matching the (equally sanitized) defining entities above.
func sanitizeConfigSetLinks(links *services.ConfigSetTplLinks) {
	sanitize.ApplyAll(
		sanitize.Pair{Target: &links.WorkflowName, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &links.ConditionSetName, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &links.ApprovalSetName, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &links.CreateScreenName, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &links.EditScreenName, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &links.ViewScreenName, Policy: sanitize.PlainTextField},
	)
	for i := range links.PriorityNames {
		sanitize.Apply(&links.PriorityNames[i], sanitize.PlainTextField)
	}
	for i := range links.ItemTypeConfigs {
		itc := &links.ItemTypeConfigs[i]
		sanitize.ApplyAll(
			sanitize.Pair{Target: &itc.ItemTypeName, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &itc.WorkflowName, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &itc.ConditionSetName, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &itc.ApprovalSetName, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &itc.CreateScreenName, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &itc.EditScreenName, Policy: sanitize.PlainTextField},
			sanitize.Pair{Target: &itc.ViewScreenName, Policy: sanitize.PlainTextField},
		)
	}
}

// sanitizeConfigSetExportBy scrubs the provenance stamp. On export, Instance
// derives from the request Host header; on import the whole struct arrives
// inside the uploaded bundle.
func sanitizeConfigSetExportBy(by *services.ConfigSetExportBy) {
	sanitize.ApplyAll(
		sanitize.Pair{Target: &by.Username, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &by.Instance, Policy: sanitize.PlainTextField},
	)
}

// configSetImportMaxBytes caps the upload size for /configuration-sets/import.
// Sufficient headroom for an ITSM-style bundle (custom fields, screens, a
// non-trivial workflow + condition + approval set) without inviting abuse.
const configSetImportMaxBytes = 5 << 20 // 5 MiB

// Export streams a configuration set as a JSON template suitable for upload
// on another instance via Import. Read-only; permitted to any authenticated
// user (matches the GET /configuration-sets/{id} read auth).
func (h *ConfigurationSetHandler) Export(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Confirm the configuration set exists before building the bundle —
	// gives us a 404 instead of a generic 500 on a missing id.
	cs, err := h.repo.FindByIDBasic(id)
	if err == repository.ErrNotFound {
		respondNotFound(w, r, "configuration_set")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	exportSvc := services.NewConfigSetExportService(h.db, h.repo)
	tpl, err := exportSvc.Export(r.Context(), id, exportedByFromRequest(r))
	if err != nil {
		if errors.Is(err, services.ErrCannotExportDefault) {
			// Audit the refusal so attempts to extract the default are visible
			// in the security log alongside successful exports.
			currentUser := utils.GetCurrentUser(r)
			if currentUser != nil {
				_ = logger.LogAudit(h.db, logger.AuditEvent{
					UserID:       currentUser.ID,
					Username:     currentUser.Username,
					IPAddress:    utils.GetClientIP(r),
					UserAgent:    r.UserAgent(),
					ActionType:   logger.ActionConfigSetExport,
					ResourceType: logger.ResourceConfigurationSet,
					ResourceID:   &id,
					ResourceName: cs.Name,
					Success:      false,
					ErrorMessage: "default_not_exportable",
				})
			}
			restapi.RespondError(w, r, restapi.NewAPIError(http.StatusForbidden, "default_not_exportable",
				"The default configuration set cannot be exported; clone it first if you need a portable copy."))
			return
		}
		respondInternalError(w, r, fmt.Errorf("export configuration set %d: %w", id, err))
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		logAudit(h.db, r, currentUser, logger.ActionConfigSetExport, logger.ResourceConfigurationSet, &id, cs.Name)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", configSetExportFilename(cs.Name)))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(tpl); err != nil {
		// Headers already sent, so just log.
		respondInternalError(w, r, err)
	}
}

// Import accepts a multipart upload (field "file") containing a JSON template
// produced by Export, applies it inside a transaction, and returns the new
// configuration set record. Admin-only; rate-limited at the route level.
func (h *ConfigurationSetHandler) Import(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, configSetImportMaxBytes)
	// #nosec G120 -- the body is already capped by MaxBytesReader above; the int arg is the in-memory threshold, not the upper bound
	if err := r.ParseMultipartForm(configSetImportMaxBytes); err != nil {
		respondBadRequest(w, r, "Failed to parse multipart form (max "+humanBytes(configSetImportMaxBytes)+")")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		respondBadRequest(w, r, "Missing 'file' upload field")
		return
	}
	defer func() { _ = file.Close() }()

	var tpl services.ConfigSetTemplate
	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tpl); err != nil {
		respondBadRequest(w, r, "Invalid template JSON: "+err.Error())
		return
	}
	if !sanitizeConfigSetTemplate(w, r, &tpl) {
		return
	}

	importSvc := services.NewConfigSetImportService(h.db, h.repo)
	newID, warnings, err := importSvc.Import(r.Context(), &tpl)
	if err != nil {
		var conflictErr *services.ErrDefaultEntityConflict
		if errors.As(err, &conflictErr) {
			apiErr := restapi.NewAPIError(http.StatusConflict, "default_entity_conflict",
				"Import would shadow a default-flagged entity on this instance; rename the bundle or import elsewhere.")
			apiErr.WithDetails(map[string]any{"conflicts": conflictErr.Conflicts})
			restapi.RespondError(w, r, apiErr)
			return
		}
		var unresolvedErr *services.ErrUnresolvedReferences
		if errors.As(err, &unresolvedErr) {
			apiErr := restapi.NewAPIError(http.StatusUnprocessableEntity, "unresolved_references",
				"Import requires identity references that don't exist on this instance")
			apiErr.WithDetails(map[string]any{"unresolved": unresolvedErr.Items})
			restapi.RespondError(w, r, apiErr)
			return
		}
		respondInternalError(w, r, err)
		return
	}

	created, err := h.repo.FindByID(newID)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("load created configuration set: %w", err))
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		logAuditWithDetails(h.db, r, currentUser, logger.ActionConfigSetImport, logger.ResourceConfigurationSet, &newID, created.Name, map[string]any{
			"workflow_id":   created.WorkflowID,
			"warning_count": len(warnings),
		})
	}

	if h.permissionService != nil {
		_ = h.permissionService.OnConfigurationSetChanged(newID)
	}

	if len(warnings) == 0 {
		respondJSONCreated(w, created)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":     created,
		"warnings": warnings,
	})
}

func exportedByFromRequest(r *http.Request) *services.ConfigSetExportBy {
	user := utils.GetCurrentUser(r)
	if user == nil {
		return nil
	}
	host := r.Host
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	by := &services.ConfigSetExportBy{
		Username: user.Username,
		Instance: scheme + "://" + host,
	}
	sanitizeConfigSetExportBy(by)
	return by
}

// configSetExportFilename produces a safe download filename derived from the
// config set's name. Slashes and quotes are stripped to avoid header smuggling.
func configSetExportFilename(name string) string {
	out := make([]byte, 0, len(name)+len(".json"))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		case c == ' ':
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		out = []byte("configuration-set")
	}
	return string(out) + ".json"
}

func humanBytes(n int) string {
	const mib = 1 << 20
	if n >= mib {
		return fmt.Sprintf("%d MiB", n/mib)
	}
	return fmt.Sprintf("%d bytes", n)
}
