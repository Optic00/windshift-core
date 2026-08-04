package models

import "time"

type AgentSessionType string

const (
	AgentSessionGeneral  AgentSessionType = "general"
	AgentSessionStandard AgentSessionType = "standard"
)

type AgentSession struct {
	ID             int                       `json:"id"`
	SessionType    AgentSessionType          `json:"session_type"`
	OwnerUserID    int                       `json:"owner_user_id"`
	WorkspaceID    *int                      `json:"workspace_id,omitempty"`
	AgentProfileID *int                      `json:"agent_profile_id,omitempty"`
	Title          string                    `json:"title"`
	ArchivedAt     *time.Time                `json:"archived_at,omitempty"`
	CreatedAt      time.Time                 `json:"created_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
	Participants   []AgentSessionParticipant `json:"participants,omitempty"`
}

type AgentSessionParticipant struct {
	UserID          int       `json:"user_id"`
	ParticipantRole string    `json:"participant_role"`
	DisplayName     string    `json:"display_name,omitempty"`
	Username        string    `json:"username,omitempty"`
	IsAgent         bool      `json:"is_agent"`
	JoinedAt        time.Time `json:"joined_at"`
}

type AgentMessage struct {
	ID           int       `json:"id"`
	SessionID    int       `json:"session_id"`
	Role         string    `json:"role"`
	AuthorUserID *int      `json:"author_user_id,omitempty"`
	AgentRunID   *int      `json:"agent_run_id,omitempty"`
	Content      string    `json:"content"`
	ContextJSON  string    `json:"-"`
	MetadataJSON string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}
