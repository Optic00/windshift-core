package services

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
)

// ErrInvalidColorMode is returned when a preference update contains an unsupported color mode.
var ErrInvalidColorMode = errors.New("invalid color mode")

// UserPreferencesService owns user preference use cases.
type UserPreferencesService struct {
	prefsRepo *repository.UserPreferencesRepository
	themeRepo *repository.ThemeRepository
}

// NewUserPreferencesService creates a UserPreferencesService.
func NewUserPreferencesService(prefsRepo *repository.UserPreferencesRepository, themeRepo *repository.ThemeRepository) *UserPreferencesService {
	return &UserPreferencesService{prefsRepo: prefsRepo, themeRepo: themeRepo}
}

func (s *UserPreferencesService) loadData(userID int) (models.UserPreferencesData, error) {
	var prefs models.UserPreferencesData
	prefsJSON, err := s.prefsRepo.GetJSON(userID)
	if errors.Is(err, repository.ErrNotFound) {
		return prefs, nil
	}
	if err != nil {
		return prefs, err
	}
	if prefsJSON == "" {
		return prefs, nil
	}
	if err := json.Unmarshal([]byte(prefsJSON), &prefs); err != nil {
		return prefs, err
	}
	return prefs, nil
}

func (s *UserPreferencesService) saveData(userID int, prefs models.UserPreferencesData) error {
	prefsBytes, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	return s.prefsRepo.UpsertJSON(userID, string(prefsBytes), time.Now())
}

// Get returns a user's preferences with resolved theme details when present.
func (s *UserPreferencesService) Get(userID int) (models.UserPreferencesResponse, error) {
	prefs, err := s.loadData(userID)
	if err != nil {
		return models.UserPreferencesResponse{}, err
	}

	response := models.UserPreferencesResponse{ThemeID: prefs.ThemeID, ColorMode: prefs.ColorMode}
	if response.ColorMode == "" {
		response.ColorMode = "system"
	}
	if prefs.ThemeID != nil {
		if theme, err := s.themeRepo.GetByID(*prefs.ThemeID); err == nil {
			response.Theme = &theme
		}
	}
	return response, nil
}

// Update applies a partial preference update and returns the updated preferences.
func (s *UserPreferencesService) Update(userID int, req models.UserPreferencesRequest) (models.UserPreferencesResponse, error) {
	if req.ColorMode != "" && req.ColorMode != "light" && req.ColorMode != "dark" && req.ColorMode != "system" {
		return models.UserPreferencesResponse{}, ErrInvalidColorMode
	}
	if req.ThemeID != nil {
		if _, err := s.themeRepo.GetByID(*req.ThemeID); err != nil {
			return models.UserPreferencesResponse{}, err
		}
	}

	prefs, err := s.loadData(userID)
	if err != nil {
		return models.UserPreferencesResponse{}, err
	}

	if req.ThemeID != nil {
		prefs.ThemeID = req.ThemeID
	}
	if req.ColorMode != "" {
		prefs.ColorMode = req.ColorMode
	}

	if err := s.saveData(userID, prefs); err != nil {
		return models.UserPreferencesResponse{}, err
	}
	return s.Get(userID)
}

// GetDashboardLayout returns the user's dashboard layout or an empty layout.
func (s *UserPreferencesService) GetDashboardLayout(userID int) (models.UserDashboardLayout, error) {
	prefs, err := s.loadData(userID)
	if err != nil {
		return models.UserDashboardLayout{}, err
	}
	layout := models.UserDashboardLayout{
		Sections: []models.UserDashboardSection{},
		Widgets:  []models.UserDashboardWidget{},
	}
	if prefs.DashboardLayout != nil {
		layout = *prefs.DashboardLayout
		if layout.Sections == nil {
			layout.Sections = []models.UserDashboardSection{}
		}
		if layout.Widgets == nil {
			layout.Widgets = []models.UserDashboardWidget{}
		}
	}
	return layout, nil
}

// GetDashboardLayoutSnapshot returns the dashboard layout together with a
// stable content revision. The revision changes only when the layout changes,
// rather than whenever another field in the shared preferences row is saved.
func (s *UserPreferencesService) GetDashboardLayoutSnapshot(userID int) (models.UserDashboardLayout, string, error) {
	layout, err := s.GetDashboardLayout(userID)
	if err != nil {
		return models.UserDashboardLayout{}, "", err
	}

	raw, err := json.Marshal(layout)
	if err != nil {
		return models.UserDashboardLayout{}, "", err
	}
	revision := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return layout, revision, nil
}

// UpdateDashboardLayout stores the user's dashboard layout without clobbering other preferences.
func (s *UserPreferencesService) UpdateDashboardLayout(userID int, layout models.UserDashboardLayout) error {
	prefs, err := s.loadData(userID)
	if err != nil {
		return err
	}
	prefs.DashboardLayout = &layout
	return s.saveData(userID, prefs)
}

// GetTUI returns the user's SSH TUI preferences (zero value when unset).
func (s *UserPreferencesService) GetTUI(userID int) (models.UserTUIPreferences, error) {
	prefs, err := s.loadData(userID)
	if err != nil {
		return models.UserTUIPreferences{}, err
	}
	if prefs.TUI == nil {
		return models.UserTUIPreferences{}, nil
	}
	return *prefs.TUI, nil
}

// UpdateTUI stores the SSH TUI preferences sub-document without clobbering
// other preferences. Values are normalized rather than rejected — a stale
// or odd preference should never break the TUI.
func (s *UserPreferencesService) UpdateTUI(userID int, tui models.UserTUIPreferences) error {
	if len(tui.Theme) > 64 {
		tui.Theme = tui.Theme[:64]
	}
	if tui.SplitRatio != nil {
		r := *tui.SplitRatio
		if r < 0.1 {
			r = 0.1
		}
		if r > 0.9 {
			r = 0.9
		}
		tui.SplitRatio = &r
	}
	if tui.LastWorkspaceID != nil && *tui.LastWorkspaceID <= 0 {
		tui.LastWorkspaceID = nil
	}

	prefs, err := s.loadData(userID)
	if err != nil {
		return err
	}
	prefs.TUI = &tui
	return s.saveData(userID, prefs)
}
