#!/usr/bin/env bash
# Run the windshift dev server with the coding-agent harness wired for LOCAL
# (Docker Desktop) development — the in-process runner spawns windshift/agent
# containers on your host Docker.
#
# All CODING_AGENT_* values below are defaults you can override by exporting
# them before running. SSO_SECRET is intentionally NOT set here: it must match
# the value your stored SCM/LLM credentials were encrypted with, or decryption
# (and the test run's git clone) will fail. Export it yourself, e.g.
#   SSO_SECRET=... ./dev-coding-agent.sh
set -euo pipefail
cd "$(dirname "$0")"

: "${SSO_SECRET:?set SSO_SECRET to the value your stored SCM/LLM creds were encrypted with (a mismatch re-breaks decryption)}"

# Local agent image (build it locally; the published default is for prod).
export CODING_AGENT_RUNNER_IMAGE="${CODING_AGENT_RUNNER_IMAGE:-windshift/agent:local}"
# Writable, absolute, Docker-shareable worktree root. The /data default is
# read-only on macOS, so point it at the repo's data dir.
export CODING_AGENT_WORKTREE_ROOT="${CODING_AGENT_WORKTREE_ROOT:-$PWD/data/worktrees}"
# So the agent container can reach this server's llm-proxy: localhost inside the
# container is the container itself; host.docker.internal hops back to the host.
export CODING_AGENT_WS_API_URL="${CODING_AGENT_WS_API_URL:-http://host.docker.internal:7777/api}"
# The prod default (coding-agent-egress) is an egress-filtered network that
# doesn't exist locally; bridge gives the container host.docker.internal access.
export CODING_AGENT_NETWORK="${CODING_AGENT_NETWORK:-bridge}"

echo "coding-agent harness:"
echo "  image      = $CODING_AGENT_RUNNER_IMAGE"
echo "  worktrees  = $CODING_AGENT_WORKTREE_ROOT"
echo "  ws_api_url = $CODING_AGENT_WS_API_URL"
echo "  network    = $CODING_AGENT_NETWORK"
echo ""

exec go run main.go --port 7777 --db windshift.db --no-csrf "$@"
