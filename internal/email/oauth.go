package email

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"windshift/internal/utils"
)

const maxOAuthResponseBytes = 1 << 20

func readOAuthResponseBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxOAuthResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxOAuthResponseBytes {
		return nil, fmt.Errorf("OAuth response exceeds %d bytes", maxOAuthResponseBytes)
	}
	return data, nil
}

// fetchOAuthJSON performs an authenticated GET request using a bearer token and
// decodes the JSON response into the provided target. This is the common pattern
// used by provider-specific GetUserEmail implementations.
func fetchOAuthJSON(ctx context.Context, endpoint, accessToken string, target any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create user info request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	httpClient := utils.NewHTTPClient(30 * time.Second)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("user info request failed: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := readOAuthResponseBody(resp.Body)
	if readErr != nil {
		return fmt.Errorf("failed to read user info response: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("user info request failed with status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("failed to parse user info: %w", err)
	}

	return nil
}

// exchangeOAuthToken performs an OAuth token exchange HTTP request and parses the response.
// It handles both authorization code exchange and refresh token flows.
func exchangeOAuthToken(ctx context.Context, tokenURL string, params url.Values) (*OAuthTokens, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := utils.NewHTTPClient(30 * time.Second)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readOAuthResponseBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &errResp)
		return nil, fmt.Errorf("token exchange failed: %s - %s", errResp.Error, errResp.ErrorDescription)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}

	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("token response did not include an access token")
	}

	tokens := &OAuthTokens{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		Scope:        tokenResp.Scope,
	}
	if tokenResp.ExpiresIn > 0 {
		expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		tokens.ExpiresAt = &expiresAt
	}

	return tokens, nil
}
