//go:build agent_e2e

// End-to-end smoke for Phase 1 (WI-84): spawn the windshift/coding-agent
// skeleton image via the DockerRunner, drive it through the RunService
// pipeline, assert the run row finalizes succeeded and the entrypoint's
// NDJSON line lands in agent_run_events.
//
// Skipped from the default `go test ./...` invocation via the agent_e2e
// build tag. Run manually with:
//
//   ( cd deploy/coding-agent && docker build -t windshift/coding-agent:wi-84-skeleton . )
//   go test -tags agent_e2e ./internal/services/... -run TestRunService_DockerSmoke -v
package services

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
)

const dockerSmokeImage = "windshift/coding-agent:wi-84-skeleton"

func TestRunService_DockerSmoke(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	// Confirm the image is present locally; we don't auto-build to keep
	// the test free of side effects.
	if err := exec.Command("docker", "image", "inspect", dockerSmokeImage).Run(); err != nil {
		t.Skipf("image %q not present locally — build it first (see file header)", dockerSmokeImage)
	}

	db := newRunServiceTestDB(t)
	repo := repository.NewAgentRunRepository(db)

	runner := &DockerRunner{
		Image: dockerSmokeImage,
		Env: map[string]string{
			"WS_WORKSPACE_ID":    "1",
			"WINDSHIFT_ITEM_ID":  "WI-84",
		},
	}
	svc, err := NewRunService(repo, RunServiceOptions{
		GlobalCap: 2,
		Runner:    runner,
		Logger:    silentLogger(t),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runID, err := svc.Start(ctx, RunRequest{WorkspaceID: 1})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	svc.Wait()

	got, err := repo.Get(ctx, runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != models.AgentRunStatusSucceeded {
		t.Fatalf("run status: want succeeded, got %q (err=%q)", got.Status, got.Error)
	}

	events, err := repo.ListEvents(ctx, runID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}

	foundEntrypointLine := false
	for _, ev := range events {
		if ev.Type != "stdout" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(ev.PayloadJSON), &payload); err != nil {
			// Non-JSON stdout lines get wrapped as {"line":...}; verify
			// the wrapper instead.
			if !strings.Contains(ev.PayloadJSON, `"line"`) {
				t.Errorf("stdout payload not JSON-shaped: %q", ev.PayloadJSON)
			}
			continue
		}
		if phase, _ := payload["phase"].(string); phase == "skeleton" {
			foundEntrypointLine = true
			if itemSent, _ := payload["item_id"].(string); itemSent != "WI-84" {
				t.Errorf("entrypoint saw item_id=%q, want WI-84", itemSent)
			}
		}
	}
	if !foundEntrypointLine {
		t.Errorf("expected entrypoint's skeleton lifecycle line in stdout events; got %d events", len(events))
		for _, ev := range events {
			t.Logf("  event: type=%s payload=%s", ev.Type, ev.PayloadJSON)
		}
	}
}
