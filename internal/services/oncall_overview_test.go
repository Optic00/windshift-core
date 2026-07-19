package services

import (
	"testing"
	"time"

	"windshift/internal/models"
)

func TestCurrentOnCallForScheduleUsesHydratedMembersAndReplacesOverriddenUser(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	schedule := &models.OnCallSchedule{
		ID: 42,
		Layers: []models.OnCallScheduleLayer{
			{
				Name:         "Primary",
				Priority:     1,
				RotationType: "daily",
				HandoffTime:  "09:00",
				StartDate:    "2026-07-18",
				Members: []models.OnCallScheduleLayerMember{
					{UserID: 7, Position: 1, UserName: "Primary User", UserEmail: "primary@example.test"},
				},
			},
			{
				Name:         "Secondary",
				Priority:     2,
				RotationType: "daily",
				HandoffTime:  "09:00",
				StartDate:    "2026-07-18",
				Members: []models.OnCallScheduleLayerMember{
					{UserID: 9, Position: 1, UserName: "Secondary User", UserEmail: "secondary@example.test"},
				},
			},
		},
		Overrides: []models.OnCallScheduleOverride{
			{
				UserID:           7,
				OverrideUserID:   8,
				OverrideUserName: "Override User",
				StartTime:        now.Add(-time.Hour),
				EndTime:          now.Add(time.Hour),
			},
		},
	}

	result := (&OnCallService{}).CurrentOnCallForSchedule(schedule, now)

	if result.ScheduleID != schedule.ID || len(result.OnCall) != 2 {
		t.Fatalf("current on-call = %+v, want override plus secondary layer", result)
	}
	if entry := result.OnCall[0]; entry.UserID != 8 || entry.UserName != "Override User" || !entry.IsOverride {
		t.Fatalf("override entry = %+v, want override user 8", entry)
	}
	if entry := result.OnCall[1]; entry.UserID != 9 || entry.UserName != "Secondary User" || entry.UserEmail != "secondary@example.test" || entry.LayerName != "Secondary" {
		t.Fatalf("rotation entry = %+v, want hydrated secondary user", entry)
	}
	for _, entry := range result.OnCall {
		if entry.UserID == 7 {
			t.Fatalf("replaced user remained on call: %+v", result.OnCall)
		}
	}
}
