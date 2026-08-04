// Package agentstudio contains dependency-neutral Agent Studio contracts shared
// by the executable AI-tool registry and profile lifecycle services.
package agentstudio

// CapabilityGroup is the stable persisted key for Standard-agent tool policy.
type CapabilityGroup string

const (
	CapabilityReadComment       CapabilityGroup = "read_comment"
	CapabilityIssueManagement   CapabilityGroup = "issue_management"
	CapabilityCommentEditing    CapabilityGroup = "comment_editing"
	CapabilityPlanningActivity  CapabilityGroup = "planning_activity"
	CapabilityKnowledgeDiagrams CapabilityGroup = "knowledge_diagrams"
	CapabilityActions           CapabilityGroup = "actions"
	CapabilityTests             CapabilityGroup = "tests"
	CapabilityTime              CapabilityGroup = "time"
	CapabilityUsersApprovals    CapabilityGroup = "users_approvals"
)

// AllCapabilityGroups returns the closed set of persisted keys. The executable
// aitools registry remains authoritative for which groups currently contain
// Standard-safe tools and what those tools are.
func AllCapabilityGroups() []CapabilityGroup {
	return []CapabilityGroup{
		CapabilityReadComment,
		CapabilityIssueManagement,
		CapabilityCommentEditing,
		CapabilityPlanningActivity,
		CapabilityKnowledgeDiagrams,
		CapabilityActions,
		CapabilityTests,
		CapabilityTime,
		CapabilityUsersApprovals,
	}
}
