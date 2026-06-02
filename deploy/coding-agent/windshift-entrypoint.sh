#!/bin/sh
# Coding-agent runner entrypoint. It prepares per-run Windshift, git, and LLM
# configuration from env injected by RunService, then execs pi in RPC mode.
set -eu

# -----------------------------------------------------------------------
# Windshift CLI config: render ws.toml from injected env.
# -----------------------------------------------------------------------
ws_config_rendered=false
if [ -n "${WS_TOKEN:-}" ] && [ -n "${WS_API_URL:-}" ] && [ -n "${WS_WORKSPACE_KEY:-}" ]; then
    mkdir -p "$HOME/.config/ws"
    envsubst < /etc/windshift/ws.toml.template > "$HOME/.config/ws/config.toml"
    chmod 0600 "$HOME/.config/ws/config.toml"
    ws_config_rendered=true

    # Best-effort: refresh WINDSHIFT.md against the live workspace so the
    # agent sees the workspace's actual item types / statuses / link types.
    if [ "${WS_REFRESH_DOCS:-true}" = "true" ]; then
        if ! ws config docs >/dev/null 2>&1; then
            printf '{"type":"warning","source":"ws_config_docs","msg":"refresh failed"}\n'
        fi
    fi
fi

# -----------------------------------------------------------------------
# Git push auth: turn the forwarded SCM token into an askpass helper.
# -----------------------------------------------------------------------
if [ -n "${AGENT_GIT_TOKEN:-}" ]; then
    askpass_dir="$(mktemp -d)"
    askpass_path="$askpass_dir/askpass.sh"
    cat > "$askpass_path" <<'EOF'
#!/bin/sh
case "$1" in
  Username*) printf 'oauth2\n' ;;
  Password*) printf '%s\n' "$AGENT_GIT_TOKEN" ;;
esac
EOF
    chmod 0700 "$askpass_dir" "$askpass_path"
    export GIT_ASKPASS="$askpass_path"
    export GIT_TERMINAL_PROMPT=0
    # Avoid persisting credentials through any global/system helper in the image.
    git config --global credential.helper '' >/dev/null 2>&1 || true
fi

# -----------------------------------------------------------------------
# LLM config for pi. RunService injects LLM_PROVIDER/LLM_MODEL/LLM_API_KEY
# from the selected Windshift AI connection. For custom base URLs, register a
# per-run provider in ~/.pi/agent/models.json; for built-ins, write auth.json
# with an env-var reference so the key never lands in argv.
# -----------------------------------------------------------------------
pi_provider="${LLM_PROVIDER:-}"
case "$pi_provider" in
    gemini) pi_provider="google" ;;
esac

# Local/custom connections and any connection with an override base URL become
# a generated provider so pi can honor that endpoint.
if [ -n "${LLM_BASE_URL:-}" ] || [ "${LLM_PROVIDER:-}" = "local" ]; then
    safe_provider="$(printf '%s' "${LLM_PROVIDER:-local}" | tr -c 'A-Za-z0-9_-' '-')"
    pi_provider="windshift-${safe_provider}"
fi

if [ -n "${LLM_API_KEY:-}" ] || [ -n "${LLM_BASE_URL:-}" ]; then
    mkdir -p "$HOME/.pi/agent"
    chmod 0700 "$HOME/.pi/agent" 2>/dev/null || true
    PI_PROVIDER="$pi_provider" node <<'NODE'
const fs = require('fs');
const path = require('path');
const home = process.env.HOME || '/home/agent';
const dir = path.join(home, '.pi', 'agent');
fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
const provider = process.env.PI_PROVIDER;
const originalProvider = process.env.LLM_PROVIDER || '';
const model = process.env.LLM_MODEL || '';
const baseUrl = process.env.LLM_BASE_URL || '';
const apiFormat = process.env.LLM_API_FORMAT || 'openai';
const hasKey = !!process.env.LLM_API_KEY;

if (hasKey && provider && !provider.startsWith('windshift-')) {
  const authKey = originalProvider === 'gemini' ? 'google' : provider;
  fs.writeFileSync(
    path.join(dir, 'auth.json'),
    JSON.stringify({ [authKey]: { type: 'api_key', key: 'LLM_API_KEY' } }, null, 2),
    { mode: 0o600 }
  );
}

if (provider && provider.startsWith('windshift-')) {
  const apiMap = {
    anthropic: 'anthropic-messages',
    openai: 'openai-completions',
    gemini: 'google-generative-ai',
    google: 'google-generative-ai',
    zai: 'openai-completions',
    openrouter: 'openai-completions',
    local: 'openai-completions',
  };
  const api = apiMap[originalProvider] || (apiFormat === 'anthropic' ? 'anthropic-messages' : 'openai-completions');
  const models = model ? [{ id: model, name: model }] : [];
  const doc = { providers: { [provider]: { baseUrl, api, apiKey: hasKey ? 'LLM_API_KEY' : 'windshift', models } } };
  fs.writeFileSync(path.join(dir, 'models.json'), JSON.stringify(doc, null, 2), { mode: 0o600 });
}
NODE
fi

# -----------------------------------------------------------------------
# Observability events before pi takes over stdout with RPC JSONL events.
# -----------------------------------------------------------------------
printf '{"type":"lifecycle","phase":"starting","run_id":"%s","item_id":"%s","workspace_id":"%s"}\n' \
    "${AGENT_RUN_ID:-unset}" \
    "${WINDSHIFT_ITEM_ID:-unset}" \
    "${WS_WORKSPACE_ID:-unset}"

if [ -d /workspace ]; then
    readme_present=false
    if [ -f /workspace/README.md ]; then
        readme_present=true
    fi
    printf '{"type":"workspace","mounted":true,"readme":%s}\n' "$readme_present"
    cd /workspace
fi

if [ "$ws_config_rendered" = "true" ]; then
    printf '{"type":"ws_config","api_url":"%s","workspace_key":"%s","token_present":true}\n' \
        "${WS_API_URL:-}" "${WS_WORKSPACE_KEY:-}"
fi

# -----------------------------------------------------------------------
# Exec pi in RPC mode. PiRunner will write the initial prompt to stdin and
# stream stdout/stderr events back to Windshift.
# -----------------------------------------------------------------------
set -- --mode rpc --no-session
if [ -n "$pi_provider" ]; then
    set -- "$@" --provider "$pi_provider"
fi
if [ -n "${LLM_MODEL:-}" ]; then
    set -- "$@" --model "${LLM_MODEL}"
fi

exec pi "$@"
