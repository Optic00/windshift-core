package handlers

import "testing"

func TestValidWorkspaceWidgetTypesIncludesZammadSupportOverview(t *testing.T) {
	if !validWorkspaceWidgetTypes["zammad-support-overview"] {
		t.Fatal("zammad support overview must be accepted by homepage layout validation")
	}
}
