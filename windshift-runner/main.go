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
//	WS_API_URL                   orchestrator base URL incl. API prefix (required)
//	WSRUNNER_REGISTRATION_TOKEN  pool registration token, wsrt_… (required)
//	WSRUNNER_NAME                runner display name (default: hostname)
//	WSRUNNER_IMAGE               windshift-agent container image (required to run jobs)
//	WSRUNNER_DOCKER              docker binary (default: docker)
//	WSRUNNER_POLL_INTERVAL       claim poll interval when idle (default: 2s)
//	WSRUNNER_HEARTBEAT_INTERVAL  lease heartbeat interval (default: 30s)
//	WSRUNNER_INITIAL_PROMPT      pi initial prompt (default: generic instruction)
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
	"syscall"
	"time"

	"windshift/internal/services"
)

func main() {
	logger := log.New(os.Stderr, "windshift-runner ", log.LstdFlags|log.LUTC)

	baseURL := mustEnv(logger, "WS_API_URL")
	regToken := mustEnv(logger, "WSRUNNER_REGISTRATION_TOKEN")
	name := envOr("WSRUNNER_NAME", hostnameOr("windshift-runner"))
	image := os.Getenv("WSRUNNER_IMAGE")
	dockerBin := envOr("WSRUNNER_DOCKER", "docker")
	triageBin := envOr("WSRUNNER_TRIAGE_BIN", "windshift-triage")
	cacheRoot := envOr("WSRUNNER_CACHE_ROOT", "/var/lib/windshift-runner/cache")
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

	reg, err := services.RegisterRunner(ctx, baseURL, regToken, name, nil)
	if err != nil {
		logger.Fatalf("register with %s: %v", baseURL, err)
	}
	logger.Printf("registered as instance %d in pool %d (name %q)", reg.InstanceID, reg.PoolID, name)

	client := services.NewHTTPOrchestratorClient(baseURL, reg.Credential, nil)
	client.PollInterval = pollInterval
	client.Logger = logger

	go heartbeatLoop(ctx, client, heartbeatInterval, logger)

	// Kind-dispatching runner (WI-146): coding_agent jobs run the windshift-agent
	// harness; action_container / ci_task jobs run the job's admin image as a
	// plain container.
	kindRunner := &services.KindDispatchRunner{
		CodingAgent: &services.DockerPiRunner{
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
