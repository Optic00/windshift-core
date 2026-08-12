package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"windshift/internal/utils"
)

// Wire DTOs for the remote-runner control plane (Initiative WI-141). Shared
// by HTTPOrchestratorClient (the agent-binary side) and RunnerControlHandler
// (the orchestrator side) so the contract lives in one place.

// HTTPStatusError is returned by the control-plane client for a non-2xx
// response. It carries the status code so callers can distinguish a definitive
// auth rejection (the credential is stale and the runner should re-register)
// from a transient/network failure (which should be retried, not re-registered).
type HTTPStatusError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("POST %s: status %d: %s", e.URL, e.StatusCode, e.Body)
}

// IsAuthRejection reports whether err is a control-plane response that rejected
// the presented credential (401/403) — as opposed to a transient failure or a
// network error, which leave it false.
func IsAuthRejection(err error) bool {
	var se *HTTPStatusError
	if errors.As(err, &se) {
		return se.StatusCode == http.StatusUnauthorized || se.StatusCode == http.StatusForbidden
	}
	return false
}

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
	Branch      string `json:"branch,omitempty"`
	BaseCommit  string `json:"base_commit,omitempty"`
	Summary     string `json:"summary,omitempty"` // agent finish summary, rendered as the PR note (WI-400)
	// Repos is the per-repo push result of a multi-repo run (WI-449). Empty for
	// single-repo runs, which use Branch/BaseCommit.
	Repos []ReportRepo `json:"repos,omitempty"`
}

// ReportRepo is one repo's push result in a ReportRequest (WI-449).
type ReportRepo struct {
	RepoSlug   string `json:"repo_slug"`
	Branch     string `json:"branch,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
}

// HeartbeatResponse is returned from POST /runner/heartbeat. Abort lists the
// run ids the runner should cancel (the orchestrator requested cancellation);
// QueueDepth is the runner pool's current queued-run count (the autoscaling
// signal).
type HeartbeatResponse struct {
	Abort      []int `json:"abort,omitempty"`
	QueueDepth int   `json:"queue_depth"`
}

// DefaultRunnerPollInterval balances idle traffic with job pickup latency.
const DefaultRunnerPollInterval = 10 * time.Second

// HTTPOrchestratorClient is the remote transport for the shared RunWorker
// loop: it implements OrchestratorClient by talking to the orchestrator's
// runner control plane over HTTPS, authenticated with the per-instance
// runner credential. The standalone agent binary (WI-160) runs RunWorker
// with this client.
type HTTPOrchestratorClient struct {
	baseURL    string
	credential string
	hc         *http.Client

	// PollInterval is how long Claim waits between polls when the
	// orchestrator has no work. Defaults to DefaultRunnerPollInterval when zero.
	PollInterval time.Duration
	// Logger, when set, receives transient Claim errors (which are retried
	// rather than surfaced, so a network blip never stops the worker).
	Logger *log.Logger

	// inflight maps an in-flight run id to the cancel func of its per-run
	// context, so Heartbeat can abort a run when the orchestrator requests
	// cancellation. The in-process client keeps the analogous registry.
	mu       sync.Mutex
	inflight map[int]context.CancelFunc
}

// NewHTTPOrchestratorClient constructs a client for baseURL (e.g.
// https://windshift.example.com) authenticated with the given per-instance
// runner credential. A nil hc uses a default client with a sane timeout.
func NewHTTPOrchestratorClient(baseURL, credential string, hc *http.Client) *HTTPOrchestratorClient {
	if hc == nil {
		hc = utils.NewHTTPClient(60 * time.Second)
	}
	return &HTTPOrchestratorClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		credential: credential,
		hc:         hc,
		inflight:   map[int]context.CancelFunc{},
	}
}

// RegisterRunner exchanges a pool registration token for a per-instance
// runner credential. It is the unauthenticated bootstrap the agent performs
// once on deploy, before constructing an authenticated client.
func RegisterRunner(ctx context.Context, baseURL, registrationToken, name string, hc *http.Client) (*RegisterResponse, error) {
	if hc == nil {
		hc = utils.NewHTTPClient(30 * time.Second)
	}
	var out RegisterResponse
	if err := doJSON(ctx, hc, strings.TrimRight(baseURL, "/")+"/runner/register", "",
		RegisterRequest{RegistrationToken: registrationToken, Name: name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Claim implements OrchestratorClient. For the remote transport "no work" is
// an idle condition, not a stop: Claim polls the orchestrator on PollInterval
// until a job is available, and returns (nil, nil) only when ctx is canceled
// (the agent is shutting down) — matching the in-process Claim, which also
// blocks until work or shutdown. Transient request failures are logged and
// retried on the same interval so a blip never stops the worker.
func (c *HTTPOrchestratorClient) Claim(ctx context.Context) (*ClaimedJob, error) {
	interval := c.pollInterval()
	for {
		select {
		case <-ctx.Done():
			return nil, nil
		default:
		}
		var out ClaimResponse
		if err := doJSON(ctx, c.hc, c.baseURL+"/runner/claim", c.credential, nil, &out); err != nil {
			// Transient failure: log (if shutting down, skip the noise) and
			// fall through to the idle wait — never surface it, so a blip
			// cannot stop the worker.
			if c.Logger != nil && ctx.Err() == nil {
				c.Logger.Printf("runner: claim: %v", err)
			}
		} else if out.Job != nil {
			// Give the job a per-run context so Heartbeat can abort it on
			// an orchestrator cancellation request. Child of the worker ctx
			// so agent shutdown cancels it too.
			runCtx, cancel := context.WithCancel(ctx)
			c.register(out.Job.RunID, cancel)
			return &ClaimedJob{Spec: *out.Job, Ctx: runCtx}, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(interval):
		}
	}
}

func (c *HTTPOrchestratorClient) pollInterval() time.Duration {
	if c.PollInterval > 0 {
		return c.PollInterval
	}
	return DefaultRunnerPollInterval
}

// Emit implements OrchestratorClient.
func (c *HTTPOrchestratorClient) Emit(ctx context.Context, runID int, eventType, payloadJSON string) error {
	return doJSON(ctx, c.hc, fmt.Sprintf("%s/runner/runs/%d/events", c.baseURL, runID), c.credential,
		EmitRequest{Type: eventType, PayloadJSON: payloadJSON}, nil)
}

// reportRetryBudget bounds how long Report keeps retrying delivery of a
// terminal verdict before giving up, and reportAttemptTimeout bounds each
// individual POST within that budget.
const (
	reportRetryBudget    = 2 * time.Minute
	reportAttemptTimeout = 30 * time.Second
)

// Report implements OrchestratorClient. It deregisters the run's per-run
// context and delivers the terminal verdict on a fresh context derived from
// ctx — the run's own context may already be canceled (abort / shutdown),
// but the verdict must still reach the orchestrator.
//
// The terminal verdict is the one message that must not be lost: a dropped
// report leaves the run stuck in 'running' on the orchestrator until the
// duration backstop reaps it (WI-331), holding a pool-concurrency slot and
// the binding's per-item dedup the whole time. So transient failures are
// retried with capped exponential backoff for up to reportRetryBudget before
// giving up. A definitive 4xx rejection (stale credential, unknown run) will
// not change on retry and is surfaced immediately.
func (c *HTTPOrchestratorClient) Report(ctx context.Context, runID int, result RunnerResult) error {
	c.mu.Lock()
	if cancel := c.inflight[runID]; cancel != nil {
		cancel()
	}
	delete(c.inflight, runID)
	c.mu.Unlock()

	req := ReportRequest{
		Status:      result.Status,
		Error:       result.Error,
		ContainerID: result.ContainerID,
		Branch:      result.Branch,
		BaseCommit:  result.BaseCommit,
		Summary:     result.Summary,
	}
	for _, rr := range result.Repos {
		req.Repos = append(req.Repos, ReportRepo(rr))
	}
	base := context.WithoutCancel(ctx)
	url := fmt.Sprintf("%s/runner/runs/%d/result", c.baseURL, runID)
	deadline := time.Now().Add(reportRetryBudget)
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		rctx, rcancel := context.WithTimeout(base, reportAttemptTimeout)
		err := doJSON(rctx, c.hc, url, c.credential, req, nil)
		rcancel()
		if err == nil {
			return nil
		}
		// A definitive client-side rejection (auth, unknown run, bad body)
		// is not transient; retrying just delays the inevitable.
		var se *HTTPStatusError
		if errors.As(err, &se) && se.StatusCode >= 400 && se.StatusCode < 500 {
			return err
		}
		if !time.Now().Add(backoff).Before(deadline) {
			return fmt.Errorf("report run %d: giving up after %d attempt(s): %w", runID, attempt, err)
		}
		if c.Logger != nil {
			c.Logger.Printf("runner: report run=%d attempt %d failed, retrying in %s: %v", runID, attempt, backoff, err)
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > reportAttemptTimeout {
			backoff = reportAttemptTimeout
		}
	}
}

// register stores the cancel func for an in-flight run so Heartbeat can abort
// it later (the cancel is invoked by Report on completion or Heartbeat on an
// orchestrator cancellation request).
func (c *HTTPOrchestratorClient) register(runID int, cancel context.CancelFunc) {
	c.mu.Lock()
	c.inflight[runID] = cancel
	c.mu.Unlock()
}

// Heartbeat implements OrchestratorClient: it renews the runner's lease and
// aborts any in-flight run the orchestrator has flagged for cancellation by
// canceling that run's per-run context (which tears down its container).
func (c *HTTPOrchestratorClient) Heartbeat(ctx context.Context, _ int) error {
	var resp HeartbeatResponse
	if err := doJSON(ctx, c.hc, c.baseURL+"/runner/heartbeat", c.credential, nil, &resp); err != nil {
		return err
	}
	for _, id := range resp.Abort {
		c.mu.Lock()
		cancel := c.inflight[id]
		c.mu.Unlock()
		if cancel != nil {
			if c.Logger != nil {
				c.Logger.Printf("runner: aborting run %d (canceled by orchestrator)", id)
			}
			cancel()
		}
	}
	return nil
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
		return &HTTPStatusError{StatusCode: resp.StatusCode, URL: url, Body: strings.TrimSpace(string(msg))}
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
