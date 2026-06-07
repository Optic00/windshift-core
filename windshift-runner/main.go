// Command windshift-runner is the standalone remote-runner agent for the
// Windshift coding-agent harness (Initiative WI-141).
//
// On start it self-registers with the orchestrator using a pool registration
// token, exchanges it for a per-instance credential, then runs the shared
// services.RunWorker loop against the HTTP transport with a DockerAgentRunner
// core: claim a queued run for its pool, execute it in a throwaway container,
// report the result. It heartbeats on an interval to keep its lease alive and
// shuts down gracefully on SIGINT/SIGTERM (finishing any in-flight job first).
//
// Configuration is environment-only (operators bake it into the deployment):
//
//	WS_API_URL                   orchestrator base URL incl. API prefix (required, https://)
//	WSRUNNER_REGISTRATION_TOKEN  pool registration token, wsrt_… (required only at first
//	                             bootstrap — when no injected/persisted credential exists)
//	WSRUNNER_CREDENTIAL          per-instance credential, wsrc_… (optional; injected directly
//	                             for immutable deploys, skips registration entirely)
//	WSRUNNER_CREDENTIAL_FILE     path to persist/reuse the per-instance credential (default: <cache>/credential)
//	WSRUNNER_ALLOW_INSECURE      set to 1 to permit a plaintext http:// WS_API_URL (dev only)
//	WSRUNNER_NAME                runner display name (default: hostname)
//	WSRUNNER_IMAGE               windshift-agent container image (required to run jobs)
//	WSRUNNER_DOCKER              docker binary (default: docker)
//	WSRUNNER_POLL_INTERVAL       claim poll interval when idle (default: 2s)
//	WSRUNNER_HEARTBEAT_INTERVAL  lease heartbeat interval (default: 30s)
//	WSRUNNER_INITIAL_PROMPT      agent initial prompt (default: generic instruction)
//
// NOTE: until the secretless access layer (WI-144) enriches the claimed
// JobSpec with per-run env (item id, brokered tokens) and worktree, a claimed
// job runs with minimal env; meaningful execution depends on that phase.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"windshift/internal/services"
)

func main() {
	logger := log.New(os.Stderr, "windshift-runner ", log.LstdFlags|log.LUTC)

	baseURL := mustEnv(logger, "WS_API_URL")
	requireHTTPS(logger, baseURL)
	name := envOr("WSRUNNER_NAME", hostnameOr("windshift-runner"))
	image := os.Getenv("WSRUNNER_IMAGE")
	dockerBin := envOr("WSRUNNER_DOCKER", "docker")
	triageBin := envOr("WSRUNNER_TRIAGE_BIN", "windshift-triage")
	cacheRoot := envOr("WSRUNNER_CACHE_ROOT", "/var/lib/windshift-runner/cache")
	credFile := envOr("WSRUNNER_CREDENTIAL_FILE", filepath.Join(cacheRoot, "credential"))
	pollInterval := envDuration(logger, "WSRUNNER_POLL_INTERVAL", 2*time.Second)
	heartbeatInterval := envDuration(logger, "WSRUNNER_HEARTBEAT_INTERVAL", 30*time.Second)
	initialPrompt := envOr("WSRUNNER_INITIAL_PROMPT", "Work the item described in your environment.")

	if image == "" {
		logger.Println("warning: WSRUNNER_IMAGE is unset; claimed jobs will fail until it is configured")
	}

	// Graceful shutdown: SIGINT/SIGTERM cancels ctx; RunWorker returns after
	// the current job (if any) reports.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Resolve an authenticated client without re-registering on every restart
	// (WI-238 security Phase 6): reuse an injected (WSRUNNER_CREDENTIAL) or
	// persisted per-instance credential, and only fall back to registering with
	// the pool token when no usable credential exists. So the registration token
	// is needed only at first bootstrap and can be single-use.
	client := loadOrRegister(ctx, logger, baseURL, credFile, name)
	client.PollInterval = pollInterval
	client.Logger = logger

	go heartbeatLoop(ctx, client, heartbeatInterval, logger)

	// Kind-dispatching runner (WI-146): coding_agent jobs run the windshift-agent
	// harness; action_container / ci_task jobs run the job's admin image as a
	// plain container.
	kindRunner := &services.KindDispatchRunner{
		CodingAgent: &services.DockerAgentRunner{
			Image:         image,
			DockerBinary:  dockerBin,
			InitialPrompt: initialPrompt,
		},
		Container: &services.ContainerImageRunner{DockerBinary: dockerBin},
	}

	// On a remote host the runner — never the agent — owns git (WI-215): for a
	// job carrying a JobRepo, TriageRunner execs windshift-triage to prepare a
	// per-run checkout and to push the run branch through the git-proxy, so the
	// agent container holds no SCM credential. Jobs without a JobRepo pass
	// straight through to the kind runner.
	runner := &services.TriageRunner{
		Inner:     kindRunner,
		TriageBin: triageBin,
		CacheRoot: cacheRoot,
		APIBase:   baseURL,
		Logger:    logger,
	}

	logger.Printf("worker started (poll=%s heartbeat=%s image=%q triage=%q cache=%q)",
		pollInterval, heartbeatInterval, image, triageBin, cacheRoot)
	services.RunWorker(ctx, client, runner, logger)
	logger.Println("shut down")
}

// heartbeatLoop renews the runner's lease on an interval until ctx is done.
func heartbeatLoop(ctx context.Context, client *services.HTTPOrchestratorClient, interval time.Duration, logger *log.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := client.Heartbeat(ctx, 0); err != nil && ctx.Err() == nil {
				logger.Printf("heartbeat: %v", err)
			}
		}
	}
}

// requireHTTPS rejects a plaintext control-plane URL unless the operator
// explicitly opts into insecure transport for local development (WI-238
// security Phase 6). Runner credentials and per-run tokens ride this
// connection, so HTTP would expose them on the wire.
func requireHTTPS(logger *log.Logger, baseURL string) {
	if strings.HasPrefix(strings.ToLower(baseURL), "https://") {
		return
	}
	if envOr("WSRUNNER_ALLOW_INSECURE", "") == "1" {
		logger.Printf("warning: WS_API_URL is not https; allowed only because WSRUNNER_ALLOW_INSECURE=1")
		return
	}
	logger.Fatalf("WS_API_URL must be https:// (got %q); set WSRUNNER_ALLOW_INSECURE=1 to override in development", baseURL)
}

// loadOrRegister returns an authenticated orchestrator client without
// re-registering on every restart (WI-238 security Phase 6). It tries, in
// order: a credential injected via WSRUNNER_CREDENTIAL (for immutable
// deployments that bootstrap out of band), then the persisted credential file.
// Only when neither yields a usable credential does it register with the pool
// token — so WSRUNNER_REGISTRATION_TOKEN is required just at first bootstrap and
// may be single-use. A reused credential is probed with one heartbeat: a
// definitive 401/403 means it is stale and triggers a re-register; a transient
// failure is trusted (the worker loop retries) so a boot-time blip never burns
// the registration token.
func loadOrRegister(ctx context.Context, logger *log.Logger, baseURL, credFile, name string) *services.HTTPOrchestratorClient {
	if injected := strings.TrimSpace(os.Getenv("WSRUNNER_CREDENTIAL")); injected != "" {
		if client, ok := useCredential(ctx, logger, baseURL, injected, "WSRUNNER_CREDENTIAL"); ok {
			return client
		}
	}
	if stored := readCredential(credFile); stored != "" {
		if client, ok := useCredential(ctx, logger, baseURL, stored, credFile); ok {
			return client
		}
	}

	// No usable credential — register. Only here is the registration token
	// needed.
	regToken := mustEnv(logger, "WSRUNNER_REGISTRATION_TOKEN")
	reg, err := services.RegisterRunner(ctx, baseURL, regToken, name, nil)
	if err != nil {
		logger.Fatalf("register with %s: %v", baseURL, err)
	}
	logger.Printf("registered as instance %d in pool %d (name %q)", reg.InstanceID, reg.PoolID, name)
	if err := writeCredential(credFile, reg.Credential); err != nil {
		logger.Printf("warning: could not persist credential to %s: %v (will re-register next restart)", credFile, err)
	}
	return services.NewHTTPOrchestratorClient(baseURL, reg.Credential, nil)
}

// useCredential probes a candidate credential with one heartbeat. It returns
// (client, true) when the credential works or the control plane is only
// transiently unreachable (trust it; the worker retries), and (nil, false) when
// the orchestrator definitively rejected it (stale → caller re-registers).
func useCredential(ctx context.Context, logger *log.Logger, baseURL, credential, source string) (*services.HTTPOrchestratorClient, bool) {
	client := services.NewHTTPOrchestratorClient(baseURL, credential, nil)
	err := client.Heartbeat(ctx, 0)
	switch {
	case err == nil:
		logger.Printf("using runner credential from %s", source)
		return client, true
	case services.IsAuthRejection(err):
		logger.Printf("runner credential from %s was rejected (%v); re-registering", source, err)
		return nil, false
	default:
		logger.Printf("runner credential from %s: control plane unreachable (%v); proceeding, worker will retry", source, err)
		return client, true
	}
}

// readCredential returns the trimmed credential stored at path, or "" if the
// file is absent or empty.
func readCredential(path string) string {
	// path is operator config (WSRUNNER_CREDENTIAL_FILE), not user-supplied.
	b, err := os.ReadFile(path) //nolint:gosec // G304: operator-controlled path

	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeCredential persists the credential with 0600 permissions, creating the
// parent directory if needed.
func writeCredential(path, credential string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(credential+"\n"), 0o600)
}

func mustEnv(logger *log.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		logger.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(logger *log.Logger, key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		logger.Fatalf("invalid %s=%q: %v", key, v, err)
	}
	return d
}

func hostnameOr(fallback string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return fallback
}
