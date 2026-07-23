package services

import (
	"errors"
	"strconv"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
)

// ErrTestRunTemplateSetNotFound identifies a template set outside the target workspace.
var ErrTestRunTemplateSetNotFound = errors.New("test set not found in workspace")

// TestRunTemplateService owns template validation and execution orchestration.
type TestRunTemplateService struct {
	repo   *repository.TestRunTemplateRepository
	setSvc *TestSetService
	runSvc *TestRunService
}

func NewTestRunTemplateService(db database.Database) *TestRunTemplateService {
	return &TestRunTemplateService{
		repo:   repository.NewTestRunTemplateRepository(db),
		setSvc: NewTestSetService(db),
		runSvc: NewTestRunService(db),
	}
}

func (s *TestRunTemplateService) List(workspaceID int) ([]models.TestRunTemplate, error) {
	return s.repo.FindAll(workspaceID)
}

func (s *TestRunTemplateService) Get(id, workspaceID int) (*models.TestRunTemplate, error) {
	return s.repo.FindByID(id, workspaceID)
}

func (s *TestRunTemplateService) Exists(id, workspaceID int) (bool, error) {
	_, err := s.repo.FindCore(id, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *TestRunTemplateService) Create(workspaceID int, template models.TestRunTemplate) (*models.TestRunTemplate, error) {
	if err := s.normalizeAndValidate(workspaceID, &template); err != nil {
		return nil, err
	}
	id, createdAt, err := s.repo.Create(workspaceID, &template)
	if err != nil {
		return nil, err
	}
	template.ID = id
	template.WorkspaceID = workspaceID
	template.CreatedAt = createdAt
	template.UpdatedAt = createdAt
	return &template, nil
}

func (s *TestRunTemplateService) Update(id, workspaceID int, template models.TestRunTemplate) (*models.TestRunTemplate, error) {
	if err := s.normalizeAndValidate(workspaceID, &template); err != nil {
		return nil, err
	}
	updatedAt, err := s.repo.Update(id, workspaceID, &template)
	if err != nil {
		return nil, err
	}
	template.ID = id
	template.WorkspaceID = workspaceID
	template.UpdatedAt = updatedAt
	return &template, nil
}

func (s *TestRunTemplateService) Delete(id, workspaceID int) error {
	return s.repo.Delete(id, workspaceID)
}

func (s *TestRunTemplateService) ListExecutions(id, workspaceID int) ([]models.TestRun, error) {
	if _, err := s.repo.FindCore(id, workspaceID); err != nil {
		return nil, err
	}
	return s.repo.FindExecutions(id, workspaceID)
}

func (s *TestRunTemplateService) Execute(id, workspaceID int) (*models.TestRun, error) {
	template, err := s.repo.FindCore(id, workspaceID)
	if err != nil {
		return nil, err
	}
	runCount, err := s.repo.CountExecutions(id, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.runSvc.Create(workspaceID, TestRunCreateRequest{
		Name:       template.Name + " - Run " + strconv.Itoa(runCount+1),
		TemplateID: id,
		SetID:      template.SetID,
	})
}

func (s *TestRunTemplateService) normalizeAndValidate(workspaceID int, template *models.TestRunTemplate) error {
	template.Name = sanitize.PlainTextField.Sanitize(template.Name)
	template.Description = sanitize.RichText.Sanitize(template.Description)
	if template.SetID <= 0 {
		return ErrTestRunTemplateSetNotFound
	}
	if _, err := s.setSvc.Get(template.SetID, workspaceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrTestRunTemplateSetNotFound
		}
		return err
	}
	return nil
}
