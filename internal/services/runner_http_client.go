package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Wire DTOs for the remote-runner control plane (Initiative WI-141). Shared
// by HTTPOrchestratorClient (the agent-binary side) and RunnerControlHandler
// (the orchestrator side) so the contract lives in one place.

// RegisterRequest is the body of POST /runner/register.
type RegisterRequest struct {
	RegistrationToken string `json:"registration_token"`
	Name              string `json:"name,omitempty"`
}

// RegisterResponse is returned from POST /runner/register. Credential is the
// per-instance runner credential, shown exactly once.
type RegisterResponse struct {
	Credential string `json:"credential"`
	InstanceID int    `json:"instance_id"`
	PoolID     int    `json:"pool_id"`
}

// ClaimResponse is returned from POST /runner/claim. Job is nil when no work
// is available for the runner's pool.
type ClaimResponse struct {
	Job *JobSpec `json:"job"`
}

// EmitRequest is the body of POST /runner/runs/{id}/events.
type EmitRequest struct {
	Type        string `json:"type"`
	PayloadJSON string `json:"payload_json"`
}

// ReportRequest is the body of POST /runner/runs/{id}/result.
type ReportRequest struct {
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
}

// HTTPOrchestratorClient is the remote transport for the shared RunWorker
// loop: it implements OrchestratorClient by talking to the orchestrator's
// runner control plane over HTTPS, authenticated with the per-instance
// runner credential. The standalone agent binary (WI-160) runs RunWorker
// with this client.
type HTTPOrchestratorClient struct {
	baseURL    string
	credential string
	hc         *http.Client
}

// NewHTTPOrchestratorClient constructs a client for baseURL (e.g.
// https://windshift.example.com) authenticated with the given per-instance
// runner credential. A nil hc uses a default client with a sane timeout.
func NewHTTPOrchestratorClient(baseURL, credential string, hc *http.Client) *HTTPOrchestratorClient {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &HTTPOrchestratorClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		credential: credential,
		hc:         hc,
	}
}

// RegisterRunner exchanges a pool registration token for a per-instance
// runner credential. It is the unauthenticated bootstrap the agent performs
// once on deploy, before constructing an authenticated client.
func RegisterRunner(ctx context.Context, baseURL, registrationToken, name string, hc *http.Client) (*RegisterResponse, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	var out RegisterResponse
	if err := doJSON(ctx, hc, strings.TrimRight(baseURL, "/")+"/runner/register", "",
		RegisterRequest{RegistrationToken: registrationToken, Name: name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Claim implements OrchestratorClient. Returns (nil, nil) when no work is
// available so RunWorker treats it as a clean idle, not an error.
func (c *HTTPOrchestratorClient) Claim(ctx context.Context) (*ClaimedJob, error) {
	var out ClaimResponse
	if err := doJSON(ctx, c.hc, c.baseURL+"/runner/claim", c.credential, nil, &out); err != nil {
		return nil, err
	}
	if out.Job == nil {
		return nil, nil
	}
	return &ClaimedJob{Spec: *out.Job}, nil
}

// Emit implements OrchestratorClient.
func (c *HTTPOrchestratorClient) Emit(ctx context.Context, runID int, eventType, payloadJSON string) error {
	return doJSON(ctx, c.hc, fmt.Sprintf("%s/runner/runs/%d/events", c.baseURL, runID), c.credential,
		EmitRequest{Type: eventType, PayloadJSON: payloadJSON}, nil)
}

// Report implements OrchestratorClient.
func (c *HTTPOrchestratorClient) Report(ctx context.Context, runID int, result RunnerResult) error {
	return doJSON(ctx, c.hc, fmt.Sprintf("%s/runner/runs/%d/result", c.baseURL, runID), c.credential,
		ReportRequest{Status: result.Status, Error: result.Error, ContainerID: result.ContainerID}, nil)
}

// Heartbeat implements OrchestratorClient: it renews the runner's lease.
func (c *HTTPOrchestratorClient) Heartbeat(ctx context.Context, _ int) error {
	return doJSON(ctx, c.hc, c.baseURL+"/runner/heartbeat", c.credential, nil, nil)
}

// doJSON sends an optional JSON body via POST and decodes an optional JSON
// response. A non-2xx status is returned as an error including the response
// body. Every control-plane call is a POST, so the method is fixed.
func doJSON(ctx context.Context, hc *http.Client, url, bearer string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s: status %d: %s", url, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// Compile-time check that the HTTP client satisfies the seam.
var _ OrchestratorClient = (*HTTPOrchestratorClient)(nil)
