package services

import (
	"encoding/json"
	"fmt"
	"strings"

	"windshift/internal/models"
)

// CreateMilestoneNodeConfig is the JSON shape stored in
// actions.action_nodes.node_config for `create_milestone` nodes. All
// template fields go through ActionService.SubstituteVariables, so
// `{{ref.short}}` etc. resolve at execution time using the SCM payload
// the sync layer packs into ActionEvent.NewValues.
type CreateMilestoneNodeConfig struct {
	// NameTemplate is the milestone name to set on insert. Empty falls
	// back to the rendered UpsertKey.
	NameTemplate string `json:"name_template,omitempty"`
	// UpsertKeyTemplate is the stable identifier used to pair a tag
	// event with a previously-created branch milestone. Required —
	// without an upsert key the executor would create duplicates on
	// every poll tick.
	UpsertKeyTemplate string `json:"upsert_key_template"`
	// StatusOnBranch is the status applied when this node runs for a
	// scm_release_branch_created event AND no milestone yet exists.
	// Defaults to "planning".
	StatusOnBranch string `json:"status_on_branch,omitempty"`
	// StatusOnTag is the status applied when this node runs for a
	// scm_tag_created event. When the milestone already exists (typical
	// case: branch ran first) the status is promoted to this value.
	// Defaults to "in-progress".
	StatusOnTag string `json:"status_on_tag,omitempty"`
	// AttachReleaseOnTag controls whether a milestone_releases row is
	// inserted alongside the tag promotion. Defaults to true.
	AttachReleaseOnTag *bool `json:"attach_release_on_tag,omitempty"`
	// CategoryID, if set, is applied on insert only — promoting an
	// existing milestone doesn't change its category.
	CategoryID *int `json:"category_id,omitempty"`
	// Description, if non-empty, is rendered + applied on insert only.
	DescriptionTemplate string `json:"description_template,omitempty"`
}

// CreateMilestoneExecutor implements the create_milestone node type. It
// owns only the narrow deps it actually needs (PlanningService for
// milestone CRUD, NodeAPI for substitution + chained event emission);
// no implicit coupling to the wider ActionService.
type CreateMilestoneExecutor struct {
	planning *PlanningService
	api      NodeAPI
}

// NewCreateMilestoneExecutor returns an executor ready to register with
// ActionService. Both deps are required; passing nil for either leaves
// the executor in a state where Execute will return a configuration
// error rather than panicking, so a partial DI wiring is debuggable.
func NewCreateMilestoneExecutor(planning *PlanningService, api NodeAPI) *CreateMilestoneExecutor {
	return &CreateMilestoneExecutor{planning: planning, api: api}
}

// NodeType pins the node-type dispatch key.
func (e *CreateMilestoneExecutor) NodeType() models.ActionNodeType {
	return models.ActionNodeCreateMilestone
}

// Execute runs the upsert logic. Returns an error to fail the action; on
// success populates stepResult.Output with audit fields the workflow
// builder UI shows in the run history.
func (e *CreateMilestoneExecutor) Execute(node *models.ActionNode, ctx *models.ExecutionContext, stepResult *models.StepResult) error {
	if e.planning == nil {
		return fmt.Errorf("create_milestone executor missing PlanningService dep")
	}
	if e.api == nil {
		return fmt.Errorf("create_milestone executor missing NodeAPI dep")
	}
	if ctx == nil || ctx.Event == nil {
		return fmt.Errorf("create_milestone requires an event context")
	}

	var config CreateMilestoneNodeConfig
	if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
		return fmt.Errorf("invalid create_milestone config: %w", err)
	}

	upsertKey := strings.TrimSpace(e.api.SubstituteVariables(config.UpsertKeyTemplate, ctx))
	if upsertKey == "" {
		return fmt.Errorf("create_milestone: upsert_key_template rendered empty")
	}

	name := strings.TrimSpace(e.api.SubstituteVariables(config.NameTemplate, ctx))
	if name == "" {
		name = upsertKey
	}

	isTagEvent := ctx.Event.EventType == models.ActionTriggerSCMTagCreated
	isBranchEvent := ctx.Event.EventType == models.ActionTriggerSCMReleaseBranchCreated
	if !isTagEvent && !isBranchEvent {
		// The node can in principle be wired to any trigger, but the
		// payload assumptions below (ref.*, repo.*) only hold for the
		// two SCM triggers. Fail loudly rather than silently doing
		// something surprising.
		return fmt.Errorf("create_milestone fired from unsupported trigger %q", ctx.Event.EventType)
	}

	workspaceID := ctx.Event.WorkspaceID
	if workspaceID <= 0 {
		return fmt.Errorf("create_milestone: event missing workspace_id")
	}

	existing, err := e.planning.FindMilestoneByExternalKey(workspaceID, upsertKey)
	if err != nil {
		return err
	}

	statusOnBranch := config.StatusOnBranch
	if statusOnBranch == "" {
		statusOnBranch = "planning"
	}
	statusOnTag := config.StatusOnTag
	if statusOnTag == "" {
		statusOnTag = "in-progress"
	}

	var milestoneID int
	created := false
	switch {
	case existing == nil:
		// Insert. For a branch event we start in "planning"; for a tag
		// event with no prior branch milestone we jump straight to the
		// tag status (covers projects that don't use release/* branches).
		insertStatus := statusOnBranch
		if isTagEvent {
			insertStatus = statusOnTag
		}
		description := strings.TrimSpace(e.api.SubstituteVariables(config.DescriptionTemplate, ctx))
		ws := workspaceID
		ek := upsertKey
		m, err := e.planning.CreateMilestone(CreateMilestoneParams{
			Name:        name,
			Description: description,
			Status:      insertStatus,
			CategoryID:  config.CategoryID,
			IsGlobal:    false,
			WorkspaceID: &ws,
			ExternalKey: &ek,
		})
		if err != nil {
			return err
		}
		milestoneID = m.ID
		created = true
	case isTagEvent:
		// Promote an existing milestone — typical "branch → tag" path.
		if err := e.planning.SetMilestoneStatus(existing.ID, workspaceID, statusOnTag); err != nil {
			return err
		}
		milestoneID = existing.ID
	default:
		// Branch event but a milestone with this key already exists —
		// leave it alone. Repeated release/* pushes shouldn't downgrade
		// a milestone that's already in-progress.
		milestoneID = existing.ID
	}

	stepResult.Output = map[string]interface{}{
		"milestone_id": milestoneID,
		"external_key": upsertKey,
		"created":      created,
		"event_type":   string(ctx.Event.EventType),
	}

	// Attach release row when fitting. Default-true to keep the user's
	// "I want the tag to bind to the milestone" mental model the typical
	// outcome; users who want to skip can set attach_release_on_tag=false.
	attach := true
	if config.AttachReleaseOnTag != nil {
		attach = *config.AttachReleaseOnTag
	}
	if isTagEvent && attach {
		if err := e.attachRelease(ctx, milestoneID); err != nil {
			// Release attach is non-fatal; the milestone itself was
			// upserted successfully and that's the user-visible value.
			stepResult.Output["attach_release_error"] = err.Error()
		} else {
			stepResult.Output["release_attached"] = true
		}
	}

	return nil
}

// attachRelease pulls SCM details from the event payload and writes the
// milestone_releases row. Missing fields (e.g. release URL unknown for
// lightweight tags) degrade to empty strings rather than failing the
// node — the row is still a useful pointer to the underlying tag.
func (e *CreateMilestoneExecutor) attachRelease(ctx *models.ExecutionContext, milestoneID int) error {
	tagName, _ := ctx.Variables["new_ref.name"].(string)
	if tagName == "" {
		return fmt.Errorf("event missing ref.name; cannot attach release")
	}
	sha, _ := ctx.Variables["new_ref.sha"].(string)
	repoFullName, _ := ctx.Variables["new_repo.full_name"].(string)

	var repoPtr *string
	if repoFullName != "" {
		r := repoFullName
		repoPtr = &r
	}

	return e.planning.AttachRelease(ReleaseMilestoneParams{
		ID:              milestoneID,
		TagName:         tagName,
		TargetCommitish: sha,
		SCMRepository:   repoPtr,
	})
}
