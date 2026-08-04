// Package todoist provides an OAuth client for the Todoist integration.
package todoist

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"windshift/internal/utils"
)

const (
	authorizeURL   = "https://todoist.com/oauth/authorize"
	tokenURL       = "https://todoist.com/oauth/access_token" // #nosec G101 -- OAuth token endpoint URL, not a credential
	requestTimeout = 10 * time.Second
)

// OAuthResult holds the result of a Todoist OAuth token exchange.
type OAuthResult struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// ExchangeOAuthCode trades an authorization code for an access token.
func ExchangeOAuthCode(clientID, clientSecret, code string) (*OAuthResult, error) {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
	}
	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := utils.NewHTTPClient(requestTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchanging code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("todoist oauth error (status %d): %s", resp.StatusCode, string(body))
	}

	var out OAuthResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("todoist oauth response missing access_token")
	}
	return &out, nil
}

// AuthorizeURL returns the URL users are sent to in order to grant consent.
// Todoist enforces the redirect URI from the app console; it is not passed here.
func AuthorizeURL(clientID, scope, state string) string {
	q := url.Values{
		"client_id": {clientID},
		"scope":     {scope},
		"state":     {state},
	}
	return authorizeURL + "?" + q.Encode()
}
