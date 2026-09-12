package netbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"windshift/internal/models"
)

const (
	defaultLimit = 20
	maxLimit     = 50
	maxOffset    = 1000
	maxObjectID  = int64(1<<53 - 1) // Preserve exact numeric identity in browser JSON.
	fields       = "id,name,display,status,site,role,primary_ip4,primary_ip6"
)

type Response struct {
	StatusCode int
	Body       []byte
}

type Transport interface {
	Do(ctx context.Context, method, targetURL string, body []byte, headers map[string]string) (*Response, error)
}

type TransportFunc func(context.Context, string, string, []byte, map[string]string) (*Response, error)

func (f TransportFunc) Do(ctx context.Context, method, targetURL string, body []byte, headers map[string]string) (*Response, error) {
	return f(ctx, method, targetURL, body, headers)
}

type APIError struct{ StatusCode int }

func (e *APIError) Error() string { return fmt.Sprintf("NetBox API returned HTTP %d", e.StatusCode) }

type UpstreamError struct{ Cause error }

func (e *UpstreamError) Error() string { return "NetBox request failed" }
func (e *UpstreamError) Unwrap() error { return e.Cause }

// ValidationError identifies caller-controlled input errors without including
// credentials or parser details in its public message.
type ValidationError struct{ message string }

func (e *ValidationError) Error() string { return e.message }

func invalid(message string) error { return &ValidationError{message: message} }

type Client struct {
	baseURL   string
	authValue string
	transport Transport
}

func NewClient(baseURL, token string, scheme models.NetBoxAuthScheme, transport Transport) (*Client, error) {
	baseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if err := ValidateToken(token, scheme); err != nil {
		return nil, err
	}
	if transport == nil {
		transport = NewSafeTransport(baseURL)
	}
	prefix := "Bearer "
	if scheme == models.NetBoxAuthToken {
		prefix = "Token "
	}
	return &Client{baseURL: baseURL, authValue: prefix + token, transport: transport}, nil
}

func NormalizeBaseURL(raw string) (string, error) {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t#") {
		return "", invalid("invalid NetBox base URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(u.Host, ":") {
		return "", invalid("invalid NetBox base URL")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", invalid("invalid NetBox base URL port")
		}
	}
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.ContainsAny(u.Path, "\\%") || hasPercentControl(escaped) {
		return "", invalid("invalid NetBox base URL")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", invalid("invalid NetBox base URL")
		}
	}
	if u.Path != "" && strings.Contains(u.Path, "//") {
		return "", invalid("invalid NetBox base URL")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func hasPercentControl(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
		if err == nil && (v < 0x20 || v == 0x7f) {
			return true
		}
	}
	return false
}

var (
	bearerToken = regexp.MustCompile(`^nbt_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
	legacyToken = regexp.MustCompile(`^[A-Fa-f0-9]+$`)
)

func ValidateToken(token string, scheme models.NetBoxAuthScheme) error {
	if token == "" || len(token) > 4096 || strings.IndexFunc(token, func(r rune) bool { return r <= 0x20 || r == 0x7f }) >= 0 {
		return invalid("invalid NetBox API token")
	}
	switch scheme {
	case models.NetBoxAuthBearer:
		if !bearerToken.MatchString(token) {
			return invalid("invalid NetBox bearer token")
		}
	case models.NetBoxAuthToken:
		if !legacyToken.MatchString(token) {
			return invalid("invalid NetBox legacy token")
		}
	default:
		return invalid("invalid NetBox authentication scheme")
	}
	return nil
}

func (c *Client) Search(ctx context.Context, objectType models.NetBoxObjectType, q string, limit, offset int) (*models.NetBoxSearchResult, error) {
	q = strings.TrimSpace(q)
	if !utf8.ValidString(q) || utf8.RuneCountInString(q) < 2 || utf8.RuneCountInString(q) > 200 || strings.IndexFunc(q, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return nil, invalid("NetBox search query must contain 2 to 200 characters")
	}
	return c.search(ctx, objectType, q, limit, offset)
}

func (c *Client) search(ctx context.Context, objectType models.NetBoxObjectType, q string, limit, offset int) (*models.NetBoxSearchResult, error) {
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > maxLimit || offset < 0 || offset > maxOffset {
		return nil, invalid("invalid NetBox pagination")
	}
	endpoint, err := collectionPath(objectType)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("fields", fields)
	query.Set("limit", strconv.Itoa(limit))
	query.Set("offset", strconv.Itoa(offset))
	query.Set("ordering", "id")
	if q != "" {
		query.Set("q", q)
	}
	var page rawPage
	if err := c.get(ctx, endpoint+"?"+query.Encode(), &page); err != nil {
		return nil, err
	}
	if page.Count == nil || page.Results == nil {
		return nil, &UpstreamError{Cause: errors.New("missing pagination envelope")}
	}
	count, results := *page.Count, *page.Results
	if count < 0 || len(results) > limit || (len(results) > 0 && count < int64(offset+len(results))) {
		return nil, &UpstreamError{Cause: errors.New("invalid result bounds")}
	}
	result := &models.NetBoxSearchResult{Results: make([]models.NetBoxObject, 0, len(results))}
	for _, raw := range results {
		object, unknown, err := decodeObject(raw, objectType, c.baseURL)
		if err != nil {
			return nil, &UpstreamError{Cause: err}
		}
		result.Results = append(result.Results, *object)
		result.UnrequestedFields = result.UnrequestedFields || unknown
	}
	if int64(offset+len(result.Results)) < count && offset+len(result.Results) <= maxOffset && len(result.Results) > 0 {
		next := offset + len(result.Results)
		result.HasMore = true
		result.NextOffset = &next
	}
	return result, nil
}

func (c *Client) GetObject(ctx context.Context, objectType models.NetBoxObjectType, id int64) (*models.NetBoxObject, error) {
	if id <= 0 || id > maxObjectID {
		return nil, invalid("invalid NetBox object ID")
	}
	base, err := collectionPath(objectType)
	if err != nil {
		return nil, err
	}
	query := url.Values{"fields": []string{fields}}
	var raw map[string]json.RawMessage
	if err := c.get(ctx, base+strconv.FormatInt(id, 10)+"/?"+query.Encode(), &raw); err != nil {
		return nil, err
	}
	object, _, err := decodeObject(raw, objectType, c.baseURL)
	if err != nil || object.ObjectID != id {
		if err == nil {
			err = errors.New("NetBox returned a different object ID")
		}
		return nil, &UpstreamError{Cause: err}
	}
	return object, nil
}

func (c *Client) Test(ctx context.Context) (*models.NetBoxConnectionTestResult, error) {
	result := &models.NetBoxConnectionTestResult{OK: true, Warnings: []string{}}
	for _, objectType := range []models.NetBoxObjectType{models.NetBoxDevice, models.NetBoxVirtualMachine} {
		page, err := c.search(ctx, objectType, "", 1, 0)
		if err != nil {
			return nil, err
		}
		if page.UnrequestedFields {
			result.Warnings = append(result.Warnings, "NetBox ignored the requested field projection")
		}
	}
	return result, nil
}

func (c *Client) get(ctx context.Context, path string, target any) error {
	if err := ctx.Err(); err != nil {
		return &UpstreamError{Cause: err}
	}
	response, err := c.transport.Do(ctx, http.MethodGet, c.baseURL+path, nil, map[string]string{
		"Accept":        "application/json",
		"Authorization": c.authValue,
	})
	if err != nil {
		return &UpstreamError{Cause: err}
	}
	if response == nil {
		return &UpstreamError{Cause: errors.New("empty response")}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &APIError{StatusCode: response.StatusCode}
	}
	if len(response.Body) > maxResponseBody {
		return &UpstreamError{Cause: errors.New("response body too large")}
	}
	decoder := json.NewDecoder(strings.NewReader(string(response.Body)))
	if err := decoder.Decode(target); err != nil {
		return &UpstreamError{Cause: errors.New("invalid JSON response")}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return &UpstreamError{Cause: errors.New("invalid trailing JSON data")}
	}
	return nil
}

type rawPage struct {
	Count   *int64                        `json:"count"`
	Results *[]map[string]json.RawMessage `json:"results"`
}

func collectionPath(objectType models.NetBoxObjectType) (string, error) {
	switch objectType {
	case models.NetBoxDevice:
		return "/api/dcim/devices/", nil
	case models.NetBoxVirtualMachine:
		return "/api/virtualization/virtual-machines/", nil
	default:
		return "", invalid("invalid NetBox object type")
	}
}

func decodeObject(raw map[string]json.RawMessage, objectType models.NetBoxObjectType, baseURL string) (*models.NetBoxObject, bool, error) {
	allowed := map[string]bool{"id": true, "name": true, "display": true, "status": true, "site": true, "role": true, "primary_ip4": true, "primary_ip6": true}
	unknown := false
	for key := range raw {
		unknown = unknown || !allowed[key]
	}
	id, err := decodeID(raw["id"])
	if err != nil {
		return nil, unknown, err
	}
	name, err := scalar(raw["name"], 256, "name")
	if err != nil {
		return nil, unknown, err
	}
	if name == "" {
		name, err = scalar(raw["display"], 256, "display")
		if err != nil {
			return nil, unknown, err
		}
	}
	object := &models.NetBoxObject{ObjectType: objectType, ObjectID: id, ExternalID: string(objectType) + ":" + strconv.FormatInt(id, 10), Name: name}
	if objectType == models.NetBoxDevice {
		object.URL = baseURL + "/dcim/devices/" + strconv.FormatInt(id, 10) + "/"
	} else {
		object.URL = baseURL + "/virtualization/virtual-machines/" + strconv.FormatInt(id, 10) + "/"
	}
	for _, field := range []struct {
		key   string
		limit int
		dst   *string
	}{{"status", 256, &object.Status}, {"site", 256, &object.Site}, {"role", 256, &object.Role}, {"primary_ip4", 64, &object.PrimaryIPv4}, {"primary_ip6", 64, &object.PrimaryIPv6}} {
		*field.dst, err = scalar(raw[field.key], field.limit, field.key)
		if err != nil {
			return nil, unknown, err
		}
	}
	return object, unknown, nil
}

func decodeID(raw json.RawMessage) (int64, error) {
	var id int64
	if len(raw) == 0 || json.Unmarshal(raw, &id) != nil || id <= 0 || id > maxObjectID {
		return 0, errors.New("invalid NetBox object ID")
	}
	return id, nil
}

func scalar(raw json.RawMessage, maxLength int, field string) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			return "", fmt.Errorf("invalid %s", field)
		}
		for _, key := range []string{"name", "display", "label", "value", "address"} {
			if candidate, ok := nested[key]; ok && json.Unmarshal(candidate, &value) == nil && value != "" {
				break
			}
		}
	}
	if utf8.RuneCountInString(value) > maxLength || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("invalid %s", field)
	}
	return value, nil
}
