package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"windshift/internal/utils"

	"golang.org/x/time/rate"
)

// Client provides methods to interact with the Jira Cloud REST API
type Client interface {
	// Connection
	TestConnection(ctx context.Context) (*JiraInstanceInfo, error)

	// Projects
	ListProjects(ctx context.Context) ([]JiraProject, error)
	GetProject(ctx context.Context, projectKey string) (*JiraProject, error)

	// Jira Service Management
	ListServiceDesks(ctx context.Context) ([]JiraServiceDesk, error)
	ListServiceDeskRequestTypes(ctx context.Context, serviceDeskID string) ([]JiraServiceDeskRequestType, error)
	ListServiceDeskRequestComments(ctx context.Context, issueKey string) ([]JiraServiceDeskComment, error)
	ListServiceDeskOrganizations(ctx context.Context, serviceDeskID string) ([]JiraServiceDeskOrganization, error)
	ListServiceDeskOrganizationUsers(ctx context.Context, organizationID string) ([]JiraUser, error)

	// Issue Types & Fields
	ListIssueTypes(ctx context.Context) ([]JiraIssueType, error)
	GetProjectIssueTypes(ctx context.Context, projectKey string) ([]JiraIssueType, error)
	ListCustomFields(ctx context.Context) ([]JiraCustomField, error)
	GetProjectFields(ctx context.Context, projectIDs []string) ([]JiraCustomField, error)

	// Workflows & Statuses
	ListStatuses(ctx context.Context) ([]JiraStatus, error)
	GetStatusCategories(ctx context.Context) ([]JiraStatusCategory, error)
	GetProjectWorkflowScheme(ctx context.Context, projectKey string) (*JiraWorkflow, error)
	GetProjectIssueTypeStatuses(ctx context.Context, projectKey string) ([]JiraIssueTypeWithStatuses, error)

	// Issues (Legacy - uses deprecated GET /rest/api/3/search)
	SearchIssues(ctx context.Context, opts SearchOptions) (*SearchResult, error)
	GetIssue(ctx context.Context, issueKey string, expand []string) (*JiraIssue, error)
	GetIssueComments(ctx context.Context, issueKey string, startAt, maxResults int) (*JiraCommentContainer, error)
	GetIssueWorklogs(ctx context.Context, issueKey string, startAt, maxResults int) (*JiraWorklogContainer, error)
	GetIssueCount(ctx context.Context, projectKey string, openOnly bool) (int, error)

	// Issues (Enhanced - uses POST /rest/api/3/search/jql)
	SearchIssuesJQL(ctx context.Context, req JQLSearchRequest) (*JQLSearchResponse, error)
	BulkFetchIssues(ctx context.Context, req BulkFetchRequest) (*BulkFetchResponse, error)
	GetAllIssueKeys(ctx context.Context, jql string) ([]string, error)

	// Versions & Sprints
	GetProjectVersions(ctx context.Context, projectKey string) ([]JiraVersion, error)
	ListBoards(ctx context.Context, projectKey string) (*BoardListResult, error)
	GetBoardSprints(ctx context.Context, boardID int) (*SprintListResult, error)
	GetBoardConfiguration(ctx context.Context, boardID int) (*JiraBoardConfiguration, error)

	// Filters
	ListFilters(ctx context.Context, projectKey string) (*FilterSearchResult, error)
	GetFilter(ctx context.Context, filterID string) (*JiraFilter, error)

	// Attachments
	DownloadAttachment(ctx context.Context, attachmentURL string) (io.ReadCloser, string, error)

	// Users
	GetUserEmail(ctx context.Context, accountID string) (string, error)

	// Jira Assets (Insight) API
	ListObjectSchemas(ctx context.Context) ([]AssetObjectSchema, error)
	GetObjectSchema(ctx context.Context, schemaID string) (*AssetObjectSchema, error)
	ListObjectTypes(ctx context.Context, schemaID string) ([]AssetObjectType, error)
	GetObjectTypeAttributes(ctx context.Context, objectTypeID string) ([]AssetObjectAttribute, error)
	SearchObjects(ctx context.Context, opts ObjectSearchOptions) (*ObjectSearchResult, error)
	GetObjectCount(ctx context.Context, schemaID string) (int, error)
}

// Config contains configuration for the Jira client
type Config struct {
	InstanceURL     string         // e.g., https://company.atlassian.net or https://jira.company.com
	Email           string         // User email (Cloud) or username (Data Center) for Basic auth
	APIToken        string         // API token or password
	DeploymentType  DeploymentType // cloud or datacenter (default: cloud)
	RateLimitPerSec int            // Rate limit (default: 10 requests/second)
	Timeout         time.Duration  // HTTP timeout (default: 30 seconds)
}

// cloudClient implements the Client interface for Jira Cloud
type cloudClient struct {
	baseURL        string
	assetsURL      string
	agileURL       string
	serviceDeskURL string
	authHeader     string
	httpClient     *http.Client
	limiter        *rate.Limiter
}

// NewClient creates a new Jira API client.
// Returns a Cloud or Data Center client based on cfg.DeploymentType.
//
// For Cloud, NewClient runs a one-time auto-probe against the operator's
// instance URL to detect whether the supplied API token is a **scoped**
// Atlassian token or a **legacy unscoped** one, and picks the appropriate
// base URL. See cloudRoutingProbe for the algorithm and Atlassian's
// rationale.
func NewClient(cfg Config) (Client, error) {
	// Validate and normalize the instance URL
	baseURL := strings.TrimSuffix(cfg.InstanceURL, "/")
	if baseURL == "" {
		return nil, ErrInvalidURL
	}

	// Parse URL to validate it
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("%w: must use http or https", ErrInvalidURL)
	}

	// Create Basic auth header (email:token base64 encoded)
	if cfg.Email == "" || cfg.APIToken == "" {
		return nil, ErrInvalidCredentials
	}
	authString := cfg.Email + ":" + cfg.APIToken
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(authString))

	// Set defaults
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	rateLimit := cfg.RateLimitPerSec
	if rateLimit == 0 {
		rateLimit = 10
	}

	// The instance URL is operator-supplied and used as the base for every
	// request (plus the pre-auth tenant_info probe and attachment downloads).
	// Dial through the SSRF-safe dialer so a base URL — or a redirect — that
	// resolves to a private/internal host cannot receive the Basic-auth
	// credential. Redirect-following is preserved; each hop is re-checked.
	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: utils.SafeNetDialer(timeout).DialContext},
	}
	limiter := rate.NewLimiter(rate.Limit(rateLimit), rateLimit)

	// Return appropriate client based on deployment type
	if cfg.DeploymentType == DeploymentDataCenter {
		return &dataCenterClient{
			baseURL:        baseURL + "/rest/api/2", // Data Center uses API v2
			agileURL:       baseURL + "/rest/agile/1.0",
			serviceDeskURL: baseURL + "/rest/servicedeskapi",
			xrayURL:        baseURL + "/rest/raven/2.0/api",
			authHeader:     authHeader,
			httpClient:     httpClient,
			limiter:        limiter,
		}, nil
	}

	// Cloud: probe to pick site URL vs api.atlassian.com gateway.
	routing := cloudRoutingProbe(baseURL, authHeader, httpClient)
	return &cloudClient{
		baseURL:        routing.platformBase, // /rest/api/3 already appended by probe
		assetsURL:      routing.assetsBase,
		agileURL:       routing.agileBase,
		serviceDeskURL: routing.serviceDeskBase,
		authHeader:     authHeader,
		httpClient:     httpClient,
		limiter:        limiter,
	}, nil
}

// cloudRouting holds the resolved base URLs for a Cloud client. All three
// already include their respective REST path prefix so the caller can
// concatenate sub-paths directly.
type cloudRouting struct {
	platformBase    string // .../rest/api/3
	agileBase       string // .../rest/agile/1.0
	assetsBase      string // legacy .../rest/assets/1.0 or gateway .../workspace/{id}/v1
	serviceDeskBase string // .../rest/servicedeskapi
	viaGateway      bool   // chosen routing, for logging only
}

// cloudRoutingProbe selects the gateway for scoped tokens and the site URL for
// legacy tokens. Scoped tokens sent to a site URL appear anonymous, so it probes
// gateway /myself after discovering the cloud ID. Any discovery or probe failure
// falls back to the site URL, preserving legacy and private-network behavior.
func cloudRoutingProbe(siteURL, authHeader string, httpClient *http.Client) cloudRouting {
	siteRouting := cloudRouting{
		platformBase:    siteURL + "/rest/api/3",
		agileBase:       siteURL + "/rest/agile/1.0",
		assetsBase:      siteURL + "/rest/assets/1.0",
		serviceDeskBase: siteURL + "/rest/servicedeskapi",
		viaGateway:      false,
	}

	cloudID, err := discoverCloudID(siteURL, httpClient)
	if err != nil || cloudID == "" {
		slog.Debug("Jira cloud routing: tenant_info lookup failed, using site URL",
			slog.String("component", "jira"),
			slog.String("site_url", siteURL),
			slog.Any("error", err),
		)
		return siteRouting
	}

	gatewayBase := "https://api.atlassian.com/ex/jira/" + cloudID
	if !gatewayAuthProbe(gatewayBase, authHeader, httpClient) {
		slog.Info("Jira cloud routing: gateway probe declined, using site URL",
			slog.String("component", "jira"),
			slog.String("cloud_id", cloudID),
		)
		return siteRouting
	}

	slog.Info("Jira cloud routing: using api.atlassian.com gateway (scoped token detected)",
		slog.String("component", "jira"),
		slog.String("cloud_id", cloudID),
	)
	assetsBase, assetsErr := discoverAssetsWorkspaceBase(gatewayBase, authHeader, httpClient)
	if assetsErr != nil {
		slog.Info("Jira cloud routing: Assets workspace is unavailable",
			slog.String("component", "jira"),
			slog.Any("error", assetsErr),
		)
	}
	return cloudRouting{
		platformBase:    gatewayBase + "/rest/api/3",
		agileBase:       gatewayBase + "/rest/agile/1.0",
		assetsBase:      assetsBase,
		serviceDeskBase: gatewayBase + "/rest/servicedeskapi",
		viaGateway:      true,
	}
}

// discoverAssetsWorkspaceBase resolves the site-scoped Assets API base used by
// scoped Cloud tokens. Jira Service Management exposes the workspace identifier
// through a read-only discovery endpoint before the Assets API can be called.
func discoverAssetsWorkspaceBase(gatewayBase, authHeader string, httpClient *http.Client) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		gatewayBase+"/rest/servicedeskapi/assets/workspace?start=0&limit=100",
		http.NoBody,
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req) //nolint:gosec // gatewayBase is derived from Atlassian's cloud ID
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("assets workspace discovery HTTP %d", resp.StatusCode)
	}

	var page struct {
		Values []struct {
			WorkspaceID string `json:"workspaceId"`
			ID          string `json:"id"`
		} `json:"values"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return "", err
	}
	if len(page.Values) == 0 {
		return "", ErrAssetsNotAvailable
	}
	workspaceID := page.Values[0].WorkspaceID
	if workspaceID == "" {
		workspaceID = page.Values[0].ID
	}
	if workspaceID == "" {
		return "", fmt.Errorf("%w: workspace response has no identifier", ErrAssetsNotAvailable)
	}
	return gatewayBase + "/jsm/assets/workspace/" + url.PathEscape(workspaceID) + "/v1", nil
}

// discoverCloudID calls the public /_edge/tenant_info well-known endpoint,
// which returns the site's stable cloud identifier. The endpoint is
// unauthenticated.
func discoverCloudID(siteURL string, httpClient *http.Client) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", siteURL+"/_edge/tenant_info", http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req) //nolint:gosec // siteURL is operator-supplied Jira base URL
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tenant_info HTTP %d", resp.StatusCode)
	}
	var body struct {
		CloudID string `json:"cloudId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.CloudID, nil
}

// gatewayAuthProbe asks "does this token authenticate against the gateway?"
// by hitting /rest/api/3/myself. A 200 means scoped-token routing is in
// effect for this caller; anything else means it isn't (legacy token, or a
// scoped token that the gateway has rejected outright).
//
// /myself is the right probe here: it requires an authenticated identity,
// so the response distinguishes "auth succeeded" (200) from "auth was
// silently dropped to anonymous" (401). Other endpoints like /serverInfo
// return 200 even anonymously and would give false positives.
func gatewayAuthProbe(gatewayBase, authHeader string, httpClient *http.Client) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", gatewayBase+"/rest/api/3/myself", http.NoBody)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req) //nolint:gosec // gatewayBase derived from discovered cloudId
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// do performs an HTTP request with rate limiting
func (c *cloudClient) do(ctx context.Context, method, reqURL string, body interface{}) (*http.Response, error) {
	if err := validateReadOnlyRequest(method, reqURL); err != nil {
		return nil, err
	}

	// Wait for rate limiter
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, err
	}

	c.setHeaders(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req) //nolint:gosec // G704: URL constructed from configured Jira base URL
}

// validateReadOnlyRequest prevents the Jira importer from ever mutating the
// source instance. Jira exposes a few query operations as POST endpoints, so
// those exact paths are allowed while every other non-GET method is denied.
func validateReadOnlyRequest(method, reqURL string) error {
	switch method {
	case http.MethodGet, http.MethodHead:
		return nil
	case http.MethodPost:
		parsed, err := url.Parse(reqURL)
		if err != nil {
			return fmt.Errorf("%w: invalid request URL: %v", ErrReadOnlyViolation, err)
		}
		for _, suffix := range []string{
			"/rest/api/3/search/jql",
			"/rest/api/3/issue/bulkfetch",
			"/rest/assets/1.0/object/navlist/aql",
		} {
			if strings.HasSuffix(parsed.Path, suffix) {
				return nil
			}
		}
		if isAssetsWorkspaceAQLPath(parsed.Path) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s %s", ErrReadOnlyViolation, method, reqURL)
}

func isAssetsWorkspaceAQLPath(path string) bool {
	const marker = "/jsm/assets/workspace/"
	index := strings.Index(path, marker)
	if index < 0 {
		return false
	}
	parts := strings.Split(strings.Trim(path[index+len(marker):], "/"), "/")
	return len(parts) == 4 &&
		parts[0] != "" &&
		parts[1] == "v1" &&
		parts[2] == "object" &&
		parts[3] == "aql"
}

// setHeaders sets common headers for Jira API requests
func (c *cloudClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Accept", "application/json")
}

// handleErrorResponse handles non-2xx responses. Jira's response body is
// preserved on every branch — operators debugging "my token should work"
// need the upstream message (deprecated auth scheme, SSO required, account
// locked, etc.) rather than a bare sentinel error.
func (c *cloudClient) handleErrorResponse(resp *http.Response) error {
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("failed to read Jira error response body: %w", readErr)
	}
	snippet := truncateBody(body)

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w (jira said: %s)", ErrInvalidCredentials, snippet)
	case http.StatusForbidden:
		// Check for rate limiting
		if strings.Contains(string(body), "rate limit") {
			return ErrRateLimited
		}
		return fmt.Errorf("%w (jira said: %s)", ErrForbidden, snippet)
	case http.StatusNotFound:
		return fmt.Errorf("%w (jira said: %s)", ErrNotFound, snippet)
	case http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		return fmt.Errorf("%w: status %d - %s", ErrAPIError, resp.StatusCode, snippet)
	}
}

// truncateBody trims a Jira response body to a length safe for inclusion in
// error strings and logs. Jira HTML error pages can be huge; the first ~512
// bytes contain the diagnostic message in every case we've seen.
func truncateBody(b []byte) string {
	const maxLen = 512
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "(empty response body)"
	}
	if len(s) > maxLen {
		return s[:maxLen] + "…(truncated)"
	}
	return s
}

// ================================================================
// Connection Methods
// ================================================================

// TestConnection tests if the credentials are valid.
//
// Probe order is deliberate: /serverInfo first, /myself second. Atlassian's
// scoped API tokens (rolled out 2024) can grant read access to projects /
// fields / issues without granting read:me, so /myself returns 401 even
// though the token is perfectly usable for the importer. Probing with
// /serverInfo confirms credentials reach the instance without depending on
// the account-identity scope. /myself is then a best-effort enrichment for
// the human-readable connection label — failure is logged and ignored.
func (c *cloudClient) TestConnection(ctx context.Context) (*JiraInstanceInfo, error) {
	serverResp, err := c.do(ctx, "GET", c.baseURL+"/serverInfo", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	defer func() { _ = serverResp.Body.Close() }()

	if serverResp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(serverResp)
	}

	var serverInfo struct {
		BaseURL        string `json:"baseUrl"`
		Version        string `json:"version"`
		DeploymentType string `json:"deploymentType"`
		ServerTitle    string `json:"serverTitle"`
	}
	if err := json.NewDecoder(serverResp.Body).Decode(&serverInfo); err != nil {
		return nil, fmt.Errorf("decode serverInfo: %w", err)
	}

	info := &JiraInstanceInfo{
		DisplayName: serverInfo.ServerTitle,
		URL:         serverInfo.BaseURL,
	}
	if info.URL == "" {
		info.URL = c.baseURL
	}

	// Best-effort: enrich DisplayName with the authenticating user's name.
	// 401 here is expected for scoped tokens without read:me — do not fail
	// the whole TestConnection over it.
	if userResp, userErr := c.do(ctx, "GET", c.baseURL+"/myself", nil); userErr == nil {
		defer func() { _ = userResp.Body.Close() }()
		if userResp.StatusCode == http.StatusOK {
			var user JiraUser
			if decodeErr := json.NewDecoder(userResp.Body).Decode(&user); decodeErr == nil && user.DisplayName != "" {
				info.DisplayName = user.DisplayName
			}
		}
	}

	if info.DisplayName == "" {
		info.DisplayName = info.URL
	}
	return info, nil
}

// ================================================================
// Project Methods
// ================================================================

// ListProjects lists all projects accessible to the user
func (c *cloudClient) ListProjects(ctx context.Context) ([]JiraProject, error) {
	resp, err := c.do(ctx, "GET", c.baseURL+"/project?expand=description", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var projects []JiraProject
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return nil, err
	}
	return projects, nil
}

// GetProject gets details about a specific project
func (c *cloudClient) GetProject(ctx context.Context, projectKey string) (*JiraProject, error) {
	resp, err := c.do(ctx, "GET", c.baseURL+"/project/"+url.PathEscape(projectKey), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var project JiraProject
	if err := json.NewDecoder(resp.Body).Decode(&project); err != nil {
		return nil, err
	}
	return &project, nil
}

// ListServiceDesks lists every Jira Service Management portal visible to the
// importing account.
func (c *cloudClient) ListServiceDesks(ctx context.Context) ([]JiraServiceDesk, error) {
	var result []JiraServiceDesk
	for start := 0; ; {
		reqURL := fmt.Sprintf("%s/servicedesk?start=%d&limit=100", c.serviceDeskURL, start)
		resp, err := c.do(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			responseErr := c.handleErrorResponse(resp)
			_ = resp.Body.Close()
			return nil, responseErr
		}
		var page JiraServiceDeskPage[JiraServiceDesk]
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, page.Values...)
		if page.IsLastPage || len(page.Values) == 0 {
			return result, nil
		}
		start += len(page.Values)
	}
}

// ListServiceDeskRequestTypes returns the complete customer-facing request
// type catalog for one service desk.
func (c *cloudClient) ListServiceDeskRequestTypes(ctx context.Context, serviceDeskID string) ([]JiraServiceDeskRequestType, error) {
	var result []JiraServiceDeskRequestType
	for start := 0; ; {
		reqURL := fmt.Sprintf("%s/servicedesk/%s/requesttype?start=%d&limit=100",
			c.serviceDeskURL, url.PathEscape(serviceDeskID), start)
		resp, err := c.do(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			responseErr := c.handleErrorResponse(resp)
			_ = resp.Body.Close()
			return nil, responseErr
		}
		var page JiraServiceDeskPage[JiraServiceDeskRequestType]
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, page.Values...)
		if page.IsLastPage || len(page.Values) == 0 {
			return result, nil
		}
		start += len(page.Values)
	}
}

// ListServiceDeskRequestComments returns JSM's public/internal visibility
// metadata for every comment on a customer request.
func (c *cloudClient) ListServiceDeskRequestComments(ctx context.Context, issueKey string) ([]JiraServiceDeskComment, error) {
	var result []JiraServiceDeskComment
	for start := 0; ; {
		reqURL := fmt.Sprintf("%s/request/%s/comment?start=%d&limit=100",
			c.serviceDeskURL, url.PathEscape(issueKey), start)
		resp, err := c.do(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			responseErr := c.handleErrorResponse(resp)
			_ = resp.Body.Close()
			return nil, responseErr
		}
		var page JiraServiceDeskPage[JiraServiceDeskComment]
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, page.Values...)
		if page.IsLastPage || len(page.Values) == 0 {
			return result, nil
		}
		start += len(page.Values)
	}
}

// ListServiceDeskOrganizations returns organizations associated with one JSM
// service desk.
func (c *cloudClient) ListServiceDeskOrganizations(ctx context.Context, serviceDeskID string) ([]JiraServiceDeskOrganization, error) {
	var result []JiraServiceDeskOrganization
	for start := 0; ; {
		reqURL := fmt.Sprintf("%s/servicedesk/%s/organization?start=%d&limit=100",
			c.serviceDeskURL, url.PathEscape(serviceDeskID), start)
		resp, err := c.do(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			responseErr := c.handleErrorResponse(resp)
			_ = resp.Body.Close()
			return nil, responseErr
		}
		var page JiraServiceDeskPage[JiraServiceDeskOrganization]
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, page.Values...)
		if page.IsLastPage || len(page.Values) == 0 {
			return result, nil
		}
		start += len(page.Values)
	}
}

// ListServiceDeskOrganizationUsers returns every customer in a JSM
// organization.
func (c *cloudClient) ListServiceDeskOrganizationUsers(ctx context.Context, organizationID string) ([]JiraUser, error) {
	var result []JiraUser
	for start := 0; ; {
		reqURL := fmt.Sprintf("%s/organization/%s/user?start=%d&limit=100",
			c.serviceDeskURL, url.PathEscape(organizationID), start)
		resp, err := c.do(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			responseErr := c.handleErrorResponse(resp)
			_ = resp.Body.Close()
			return nil, responseErr
		}
		var page JiraServiceDeskPage[JiraUser]
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, page.Values...)
		if page.IsLastPage || len(page.Values) == 0 {
			return result, nil
		}
		start += len(page.Values)
	}
}

// ================================================================
// Issue Type Methods
// ================================================================

// ListIssueTypes lists all issue types in the instance
func (c *cloudClient) ListIssueTypes(ctx context.Context) ([]JiraIssueType, error) {
	resp, err := c.do(ctx, "GET", c.baseURL+"/issuetype", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var issueTypes []JiraIssueType
	if err := json.NewDecoder(resp.Body).Decode(&issueTypes); err != nil {
		return nil, err
	}
	return issueTypes, nil
}

// GetProjectIssueTypes gets issue types available in a project
func (c *cloudClient) GetProjectIssueTypes(ctx context.Context, projectKey string) ([]JiraIssueType, error) {
	resp, err := c.do(ctx, "GET", c.baseURL+"/project/"+url.PathEscape(projectKey)+"/statuses", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	// The response is an array of issue types with their statuses
	var issueTypeStatuses []struct {
		ID       string       `json:"id"`
		Name     string       `json:"name"`
		Subtask  bool         `json:"subtask"`
		Statuses []JiraStatus `json:"statuses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issueTypeStatuses); err != nil {
		return nil, err
	}

	issueTypes := make([]JiraIssueType, len(issueTypeStatuses))
	for i, its := range issueTypeStatuses {
		issueTypes[i] = JiraIssueType{
			ID:      its.ID,
			Name:    its.Name,
			Subtask: its.Subtask,
		}
	}
	return issueTypes, nil
}

// ================================================================
// Custom Field Methods
// ================================================================

// ListCustomFields lists all custom field definitions
func (c *cloudClient) ListCustomFields(ctx context.Context) ([]JiraCustomField, error) {
	resp, err := c.do(ctx, "GET", c.baseURL+"/field", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var fields []JiraCustomField
	if err := json.NewDecoder(resp.Body).Decode(&fields); err != nil {
		return nil, err
	}

	// Filter to only custom fields
	customFields := make([]JiraCustomField, 0)
	for _, f := range fields {
		if f.Custom {
			customFields = append(customFields, f)
		}
	}
	return customFields, nil
}

// GetProjectFields returns only custom fields used by specific projects
// Uses the stable GET /rest/api/3/field/search endpoint with projectIds filter
func (c *cloudClient) GetProjectFields(ctx context.Context, projectIDs []string) ([]JiraCustomField, error) {
	if len(projectIDs) == 0 {
		return nil, fmt.Errorf("at least one project ID is required")
	}

	// Build URL with project IDs and type=custom filter
	endpoint := c.baseURL + "/field/search?projectIds=" + strings.Join(projectIDs, ",") + "&type=custom"

	slog.Debug("GetProjectFields request", slog.String("component", "jira"), slog.String("url", endpoint))

	var allFields []JiraCustomField
	startAt := 0
	maxResults := 50

	for {
		paginatedEndpoint := fmt.Sprintf("%s&startAt=%d&maxResults=%d", endpoint, startAt, maxResults)

		resp, err := c.do(ctx, "GET", paginatedEndpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		slog.Debug("GetProjectFields response", slog.String("component", "jira"), slog.Int("status", resp.StatusCode), slog.Int("body_length", len(body)))

		if resp.StatusCode != http.StatusOK {
			bodyPreview := string(body)
			if len(bodyPreview) > 500 {
				bodyPreview = bodyPreview[:500] + "..."
			}
			slog.Debug("GetProjectFields error response", slog.String("component", "jira"), slog.String("body", bodyPreview))
			return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, bodyPreview)
		}

		// Parse paginated response
		var result struct {
			Values     []JiraCustomField `json:"values"`
			StartAt    int               `json:"startAt"`
			MaxResults int               `json:"maxResults"`
			Total      int               `json:"total"`
			IsLast     bool              `json:"isLast"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		allFields = append(allFields, result.Values...)

		// Check if we've fetched all fields
		if result.IsLast || len(result.Values) == 0 {
			break
		}
		startAt += len(result.Values)
	}

	return allFields, nil
}

// ================================================================
// Status Methods
// ================================================================

// ListStatuses lists all statuses in the instance
func (c *cloudClient) ListStatuses(ctx context.Context) ([]JiraStatus, error) {
	resp, err := c.do(ctx, "GET", c.baseURL+"/status", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var statuses []JiraStatus
	if err := json.NewDecoder(resp.Body).Decode(&statuses); err != nil {
		return nil, err
	}
	return statuses, nil
}

// GetStatusCategories gets all status categories
func (c *cloudClient) GetStatusCategories(ctx context.Context) ([]JiraStatusCategory, error) {
	resp, err := c.do(ctx, "GET", c.baseURL+"/statuscategory", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var categories []JiraStatusCategory
	if err := json.NewDecoder(resp.Body).Decode(&categories); err != nil {
		return nil, err
	}
	return categories, nil
}

// GetProjectWorkflowScheme gets the workflow scheme for a project
func (c *cloudClient) GetProjectWorkflowScheme(ctx context.Context, projectKey string) (*JiraWorkflow, error) {
	// Get project statuses which includes workflow information
	resp, err := c.do(ctx, "GET", c.baseURL+"/project/"+url.PathEscape(projectKey)+"/statuses", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var issueTypeStatuses []struct {
		ID       string       `json:"id"`
		Name     string       `json:"name"`
		Statuses []JiraStatus `json:"statuses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issueTypeStatuses); err != nil {
		return nil, err
	}

	// Collect unique statuses across all issue types
	statusMap := make(map[string]JiraStatus)
	for _, its := range issueTypeStatuses {
		for _, s := range its.Statuses {
			statusMap[s.ID] = s
		}
	}

	statuses := make([]JiraStatus, 0, len(statusMap))
	for _, s := range statusMap {
		statuses = append(statuses, s)
	}

	return &JiraWorkflow{
		Name:     projectKey + " Workflow",
		Statuses: statuses,
	}, nil
}

// GetProjectIssueTypeStatuses gets issue types with their available statuses for a project
func (c *cloudClient) GetProjectIssueTypeStatuses(ctx context.Context, projectKey string) ([]JiraIssueTypeWithStatuses, error) {
	resp, err := c.do(ctx, "GET", c.baseURL+"/project/"+url.PathEscape(projectKey)+"/statuses", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var result []JiraIssueTypeWithStatuses
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

// ================================================================
// Issue Methods
// ================================================================

// SearchIssues searches for issues using JQL
func (c *cloudClient) SearchIssues(ctx context.Context, opts SearchOptions) (*SearchResult, error) {
	// Build URL with query parameters
	params := url.Values{}
	if opts.JQL != "" {
		params.Set("jql", opts.JQL)
	}
	params.Set("startAt", fmt.Sprintf("%d", opts.StartAt))
	if opts.MaxResults > 0 {
		params.Set("maxResults", fmt.Sprintf("%d", opts.MaxResults))
	} else {
		params.Set("maxResults", "50")
	}
	if len(opts.Fields) > 0 {
		params.Set("fields", strings.Join(opts.Fields, ","))
	}
	if len(opts.Expand) > 0 {
		params.Set("expand", strings.Join(opts.Expand, ","))
	}

	resp, err := c.do(ctx, "GET", c.baseURL+"/search?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var result SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetIssue gets a single issue by key
func (c *cloudClient) GetIssue(ctx context.Context, issueKey string, expand []string) (*JiraIssue, error) {
	params := url.Values{}
	if len(expand) > 0 {
		params.Set("expand", strings.Join(expand, ","))
	}

	urlStr := c.baseURL + "/issue/" + url.PathEscape(issueKey)
	if len(params) > 0 {
		urlStr += "?" + params.Encode()
	}

	resp, err := c.do(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var issue JiraIssue
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

func (c *cloudClient) GetIssueComments(ctx context.Context, issueKey string, startAt, maxResults int) (*JiraCommentContainer, error) {
	params := url.Values{}
	params.Set("startAt", fmt.Sprintf("%d", startAt))
	if maxResults <= 0 {
		maxResults = 100
	}
	params.Set("maxResults", fmt.Sprintf("%d", maxResults))

	resp, err := c.do(ctx, "GET", c.baseURL+"/issue/"+url.PathEscape(issueKey)+"/comment?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}
	var result JiraCommentContainer
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *cloudClient) GetIssueWorklogs(ctx context.Context, issueKey string, startAt, maxResults int) (*JiraWorklogContainer, error) {
	params := url.Values{}
	params.Set("startAt", fmt.Sprintf("%d", startAt))
	if maxResults <= 0 {
		maxResults = 100
	}
	params.Set("maxResults", fmt.Sprintf("%d", maxResults))

	resp, err := c.do(ctx, "GET", c.baseURL+"/issue/"+url.PathEscape(issueKey)+"/worklog?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}
	var result JiraWorklogContainer
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetIssueCount gets the total number of issues in a project using the new JQL search endpoint
func (c *cloudClient) GetIssueCount(ctx context.Context, projectKey string, openOnly bool) (int, error) {
	jql := fmt.Sprintf("project = %s", projectKey)
	if openOnly {
		jql += " AND statusCategory != Done"
	}

	// Use the new POST /rest/api/3/search/jql endpoint
	// Request only the key field to minimize response size
	result, err := c.SearchIssuesJQL(ctx, JQLSearchRequest{
		JQL:        jql,
		MaxResults: 1, // We only need the total count
		Fields:     []string{"key"},
	})
	if err != nil {
		return 0, err
	}

	// If Total is returned, use it
	if result.Total > 0 {
		return result.Total, nil
	}

	// If Total is not returned (some Jira instances), we need to paginate and count
	// This is a fallback for when total is not available
	return c.countAllIssues(ctx, jql)
}

// countAllIssues counts issues by paginating through all results
// This is a fallback when the total field is not available
func (c *cloudClient) countAllIssues(ctx context.Context, jql string) (int, error) {
	count := 0
	nextPageToken := ""

	for {
		result, err := c.SearchIssuesJQL(ctx, JQLSearchRequest{
			JQL:           jql,
			MaxResults:    100, // Larger batches to count faster
			Fields:        []string{"key"},
			NextPageToken: nextPageToken,
		})
		if err != nil {
			return count, err
		}

		count += len(result.Issues)

		if result.NextPageToken == "" {
			break
		}
		nextPageToken = result.NextPageToken
	}

	return count, nil
}

// SearchIssuesJQL searches for issues using the new POST /rest/api/3/search/jql endpoint
func (c *cloudClient) SearchIssuesJQL(ctx context.Context, req JQLSearchRequest) (*JQLSearchResponse, error) {
	resp, err := c.do(ctx, "POST", c.baseURL+"/search/jql", req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var result JQLSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// BulkFetchIssues fetches multiple issues by their IDs or keys
// Uses POST /rest/api/3/issue/bulkfetch
func (c *cloudClient) BulkFetchIssues(ctx context.Context, req BulkFetchRequest) (*BulkFetchResponse, error) {
	resp, err := c.do(ctx, "POST", c.baseURL+"/issue/bulkfetch", req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var result BulkFetchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetAllIssueKeys retrieves all issue keys matching a JQL query
// Paginates through all results using nextPageToken
func (c *cloudClient) GetAllIssueKeys(ctx context.Context, jql string) ([]string, error) {
	var keys []string
	nextPageToken := ""

	for {
		result, err := c.SearchIssuesJQL(ctx, JQLSearchRequest{
			JQL:           jql,
			MaxResults:    100, // Fetch 100 at a time
			Fields:        []string{"key"},
			NextPageToken: nextPageToken,
		})
		if err != nil {
			return keys, err
		}

		for _, issue := range result.Issues {
			keys = append(keys, issue.Key)
		}

		if result.NextPageToken == "" {
			break
		}
		nextPageToken = result.NextPageToken
	}

	return keys, nil
}

// ================================================================
// Version & Sprint Methods
// ================================================================

// GetProjectVersions gets all versions for a project
func (c *cloudClient) GetProjectVersions(ctx context.Context, projectKey string) ([]JiraVersion, error) {
	resp, err := c.do(ctx, "GET", c.baseURL+"/project/"+url.PathEscape(projectKey)+"/versions", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var versions []JiraVersion
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return nil, err
	}
	return versions, nil
}

// ListBoards lists all Agile boards for a project.
//
// Jira's Agile API is paginated. Returning only the first page would silently
// miss boards on larger projects, which in turn drops every sprint that lives on
// later boards from the Windshift iteration import.
func (c *cloudClient) ListBoards(ctx context.Context, projectKey string) (*BoardListResult, error) {
	const defaultMaxResults = 50

	aggregate := &BoardListResult{MaxResults: defaultMaxResults, IsLast: true}
	for startAt := 0; ; {
		params := url.Values{}
		params.Set("startAt", fmt.Sprintf("%d", startAt))
		params.Set("maxResults", fmt.Sprintf("%d", defaultMaxResults))
		if projectKey != "" {
			params.Set("projectKeyOrId", projectKey)
		}

		resp, err := c.do(ctx, "GET", c.agileURL+"/board?"+params.Encode(), nil)
		if err != nil {
			return nil, err
		}

		var page BoardListResult
		decodeErr := func() error {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return c.handleErrorResponse(resp)
			}
			return json.NewDecoder(resp.Body).Decode(&page)
		}()
		if decodeErr != nil {
			return nil, decodeErr
		}

		aggregate.Values = append(aggregate.Values, page.Values...)
		aggregate.Total = page.Total
		aggregate.IsLast = page.IsLast
		if page.MaxResults > 0 {
			aggregate.MaxResults = page.MaxResults
		}
		if page.IsLast || len(page.Values) == 0 || (page.Total > 0 && len(aggregate.Values) >= page.Total) {
			break
		}
		if page.StartAt+len(page.Values) > startAt {
			startAt = page.StartAt + len(page.Values)
		} else {
			startAt += defaultMaxResults
		}
	}
	return aggregate, nil
}

// GetBoardConfiguration gets Agile board columns, status mappings, and backing filter metadata.
func (c *cloudClient) GetBoardConfiguration(ctx context.Context, boardID int) (*JiraBoardConfiguration, error) {
	resp, err := c.do(ctx, "GET", fmt.Sprintf("%s/board/%d/configuration", c.agileURL, boardID), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}
	var config JiraBoardConfiguration
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

// ListFilters lists saved filters associated with a project.
func (c *cloudClient) ListFilters(ctx context.Context, projectKey string) (*FilterSearchResult, error) {
	project, err := c.GetProject(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	const defaultMaxResults = 50
	aggregate := &FilterSearchResult{MaxResults: defaultMaxResults, IsLast: true}
	for startAt := 0; ; {
		params := url.Values{}
		params.Set("startAt", fmt.Sprintf("%d", startAt))
		params.Set("maxResults", fmt.Sprintf("%d", defaultMaxResults))
		params.Set("expand", "jql,description,owner,viewUrl")
		if project != nil && strings.TrimSpace(project.ID) != "" {
			params.Set("projectId", project.ID)
		}

		resp, err := c.do(ctx, "GET", c.baseURL+"/filter/search?"+params.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var page FilterSearchResult
		decodeErr := func() error {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return c.handleErrorResponse(resp)
			}
			return json.NewDecoder(resp.Body).Decode(&page)
		}()
		if decodeErr != nil {
			return nil, decodeErr
		}

		aggregate.Values = append(aggregate.Values, page.Values...)
		aggregate.Total = page.Total
		aggregate.IsLast = page.IsLast
		if page.MaxResults > 0 {
			aggregate.MaxResults = page.MaxResults
		}
		if page.IsLast || len(page.Values) == 0 || (page.Total > 0 && len(aggregate.Values) >= page.Total) {
			break
		}
		if page.StartAt+len(page.Values) > startAt {
			startAt = page.StartAt + len(page.Values)
		} else {
			startAt += defaultMaxResults
		}
	}
	return aggregate, nil
}

// GetFilter gets a saved filter with expanded JQL where available.
func (c *cloudClient) GetFilter(ctx context.Context, filterID string) (*JiraFilter, error) {
	params := url.Values{}
	params.Set("expand", "jql,description,owner,viewUrl")
	resp, err := c.do(ctx, "GET", c.baseURL+"/filter/"+url.PathEscape(filterID)+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}
	var filter JiraFilter
	if err := json.NewDecoder(resp.Body).Decode(&filter); err != nil {
		return nil, err
	}
	return &filter, nil
}

// GetBoardSprints gets all sprints for a board.
//
// Jira embeds sprint results in pages too; importers need the full set so issue
// sprint custom fields can always resolve to a Windshift iteration.
func (c *cloudClient) GetBoardSprints(ctx context.Context, boardID int) (*SprintListResult, error) {
	const defaultMaxResults = 50

	aggregate := &SprintListResult{MaxResults: defaultMaxResults, IsLast: true}
	for startAt := 0; ; {
		params := url.Values{}
		params.Set("startAt", fmt.Sprintf("%d", startAt))
		params.Set("maxResults", fmt.Sprintf("%d", defaultMaxResults))

		resp, err := c.do(ctx, "GET", fmt.Sprintf("%s/board/%d/sprint?%s", c.agileURL, boardID, params.Encode()), nil)
		if err != nil {
			return nil, err
		}

		var page SprintListResult
		decodeErr := func() error {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return c.handleErrorResponse(resp)
			}
			return json.NewDecoder(resp.Body).Decode(&page)
		}()
		if decodeErr != nil {
			return nil, decodeErr
		}

		aggregate.Values = append(aggregate.Values, page.Values...)
		aggregate.Total = page.Total
		aggregate.IsLast = page.IsLast
		if page.MaxResults > 0 {
			aggregate.MaxResults = page.MaxResults
		}
		if page.IsLast || len(page.Values) == 0 || (page.Total > 0 && len(aggregate.Values) >= page.Total) {
			break
		}
		if page.StartAt+len(page.Values) > startAt {
			startAt = page.StartAt + len(page.Values)
		} else {
			startAt += defaultMaxResults
		}
	}
	return aggregate, nil
}

// ================================================================
// Attachment Methods
// ================================================================

// DownloadAttachment downloads an attachment and returns the reader and content type
func (c *cloudClient) DownloadAttachment(ctx context.Context, attachmentURL string) (io.ReadCloser, string, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", attachmentURL, http.NoBody)
	if err != nil {
		return nil, "", err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: attachment URL from trusted Jira API response
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", c.handleErrorResponse(resp)
	}

	contentType := resp.Header.Get("Content-Type")
	return resp.Body, contentType, nil
}

// ================================================================
// User Methods
// ================================================================

// GetUserEmail fetches a user's email address by account ID
// This is needed because Jira Cloud omits email addresses from standard API responses
// Reference: https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-users/#api-rest-api-3-user-email-get
func (c *cloudClient) GetUserEmail(ctx context.Context, accountID string) (string, error) {
	if accountID == "" {
		return "", nil
	}

	resp, err := c.do(ctx, "GET", c.baseURL+"/user/email?accountId="+url.QueryEscape(accountID), nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	// 404 means user not found or email not available - return empty string, not error
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	// 403 means the user doesn't have permission to view emails
	if resp.StatusCode == http.StatusForbidden {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", c.handleErrorResponse(resp)
	}

	var result UserEmailResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Email, nil
}

// ================================================================
// Jira Assets (Insight) Methods
// ================================================================

// ListObjectSchemas lists all object schemas in Assets
func (c *cloudClient) ListObjectSchemas(ctx context.Context) ([]AssetObjectSchema, error) {
	if c.assetsURL == "" {
		return nil, ErrAssetsNotAvailable
	}
	if strings.Contains(c.assetsURL, "/jsm/assets/workspace/") {
		return c.listCurrentObjectSchemas(ctx)
	}

	resp, err := c.do(ctx, http.MethodGet, c.assetsURL+"/objectschema/list", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrAssetsNotAvailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var result struct {
		ObjectSchemas []AssetObjectSchema `json:"objectschemas"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.ObjectSchemas, nil
}

func (c *cloudClient) listCurrentObjectSchemas(ctx context.Context) ([]AssetObjectSchema, error) {
	const pageSize = 100
	var schemas []AssetObjectSchema
	for startAt := 0; ; {
		query := url.Values{}
		query.Set("startAt", fmt.Sprintf("%d", startAt))
		query.Set("maxResults", fmt.Sprintf("%d", pageSize))
		query.Set("includeCounts", "true")
		resp, err := c.do(
			ctx,
			http.MethodGet,
			c.assetsURL+"/objectschema/list?"+query.Encode(),
			nil,
		)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return nil, ErrAssetsNotAvailable
		}
		if resp.StatusCode != http.StatusOK {
			responseErr := c.handleErrorResponse(resp)
			_ = resp.Body.Close()
			return nil, responseErr
		}

		var page struct {
			Values     []AssetObjectSchema `json:"values"`
			StartAt    int                 `json:"startAt"`
			MaxResults int                 `json:"maxResults"`
			Total      int                 `json:"total"`
			IsLast     bool                `json:"isLast"`
			Last       bool                `json:"last"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		schemas = append(schemas, page.Values...)
		next := page.StartAt + len(page.Values)
		if page.IsLast || page.Last || len(page.Values) == 0 || (page.Total > 0 && next >= page.Total) {
			return schemas, nil
		}
		startAt = next
	}
}

// GetObjectSchema gets a single object schema by ID
func (c *cloudClient) GetObjectSchema(ctx context.Context, schemaID string) (*AssetObjectSchema, error) {
	resp, err := c.do(ctx, "GET", c.assetsURL+"/objectschema/"+url.PathEscape(schemaID), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var schema AssetObjectSchema
	if err := json.NewDecoder(resp.Body).Decode(&schema); err != nil {
		return nil, err
	}
	return &schema, nil
}

// ListObjectTypes lists all object types in a schema
func (c *cloudClient) ListObjectTypes(ctx context.Context, schemaID string) ([]AssetObjectType, error) {
	resp, err := c.do(ctx, "GET", c.assetsURL+"/objectschema/"+url.PathEscape(schemaID)+"/objecttypes/flat", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var types []AssetObjectType
	if err := json.NewDecoder(resp.Body).Decode(&types); err != nil {
		return nil, err
	}
	return types, nil
}

// GetObjectTypeAttributes gets all attributes for an object type
func (c *cloudClient) GetObjectTypeAttributes(ctx context.Context, objectTypeID string) ([]AssetObjectAttribute, error) {
	resp, err := c.do(ctx, "GET", c.assetsURL+"/objecttype/"+url.PathEscape(objectTypeID)+"/attributes", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var attrs []AssetObjectAttribute
	if err := json.NewDecoder(resp.Body).Decode(&attrs); err != nil {
		return nil, err
	}
	normalizeAssetDefaultTypes(attrs)
	return attrs, nil
}

// SearchObjects searches for objects in a schema
func (c *cloudClient) SearchObjects(ctx context.Context, opts ObjectSearchOptions) (*ObjectSearchResult, error) {
	if strings.Contains(c.assetsURL, "/jsm/assets/workspace/") {
		return c.searchCurrentObjects(ctx, opts)
	}

	// Build the request body for object search
	reqBody := map[string]interface{}{
		"objectSchemaId":    opts.ObjectSchemaID,
		"page":              opts.Page,
		"resultsPerPage":    opts.PageSize,
		"includeAttributes": opts.IncludeAttributes,
	}
	if opts.ObjectTypeID != "" {
		reqBody["objectTypeId"] = opts.ObjectTypeID
	}
	if opts.IQL != "" {
		reqBody["iql"] = opts.IQL
	}

	resp, err := c.do(ctx, "POST", c.assetsURL+"/object/navlist/aql", reqBody)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var result ObjectSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *cloudClient) searchCurrentObjects(ctx context.Context, opts ObjectSearchOptions) (*ObjectSearchResult, error) {
	page := opts.Page
	if page < 1 {
		page = 1
	}
	pageSize := opts.PageSize
	if pageSize < 1 {
		pageSize = 100
	}
	startAt := (page - 1) * pageSize

	clauses := []string{
		"objectSchemaId = " + quoteAssetsAQLID(opts.ObjectSchemaID),
	}
	if opts.ObjectTypeID != "" {
		clauses = append(clauses, "objectTypeId = "+quoteAssetsAQLID(opts.ObjectTypeID))
	}
	if strings.TrimSpace(opts.IQL) != "" {
		clauses = append(clauses, "("+strings.TrimSpace(opts.IQL)+")")
	}

	query := url.Values{}
	query.Set("startAt", fmt.Sprintf("%d", startAt))
	query.Set("maxResults", fmt.Sprintf("%d", pageSize))
	query.Set("includeAttributes", fmt.Sprintf("%t", opts.IncludeAttributes))
	reqBody := map[string]string{"qlQuery": strings.Join(clauses, " AND ")}

	resp, err := c.do(
		ctx,
		http.MethodPost,
		c.assetsURL+"/object/aql?"+query.Encode(),
		reqBody,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleErrorResponse(resp)
	}

	var current struct {
		Values               []AssetObject          `json:"values"`
		ObjectTypeAttributes []AssetObjectAttribute `json:"objectTypeAttributes"`
		MaxResults           int                    `json:"maxResults"`
		StartAt              int                    `json:"startAt"`
		Total                int                    `json:"total"`
		IsLast               bool                   `json:"isLast"`
		HasMoreResults       bool                   `json:"hasMoreResults"`
		Last                 bool                   `json:"last"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&current); err != nil {
		return nil, err
	}
	normalizeAssetDefaultTypes(current.ObjectTypeAttributes)
	return &ObjectSearchResult{
		ObjectEntries:        current.Values,
		ObjectTypeAttributes: current.ObjectTypeAttributes,
		PageNumber:           page,
		PageSize:             current.MaxResults,
		TotalFilterCount:     current.Total,
		StartIndex:           current.StartAt,
		ToIndex:              current.StartAt + len(current.Values),
		IsLast:               current.IsLast || current.Last || !current.HasMoreResults,
	}, nil
}

func quoteAssetsAQLID(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return r < '0' || r > '9'
	}) == -1 {
		return value
	}
	escaped := strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`)
	return `"` + escaped + `"`
}

func normalizeAssetDefaultTypes(attributes []AssetObjectAttribute) {
	for index := range attributes {
		if attributes[index].DefaultType != nil {
			attributes[index].DefaultTypeID = attributes[index].DefaultType.ID
		}
	}
}

// GetObjectCount gets the total number of objects in a schema
func (c *cloudClient) GetObjectCount(ctx context.Context, schemaID string) (int, error) {
	schema, err := c.GetObjectSchema(ctx, schemaID)
	if err != nil {
		return 0, err
	}
	return schema.ObjectCount, nil
}
