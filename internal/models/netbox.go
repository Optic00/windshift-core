package models

import "time"

const IntegrationProviderNetBox IntegrationProviderType = "netbox"

type NetBoxObjectType string

const (
	NetBoxDevice         NetBoxObjectType = "dcim.device"
	NetBoxVirtualMachine NetBoxObjectType = "virtualization.virtualmachine"
)

type NetBoxAuthScheme string

const (
	NetBoxAuthBearer NetBoxAuthScheme = "bearer"
	NetBoxAuthToken  NetBoxAuthScheme = "token"
)

// NetBoxConnection shares provider identity and managed credential scope. No
// credential material, including token prefixes, is exposed in its response.
type NetBoxConnection struct {
	ProviderID             string           `json:"id"`
	Slug                   string           `json:"slug"`
	Name                   string           `json:"name"`
	Enabled                bool             `json:"enabled"`
	BaseURL                string           `json:"base_url"`
	AuthScheme             NetBoxAuthScheme `json:"auth_scheme"`
	ConfigRevision         int64            `json:"revision"`
	CredentialID           int              `json:"-"`
	CredentialEnabled      bool             `json:"-"`
	HasAPIToken            bool             `json:"has_api_token"`
	AppliesToAllWorkspaces bool             `json:"applies_to_all_workspaces"`
	WorkspaceIDs           []int            `json:"workspace_ids"`
	CreatedBy              *int             `json:"-"`
	CreatedAt              time.Time        `json:"created_at"`
	UpdatedAt              time.Time        `json:"updated_at"`
}

type CreateNetBoxConnectionRequest struct {
	Slug                   string           `json:"slug"`
	Name                   string           `json:"name"`
	Enabled                *bool            `json:"enabled,omitempty"`
	BaseURL                string           `json:"base_url"`
	AuthScheme             NetBoxAuthScheme `json:"auth_scheme"`
	APIToken               string           `json:"api_token"`
	AppliesToAllWorkspaces *bool            `json:"applies_to_all_workspaces"`
	WorkspaceIDs           []int            `json:"workspace_ids"`
}

type UpdateNetBoxConnectionRequest struct {
	Revision               int64             `json:"revision"`
	Name                   *string           `json:"name,omitempty"`
	Enabled                *bool             `json:"enabled,omitempty"`
	APIToken               *string           `json:"api_token,omitempty"`
	AppliesToAllWorkspaces *bool             `json:"applies_to_all_workspaces,omitempty"`
	WorkspaceIDs           *[]int            `json:"workspace_ids,omitempty"`
	BaseURL                *string           `json:"base_url,omitempty"`
	AuthScheme             *NetBoxAuthScheme `json:"auth_scheme,omitempty"`
}

type NetBoxWorkspaceConnection struct {
	ProviderID string `json:"id"`
	Name       string `json:"name"`
}

// NetBoxObject is an explicit data projection, never a raw API response. URLs
// are generated from the configured origin, not copied from remote fields.
type NetBoxObject struct {
	ObjectType  NetBoxObjectType `json:"object_type"`
	ObjectID    int64            `json:"object_id"`
	ExternalID  string           `json:"external_id"`
	Name        string           `json:"name"`
	URL         string           `json:"url"`
	Status      string           `json:"status,omitempty"`
	Site        string           `json:"site,omitempty"`
	Role        string           `json:"role,omitempty"`
	PrimaryIPv4 string           `json:"primary_ip4,omitempty"`
	PrimaryIPv6 string           `json:"primary_ip6,omitempty"`
}

type NetBoxSearchResult struct {
	Results           []NetBoxObject `json:"results"`
	HasMore           bool           `json:"has_more"`
	NextOffset        *int           `json:"next_offset"`
	UnrequestedFields bool           `json:"-"`
}

type NetBoxConnectionTestResult struct {
	OK       bool     `json:"ok"`
	Warnings []string `json:"warnings"`
}

type NetBoxItemLink struct {
	ID                string       `json:"id"`
	ItemID            int          `json:"item_id"`
	ProviderID        string       `json:"connection_id"`
	ConnectionName    string       `json:"connection_name"`
	Object            NetBoxObject `json:"object"`
	Revision          int64        `json:"revision"`
	SnapshotUpdatedAt time.Time    `json:"snapshot_updated_at"`
	CreatedBy         *int         `json:"-"`
	CreatedAt         time.Time    `json:"created_at"`
}

type CreateNetBoxItemLinkRequest struct {
	ConnectionID string           `json:"connection_id"`
	ObjectType   NetBoxObjectType `json:"object_type"`
	ObjectID     int64            `json:"object_id"`
}
