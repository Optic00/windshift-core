package services

import (
	"context"
	"errors"
	"testing"
)

func TestEnsureDefaultPortalSectionRejectsNullConfig(t *testing.T) {
	if _, err := ensureDefaultPortalSection("null"); err == nil {
		t.Fatal("null portal config unexpectedly succeeded")
	}
}

func TestCreateChannelRejectsGenericDefaultCreation(t *testing.T) {
	service := &ChannelService{}
	_, err := service.Create(context.Background(), ChannelCreateRequest{
		Name:      "Replacement SMTP",
		Type:      "smtp",
		Direction: "outbound",
		IsDefault: true,
	})
	if !errors.Is(err, ErrInvalidChannelField) {
		t.Fatalf("Create(default) error = %v, want ErrInvalidChannelField", err)
	}
}
