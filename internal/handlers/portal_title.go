package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"windshift/internal/models"
	"windshift/internal/services"
	tmpl "windshift/internal/services/template"
)

// renderPortalTitle renders a missing form title from the request type template.
// Templates support type, requester, description (120 runes), and named custom
// field variables; an empty result is rejected by the caller.
func (h *PortalHandler) renderPortalTitle(ctx context.Context, rt *models.RequestType, description string, customFields map[string]any, userID, customerID *int) string {
	return renderSubmissionTitle(ctx, h.portalService, rt, description, customFields, userID, customerID)
}

func renderSubmissionTitle(ctx context.Context, portalService *services.PortalService, rt *models.RequestType, description string, customFields map[string]any, userID, customerID *int) string {
	if rt == nil || strings.TrimSpace(rt.TitleTemplate) == "" {
		return ""
	}

	vars := map[string]string{
		"type.name":       rt.Name,
		"type.id":         strconv.Itoa(rt.ID),
		"description":     truncateRunes(description, 120),
		"requester.name":  "",
		"requester.email": "",
	}

	switch {
	case userID != nil:
		if name, email, err := portalService.GetUserRequesterTemplateVars(ctx, *userID); err == nil {
			vars["requester.name"] = name
			vars["requester.email"] = email
		}
	case customerID != nil:
		if name, email, err := portalService.GetCustomerRequesterTemplateVars(ctx, *customerID); err == nil {
			vars["requester.name"] = name
			vars["requester.email"] = email
		}
	}

	for name, value := range resolveCustomFieldNames(ctx, portalService, customFields) {
		vars["custom."+name] = value
	}

	return strings.TrimSpace(tmpl.Substitute(rt.TitleTemplate, vars))
}

// resolveCustomFieldNames maps numeric field IDs to template variable names.
// Virtual or malformed keys are skipped.
func resolveCustomFieldNames(ctx context.Context, portalService *services.PortalService, customFields map[string]any) map[string]string {
	if len(customFields) == 0 {
		return nil
	}

	var ids []int
	keyToValue := map[int]any{}
	for k, v := range customFields {
		if id, err := strconv.Atoi(k); err == nil {
			ids = append(ids, id)
			keyToValue[id] = v
		}
	}
	if len(ids) == 0 {
		return nil
	}

	names, err := portalService.GetCustomFieldNamesByID(ctx, ids)
	if err != nil {
		return nil
	}

	out := map[string]string{}
	for id, name := range names {
		if v, ok := keyToValue[id]; ok && name != "" {
			out[name] = formatTemplateValue(v)
		}
	}
	return out
}

func formatTemplateValue(v any) string {
	if v == nil {
		return ""
	}
	switch typed := v.(type) {
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		// Render whole JSON numbers without a decimal suffix.
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
