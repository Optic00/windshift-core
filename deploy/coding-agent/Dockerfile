# =============================================================================
# Windshift Coding Agent Runner
#
# Per-run sandbox the coding-agent harness spawns to execute pi-coding-agent
# against a mounted worktree. Phase 3 (WI-86) is the first variant that
# bakes the real ws CLI + pi-coding-agent + windshift-guard.ts extension +
# ws.toml.template; Phase 1-2 (WI-84 / WI-85) shipped a node-only skeleton.
#
# Built from the *repo root* (the build context needs cmd/ws + go.mod):
#
#   docker build -f deploy/coding-agent/Dockerfile \
#     -t windshift/coding-agent:wi-86-skeleton \
#     [--build-arg INSTALL_PI=false] [--build-arg PI_VERSION=<tag>] \
#     .
#
# Set INSTALL_PI=false when iterating on the ws/env path locally — it skips
# the multi-MB npm install. Production builds keep INSTALL_PI=true (default).
# =============================================================================

# --------------------------------------------------------------------
# Stage 1: cross-compile the ws CLI for linux/<TARGETARCH>
# --------------------------------------------------------------------
FROM golang:1.26-bookworm AS ws-build

WORKDIR /src

# Cache module downloads ahead of source copy so changes outside cmd/ws
# don't blow the layer cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ws ./cmd/ws

# --------------------------------------------------------------------
# Stage 2: runner image
# --------------------------------------------------------------------
FROM node:lts-slim AS runner

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        gettext-base \
        git \
    && rm -rf /var/lib/apt/lists/*

# pi-coding-agent install. PI_VERSION must resolve to a published tag on
# @earendil-works/pi-coding-agent. WI-89 (Phase 6) is where the
# orchestrator actually drives `pi --mode rpc`; until then the binary is
# along for the ride.
ARG INSTALL_PI=true
ARG PI_VERSION=latest
RUN if [ "${INSTALL_PI}" = "true" ]; then \
        npm install -g --omit=dev --ignore-scripts \
            "@earendil-works/pi-coding-agent@${PI_VERSION}"; \
    fi

RUN groupadd --gid 1000 agent \
    && useradd --uid 1000 --gid 1000 --create-home --home-dir /home/agent agent

# ws binary, baked extension, ws.toml template, entrypoint.
COPY --from=ws-build /out/ws /usr/local/bin/ws
COPY deploy/coding-agent/ws.toml.template /etc/windshift/ws.toml.template
COPY deploy/coding-agent/windshift-guard.ts /opt/windshift/extensions/windshift-guard.ts
COPY deploy/coding-agent/windshift-entrypoint.sh /usr/local/bin/windshift-entrypoint.sh

# Per-user state: agent owns its ~/.config/ws and ~/.pi tree. The
# extension is copied (not bind-symlinked) so the agent user owns it
# without giving write access to /opt/windshift.
RUN install -d -o agent -g agent -m 0750 /home/agent/.config/ws \
    && install -d -o agent -g agent -m 0750 /home/agent/.pi/agent/extensions \
    && cp /opt/windshift/extensions/windshift-guard.ts /home/agent/.pi/agent/extensions/windshift-guard.ts \
    && chown -R agent:agent /home/agent \
    && chmod +x /usr/local/bin/windshift-entrypoint.sh /usr/local/bin/ws \
    && chmod 0644 /etc/windshift/ws.toml.template

USER agent
WORKDIR /home/agent
ENV PATH=/usr/local/bin:$PATH

ENTRYPOINT ["/usr/local/bin/windshift-entrypoint.sh"]
