package jira

import "errors"

// Common Jira API errors
var (
	ErrInvalidCredentials                   = errors.New("invalid Jira credentials")
	ErrNotAuthenticated                     = errors.New("not authenticated to Jira")
	ErrRateLimited                          = errors.New("jira API rate limit exceeded")
	ErrNotFound                             = errors.New("jira resource not found")
	ErrForbidden                            = errors.New("access to Jira resource forbidden")
	ErrAPIError                             = errors.New("jira API error")
	ErrAssetsNotAvailable                   = errors.New("jira assets API not available")
	ErrWorkflowConfigurationNotAvailable    = errors.New("jira workflow configuration API not available")
	ErrScreenConfigurationNotAvailable      = errors.New("jira screen configuration API not available")
	ErrCustomFieldConfigurationNotAvailable = errors.New("jira custom field configuration API not available")
	ErrInvalidURL                           = errors.New("invalid Jira instance URL")
	ErrConnectionFailed                     = errors.New("failed to connect to Jira")
	ErrReadOnlyViolation                    = errors.New("jira import attempted a non-read-only request")
)
