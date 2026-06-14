#!/usr/bin/env bash
# Run the windshift dev server with the coding-agent harness enabled for LOCAL
# development. Windshift orchestrates only — it queues runs and serves the
# runner pool; agents execute on a windshift-runner, not in this process. To
# actually run agents locally, start a windshift-runner against this server in a
# second terminal (configure it via WSRUNNER_* env; see windshift-runner/main.go).
#
# SSO_SECRET is intentionally NOT defaulted here: it must match the value your
# stored SCM/LLM credentials were encrypted with, or decryption (and the PR
# hook's git access) will fail. Export it yourself, e.g.
#   SSO_SECRET=... ./dev-coding-agent.sh
set -euo pipefail
cd "$(dirname "$0")"

: "${SSO_SECRET:?set SSO_SECRET to the value your stored SCM/LLM creds were encrypted with (a mismatch re-breaks decryption)}"

# API URL handed to runners/agents for the ws CLI and run-scoped llm-proxy.
# localhost inside an agent container is the container itself; host.docker.internal
# hops back to the host where this dev server listens.
export CODING_AGENT_WS_API_URL="${CODING_AGENT_WS_API_URL:-http://host.docker.internal:7777/api}"

echo "coding-agent harness: orchestration-only (runs execute on a windshift-runner)"
echo "  ws_api_url = $CODING_AGENT_WS_API_URL"
echo "  next: start a windshift-runner against http://localhost:7777/api to execute jobs"
echo ""

exec go run main.go --port 7777 --db windshift.db --no-csrf --enable-coding-agent "$@"
