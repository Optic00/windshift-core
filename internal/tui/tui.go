// Package tui wires an authenticated SSH session to the Bubble Tea app:
// per-connection token minting/cleanup and construction of the shared
// context + root model. The UI itself lives in the sub-packages (app, core,
// screens, dialog, components, data, styles).
package tui

import (
	"fmt"
	"log/slog"
	"time"

	"windshift/internal/auth"
	"windshift/internal/middleware"
	"windshift/internal/models"
	"windshift/internal/tui/app"
	"windshift/internal/tui/core"
	"windshift/internal/tui/data"
	"windshift/internal/tui/screens/workspaces"
	"windshift/internal/tui/styles"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/ssh"
)

// tuiTokenLifetime caps the SSH-minted API token at 24h, matching the SSH
// session's wish.WithMaxTimeout in main.go. The cleanup goroutine below
// usually deletes the token well before this on connection close.
const tuiTokenLifetime = 24 * time.Hour

// tuiTokenScopes is the minimal v1 permission set the TUI's APIClient needs:
// workspaces:read covers workspace list/get + workspace-scoped statuses;
// items:read/write covers item CRUD + comments (which live under /items/{id}).
// priorities:read covers the priority list. No admin or delete scopes.
var tuiTokenScopes = []string{
	"workspaces:read",
	"items:read",
	"items:write",
	"priorities:read",
	"users:read",             // /users/me + /workspaces/{id}/assignable-users (assignee picker)
	"user-preferences:read",  // theme / split / last-workspace persistence
	"user-preferences:write", // (see /users/me/tui-preferences)
}

// NewTUIHandler creates a new TUI handler for SSH sessions.
//
// The handler mints two pieces of auth state for each connection:
//   - A session via sessionManager (for legacy /api/* endpoints — currently
//     only the time-logging screen, until v1 grows time endpoints).
//   - A bearer API token via tokenManager (for /api/rest/api/v1/* endpoints —
//     the rest of the TUI).
//
// Both are deleted in a single goroutine when the SSH context cancels so
// rows don't accumulate across disconnects.
func NewTUIHandler(apiURL string, sessionManager *auth.SessionManager, tokenManager *auth.TokenManager) func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	return func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
		// Extract authenticated user information from SSH context
		var userInfo *data.UserInfo

		var sessionToken string
		var bearerToken string
		var sessionTokenForCleanup string
		var apiTokenIDForCleanup int
		var apiTokenUserIDForCleanup int

		if middleware.IsAuthenticated(s.Context()) {
			userID, _ := middleware.GetAuthenticatedUserID(s.Context())
			credentialID, credentialName, _ := middleware.GetCredentialInfo(s.Context())
			email, username, firstName, lastName, _ := middleware.GetUserInfo(s.Context())

			userInfo = &data.UserInfo{
				UserID:         userID,
				CredentialID:   credentialID,
				CredentialName: credentialName,
				RemoteAddr:     s.RemoteAddr().String(),
				Email:          email,
				Username:       username,
				FirstName:      firstName,
				LastName:       lastName,
			}

			// Create a session for the legacy /api/* endpoints the TUI still
			// hits (time-logging). v1 endpoints use the bearer token below.
			if sessionManager != nil {
				remoteAddr := s.RemoteAddr().String()
				userAgent := fmt.Sprintf("SSH TUI (%s via %s)", username, credentialName)
				session, err := sessionManager.CreateSession(userID, remoteAddr, userAgent, false)
				if err != nil {
					slog.Error("failed to create session",
						slog.String("component", "tui"),
						slog.Int("user_id", userID),
						slog.Any("error", err))
				} else {
					sessionToken = session.Token
					sessionTokenForCleanup = session.Token
					slog.Debug("created session for SSH TUI",
						slog.String("component", "tui"),
						slog.Int("user_id", userID),
						slog.String("username", username),
						slog.Int("session_id", session.ID))
				}
			}

			// Mint a short-lived API token for the v1 endpoints. The narrow
			// scope list reflects exactly what the TUI's APIClient calls.
			if tokenManager != nil {
				exp := time.Now().Add(tuiTokenLifetime)
				resp, err := tokenManager.CreateToken(userID, models.APITokenCreate{
					Name:        fmt.Sprintf("ssh-tui:%s", data.SanitizeLine(credentialName)),
					Permissions: tuiTokenScopes,
					ExpiresAt:   &exp,
					IsTemporary: true,
				})
				if err != nil {
					slog.Error("failed to mint api token",
						slog.String("component", "tui"),
						slog.Int("user_id", userID),
						slog.Any("error", err))
				} else {
					bearerToken = resp.Token
					apiTokenIDForCleanup = resp.APIToken.ID
					apiTokenUserIDForCleanup = userID
					slog.Debug("minted bearer token for SSH TUI",
						slog.String("component", "tui"),
						slog.Int("user_id", userID),
						slog.Int("token_id", resp.APIToken.ID),
						slog.String("token_prefix", resp.APIToken.TokenPrefix))
				}
			}

			// Single cleanup goroutine — fires on SSH disconnect.
			if sessionTokenForCleanup != "" || apiTokenIDForCleanup != 0 {
				sctx := s.Context()
				go func() {
					<-sctx.Done()
					if sessionTokenForCleanup != "" {
						if err := sessionManager.DeleteSession(sessionTokenForCleanup); err != nil {
							slog.Warn("failed to delete SSH session on disconnect",
								slog.String("component", "tui"),
								slog.Any("error", err))
						}
					}
					if apiTokenIDForCleanup != 0 {
						if err := tokenManager.RevokeToken(apiTokenIDForCleanup, apiTokenUserIDForCleanup); err != nil {
							slog.Warn("failed to revoke SSH api token on disconnect",
								slog.String("component", "tui"),
								slog.Any("error", err))
						}
					}
				}()
			}
		}

		// Create a new app instance for each session: a fresh client, a
		// fresh shared context and a fresh screen stack.
		client := data.NewClient(apiURL)
		if sessionToken != "" {
			client.SetSessionToken(sessionToken)
		}
		if bearerToken != "" {
			client.SetBearerToken(bearerToken)
		}

		defaultTheme := styles.ByName(styles.DefaultTheme)
		ctx := &core.Ctx{
			Styles: styles.New(defaultTheme.Palette),
			Theme:  defaultTheme.Name,
			Client: client,
			User:   userInfo,
			Keys:   core.DefaultKeyMap(),
		}

		model := app.New(ctx, workspaces.New(ctx))

		// Bubble Tea v2 owns alt-screen + mouse mode via the View struct
		// (see app.Model.View). No program options needed for those.
		return model, nil
	}
}
