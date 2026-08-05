package llm

import (
	"context"

	"windshift/internal/models"
)

// TemplateSource is the catalog interface both the Agent Studio templates
// endpoint and Draft creation call. *PromptStore and *TemplateCatalog each
// satisfy it, so callers can inject defaults alone or a merged catalog.
type TemplateSource interface {
	AgentTemplates() []AgentTemplate
	AgentTemplate(key string) (AgentTemplate, bool)
}

// AgentTemplateOverrides is the subset of the override repository the
// merged catalog reads at each call. It includes disabled rows because those
// suppress embedded defaults. It is always called with a fresh context so
// the catalog is request-scoped, not cached.
type AgentTemplateOverrides interface {
	List(ctx context.Context) ([]*models.AgentTemplateCatalogEntry, error)
}

// TemplateCatalog merges the embedded default catalog with the
// system-admin-configured DB overrides (WI-922). When an enabled override
// exists for a template key, its name, default_type, and instructions win
// over the embedded defaults. A blank field in the override falls back to
// the default value. New keys (not in the embedded definitions) are
// appended if the override supplies a non-empty name.
type TemplateCatalog struct {
	defaults  *PromptStore
	overrides AgentTemplateOverrides
}

// NewTemplateCatalog wraps a default PromptStore with a DB override source.
func NewTemplateCatalog(defaults *PromptStore, overrides AgentTemplateOverrides) *TemplateCatalog {
	return &TemplateCatalog{defaults: defaults, overrides: overrides}
}

// AgentTemplates returns the merged catalog — defaults overlaid by any
// enabled DB override rows.
func (c *TemplateCatalog) AgentTemplates() []AgentTemplate {
	return c.mergeTemplates()
}

// AgentTemplate resolves a single template key from the merged catalog.
func (c *TemplateCatalog) AgentTemplate(key string) (AgentTemplate, bool) {
	for _, t := range c.mergeTemplates() {
		if t.Key == key {
			return t, true
		}
	}
	return AgentTemplate{}, false
}

func (c *TemplateCatalog) mergeTemplates() []AgentTemplate {
	// Build a map of configured overrides keyed by template_key. Disabled
	// rows must remain visible here so they can suppress embedded defaults.
	overrides := make(map[string]*models.AgentTemplateCatalogEntry)
	if c.overrides != nil {
		rows, err := c.overrides.List(context.Background())
		if err == nil {
			for _, row := range rows {
				overrides[row.TemplateKey] = row
			}
		}
	}

	// Start from the embedded defaults, omitting any definition explicitly
	// disabled by a configured row and overlaying enabled definitions.
	defaults := c.defaults.AgentTemplates()
	base := make([]AgentTemplate, 0, len(defaults))
	for _, t := range defaults {
		if ov, ok := overrides[t.Key]; ok {
			if !ov.Enabled {
				continue
			}
			if ov.Name != "" {
				t.Name = ov.Name
			}
			if ov.DefaultType != "" {
				t.DefaultType = ov.DefaultType
			}
			if ov.Instructions != "" {
				t.Instructions = ov.Instructions
			}
		}
		base = append(base, t)
	}

	// Append any new keys not in the embedded defaults.
	for _, ov := range overrides {
		if !ov.Enabled || ov.Name == "" {
			continue // skip rows that are not properly configured
		}
		found := false
		for _, t := range base {
			if t.Key == ov.TemplateKey {
				found = true
				break
			}
		}
		if !found {
			defaultType := ov.DefaultType
			if defaultType == "" {
				defaultType = models.AgentProfileStandard
			}
			base = append(base, AgentTemplate{
				Key:          ov.TemplateKey,
				Name:         ov.Name,
				DefaultType:  defaultType,
				Instructions: ov.Instructions,
			})
		}
	}

	return base
}
