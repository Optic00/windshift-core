package jiraimport

import (
	"time"

	"windshift/internal/models"
)

func (s *Service) FindCustomField(name string) (*models.CustomFieldDefinition, error) {
	return s.customFields.FindByName(name)
}

func (s *Service) FindAffectsVersionField(name, preferredType string) (*models.CustomFieldDefinition, error) {
	return s.customFields.FindPreferredByName(name, preferredType)
}

func (s *Service) CreateCustomField(name, fieldType, description, options string, now time.Time) (int, error) {
	id, err := s.customFields.Create(&models.CustomFieldDefinition{
		Name: name, FieldType: fieldType, Description: description, Options: options,
	}, now)
	return int(id), err
}

func (s *Service) UpdateCustomField(id int, name, fieldType, description, options string, now time.Time) error {
	return s.customFields.Update(id, &models.CustomFieldDefinition{
		Name: name, FieldType: fieldType, Description: description, Options: options,
	}, now)
}

func (s *Service) FindMilestone(name string, workspaceID int) (milestoneID int, found bool, err error) {
	milestone, err := s.planning.FindMilestoneByName(workspaceID, name)
	if err != nil || milestone == nil {
		return 0, false, err
	}
	return milestone.ID, true, nil
}

func (s *Service) FindStatus(name string) (*models.Status, error) {
	return s.statuses.FindByName(name)
}

func (s *Service) FindItemType(name string) (*models.ItemType, error) {
	return s.itemTypes.FindByName(name)
}
