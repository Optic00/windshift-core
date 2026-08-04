package llm

import "windshift/internal/models"

// AgentTemplate is one immutable template choice in the Agent Studio creation
// journey. Instructions contains the effective embedded-or-overridden prompt
// copied into the new Draft; later prompt-file changes do not mutate profiles.
type AgentTemplate struct {
	Key          string                  `json:"key"`
	Name         string                  `json:"name"`
	DefaultType  models.AgentProfileType `json:"default_type"`
	Instructions string                  `json:"instructions"`
}

type agentTemplateDefinition struct {
	key         string
	name        string
	defaultType models.AgentProfileType
	promptName  string
}

var agentTemplateDefinitions = []agentTemplateDefinition{
	{key: "workspace_guide", name: "Workspace Guide", defaultType: models.AgentProfileStandard, promptName: PromptAgentWorkspaceGuide},
	{key: "work_item_triage", name: "Work-item Triage", defaultType: models.AgentProfileStandard, promptName: PromptAgentWorkItemTriage},
	{key: "delivery_coordinator", name: "Delivery Coordinator", defaultType: models.AgentProfileStandard, promptName: PromptAgentDeliveryCoordinator},
	{key: "software_engineer", name: "Software Engineer", defaultType: models.AgentProfileCoding, promptName: PromptAgentSoftwareEngineer},
	{key: "code_reviewer", name: "Code Reviewer", defaultType: models.AgentProfileCoding, promptName: PromptAgentCodeReviewer},
	{key: "qa_test_engineer", name: "QA / Test Engineer", defaultType: models.AgentProfileStandard, promptName: PromptAgentQATestEngineer},
	{key: "release_manager", name: "Release Manager", defaultType: models.AgentProfileStandard, promptName: PromptAgentReleaseManager},
	{key: "blank", name: "Blank", defaultType: models.AgentProfileStandard, promptName: PromptAgentBlank},
}

// AgentTemplates returns the stable creation catalog with effective prompts.
func (ps *PromptStore) AgentTemplates() []AgentTemplate {
	out := make([]AgentTemplate, 0, len(agentTemplateDefinitions))
	for _, definition := range agentTemplateDefinitions {
		out = append(out, AgentTemplate{
			Key:          definition.key,
			Name:         definition.name,
			DefaultType:  definition.defaultType,
			Instructions: ps.Get(definition.promptName),
		})
	}
	return out
}

// AgentTemplate resolves a template key from the closed creation catalog.
func (ps *PromptStore) AgentTemplate(key string) (AgentTemplate, bool) {
	for _, template := range ps.AgentTemplates() {
		if template.Key == key {
			return template, true
		}
	}
	return AgentTemplate{}, false
}
