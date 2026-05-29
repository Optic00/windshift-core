#!/bin/sh
# Coding-agent runner entrypoint. Phases stack:
#   WI-84: emit a single lifecycle line so RunService has something to
#          observe in agent_run_events.
#   WI-85: report what the /workspace bind-mount looks like.
#   WI-86: render ws.toml from injected env (WS_TOKEN / WS_API_URL /
#          WS_WORKSPACE_KEY) and best-effort `ws config docs` against
#          the live workspace.
#   WI-89: replace the exit at the bottom with `exec pi --mode rpc ...`.
set -eu

# -----------------------------------------------------------------------
# Phase 3 (WI-86): render ws.toml + refresh WINDSHIFT.md
# -----------------------------------------------------------------------
ws_config_rendered=false
if [ -n "${WS_TOKEN:-}" ] && [ -n "${WS_API_URL:-}" ] && [ -n "${WS_WORKSPACE_KEY:-}" ]; then
    mkdir -p "$HOME/.config/ws"
    # envsubst replaces ${WS_TOKEN}, ${WS_API_URL}, ${WS_WORKSPACE_KEY}
    # from the env we just inherited and writes the rendered toml to the
    # ws CLI's canonical config location.
    envsubst < /etc/windshift/ws.toml.template > "$HOME/.config/ws/config.toml"
    chmod 0600 "$HOME/.config/ws/config.toml"
    ws_config_rendered=true

    # Best-effort: refresh WINDSHIFT.md against the live workspace so the
    # agent sees the workspace's actual item types / statuses / link
    # types. Failures here (e.g. unreachable server in dev) are surfaced
    # as a warning event but do not abort the run — the agent can still
    # invoke `ws` commands directly.
    if [ "${WS_REFRESH_DOCS:-true}" = "true" ]; then
        if ! ws config docs >/dev/null 2>&1; then
            printf '{"type":"warning","source":"ws_config_docs","msg":"refresh failed"}\n'
        fi
    fi
fi

# -----------------------------------------------------------------------
# Phase 1 (WI-84): skeleton lifecycle line
# -----------------------------------------------------------------------
printf '{"type":"lifecycle","phase":"skeleton","run_id":"%s","item_id":"%s","workspace_id":"%s"}\n' \
    "${AGENT_RUN_ID:-unset}" \
    "${WINDSHIFT_ITEM_ID:-unset}" \
    "${WS_WORKSPACE_ID:-unset}"

# -----------------------------------------------------------------------
# Phase 2 (WI-85): workspace mount inspection
# -----------------------------------------------------------------------
if [ -d /workspace ]; then
    readme_present=false
    if [ -f /workspace/README.md ]; then
        readme_present=true
    fi
    printf '{"type":"workspace","mounted":true,"readme":%s}\n' "$readme_present"
fi

# -----------------------------------------------------------------------
# Phase 3 (WI-86): ws config / token presence echo
# -----------------------------------------------------------------------
if [ "$ws_config_rendered" = "true" ]; then
    api_url="$(awk -F'"' '/^[[:space:]]*url[[:space:]]*=/ {print $2; exit}' "$HOME/.config/ws/config.toml" || true)"
    ws_key="$(awk -F'"' '/^[[:space:]]*workspace_key[[:space:]]*=/ {print $2; exit}' "$HOME/.config/ws/config.toml" || true)"
    token_present=false
    if grep -qE '^[[:space:]]*token[[:space:]]*=[[:space:]]*"[^"]+"' "$HOME/.config/ws/config.toml"; then
        token_present=true
    fi
    printf '{"type":"ws_config","api_url":"%s","workspace_key":"%s","token_present":%s}\n' \
        "$api_url" "$ws_key" "$token_present"
fi

# -----------------------------------------------------------------------
# Phase 6 (WI-89) placeholder: when an LLM provider is configured, this
# is where `exec pi --mode rpc --provider $LLM_PROVIDER --model $LLM_MODEL`
# will go. For now we just announce the intent so the orchestrator can
# observe it.
# -----------------------------------------------------------------------
if [ -n "${LLM_PROVIDER:-}" ]; then
    printf '{"type":"lifecycle","phase":"pi_invocation_deferred","llm_provider":"%s","llm_model":"%s"}\n' \
        "${LLM_PROVIDER}" "${LLM_MODEL:-}"
fi

exit 0
