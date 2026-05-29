#!/bin/sh
# Phase 1 walking-skeleton entrypoint (WI-84). Phase 3 (WI-86) replaces this
# with: envsubst the baked ws.toml template from env vars, then
# `exec pi --mode rpc --provider $LLM_PROVIDER --model $LLM_MODEL`.
#
# For now: emit a single NDJSON line so the orchestrator's smoke test has
# something to observe in agent_run_events, then exit cleanly.
set -eu

printf '{"type":"lifecycle","phase":"skeleton","run_id":"%s","item_id":"%s","workspace_id":"%s"}\n' \
    "${AGENT_RUN_ID:-unset}" \
    "${WINDSHIFT_ITEM_ID:-unset}" \
    "${WS_WORKSPACE_ID:-unset}"

# When the orchestrator bind-mounts a worktree (WI-85+) it lands at
# /workspace. The skeleton just reports what it sees so the e2e test can
# assert the mount made it through; Phase 3 (WI-86) does real work here.
if [ -d /workspace ]; then
    readme_present=false
    if [ -f /workspace/README.md ]; then
        readme_present=true
    fi
    printf '{"type":"workspace","mounted":true,"readme":%s}\n' "$readme_present"
fi

exit 0
