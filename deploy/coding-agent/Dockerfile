# =============================================================================
# Windshift Coding Agent Runner
#
# Per-run sandbox the coding-agent harness spawns to execute pi-coding-agent
# against a mounted worktree. This is the Phase 1 walking skeleton (WI-84):
# node base + entrypoint that emits one NDJSON lifecycle line and exits.
#
# Phase 3 (WI-86) replaces the stub entrypoint with:
#   - pinned pi-coding-agent install
#   - baked ws CLI + ws.toml.template + canonical WINDSHIFT.md
#   - baked ~/.pi/agent/extensions/windshift-guard.ts
#   - envsubst → ws.toml + exec pi --mode rpc
# =============================================================================

FROM node:lts-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        gettext-base \
        git \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd --system agent \
    && useradd --system --gid agent --create-home --home-dir /home/agent agent

COPY windshift-entrypoint.sh /usr/local/bin/windshift-entrypoint.sh
RUN chmod +x /usr/local/bin/windshift-entrypoint.sh

USER agent
WORKDIR /home/agent

ENTRYPOINT ["/usr/local/bin/windshift-entrypoint.sh"]
