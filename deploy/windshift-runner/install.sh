#!/usr/bin/env bash
# install.sh — provision a Windshift remote-runner host.
#
# Installs the windshift-runner + windshift-triage binaries, ensures git/docker
# are present, creates a dedicated service user and the bare-clone cache dir,
# and installs a systemd unit + env file. Idempotent: re-running upgrades the
# binaries and leaves an existing /etc/windshift-runner/runner.env untouched.
#
# Binaries come from one of:
#   --core <dir>      build windshift-runner + windshift-triage from a core checkout (needs Go)
#   --bin-dir <dir>   copy prebuilt windshift-runner + windshift-triage from <dir>
# (defaults to the directory containing this script if it holds the binaries)
#
# Usage (as root):
#   sudo ./install.sh --core /path/to/core
#   sudo ./install.sh --bin-dir /path/to/prebuilt
#
# Options:
#   --prefix DIR      install binaries here            (default: /usr/local/bin)
#   --cache-dir DIR   bare-clone cache root            (default: /var/lib/windshift-runner/cache)
#   --user NAME       service user                     (default: windshift-runner)
#   --no-enable       install but don't enable the systemd unit
#   -h, --help
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PREFIX=/usr/local/bin
CACHE_DIR=/var/lib/windshift-runner/cache
SVC_USER=windshift-runner
CORE_DIR=""
BIN_DIR=""
ENABLE=1

die() { echo "install.sh: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --core)      CORE_DIR="${2:?}"; shift 2 ;;
    --bin-dir)   BIN_DIR="${2:?}"; shift 2 ;;
    --prefix)    PREFIX="${2:?}"; shift 2 ;;
    --cache-dir) CACHE_DIR="${2:?}"; shift 2 ;;
    --user)      SVC_USER="${2:?}"; shift 2 ;;
    --no-enable) ENABLE=0; shift ;;
    -h|--help)   sed -n '2,30p' "$0"; exit 0 ;;
    *)           die "unknown option: $1 (try --help)" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "must run as root (use sudo)"

# --- prerequisites ----------------------------------------------------------
have docker || die "docker not found. Install Docker Engine first (https://docs.docker.com/engine/install/)."
have git    || die "git not found. Install it (e.g. apt-get install -y git / dnf install -y git)."
have systemctl || die "systemctl not found. This installer targets systemd hosts."

# --- obtain binaries --------------------------------------------------------
if [ -z "$BIN_DIR" ] && [ -z "$CORE_DIR" ]; then
  if [ -x "$SCRIPT_DIR/windshift-runner" ] && [ -x "$SCRIPT_DIR/windshift-triage" ]; then
    BIN_DIR="$SCRIPT_DIR"
  else
    die "provide --core <checkout> (build from source) or --bin-dir <dir> (prebuilt binaries)"
  fi
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

if [ -n "$CORE_DIR" ]; then
  have go || die "Go toolchain not found but --core was given. Install Go or use --bin-dir."
  [ -d "$CORE_DIR/windshift-runner" ] || die "$CORE_DIR does not look like a core checkout (no windshift-runner/)."
  echo "==> building binaries from $CORE_DIR"
  ( cd "$CORE_DIR" && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$WORK/windshift-runner" ./windshift-runner )
  ( cd "$CORE_DIR" && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$WORK/windshift-triage" ./cmd/windshift-triage )
  BIN_DIR="$WORK"
fi

[ -x "$BIN_DIR/windshift-runner" ]  || die "$BIN_DIR/windshift-runner not found or not executable"
[ -x "$BIN_DIR/windshift-triage" ] || die "$BIN_DIR/windshift-triage not found or not executable"

# --- service user -----------------------------------------------------------
if ! id "$SVC_USER" >/dev/null 2>&1; then
  echo "==> creating system user $SVC_USER"
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
fi
if getent group docker >/dev/null 2>&1; then
  echo "==> adding $SVC_USER to docker group (NOTE: docker access is root-equivalent)"
  usermod -aG docker "$SVC_USER"
else
  echo "WARNING: no docker group found; ensure $SVC_USER can reach the docker socket." >&2
fi

# --- install binaries -------------------------------------------------------
echo "==> installing binaries to $PREFIX"
install -m 0755 "$BIN_DIR/windshift-runner"  "$PREFIX/windshift-runner"
install -m 0755 "$BIN_DIR/windshift-triage" "$PREFIX/windshift-triage"

# --- cache dir --------------------------------------------------------------
echo "==> creating cache dir $CACHE_DIR"
install -d -o "$SVC_USER" -g "$SVC_USER" -m 0750 "$CACHE_DIR"

# --- env file + systemd unit ------------------------------------------------
install -d -m 0755 /etc/windshift-runner
if [ -f /etc/windshift-runner/runner.env ]; then
  echo "==> keeping existing /etc/windshift-runner/runner.env"
else
  echo "==> installing /etc/windshift-runner/runner.env (EDIT IT before starting)"
  install -m 0640 -o root -g "$SVC_USER" "$SCRIPT_DIR/runner.env.example" /etc/windshift-runner/runner.env
fi

echo "==> installing systemd unit"
# Reflect chosen --prefix / --cache-dir / --user into the unit.
sed -e "s#/usr/local/bin/windshift-runner#$PREFIX/windshift-runner#" \
    -e "s#^User=.*#User=$SVC_USER#" \
    -e "s#^ReadWritePaths=.*#ReadWritePaths=$(dirname "$CACHE_DIR")#" \
    "$SCRIPT_DIR/windshift-runner.service" > /etc/systemd/system/windshift-runner.service

systemctl daemon-reload
if [ "$ENABLE" -eq 1 ]; then
  systemctl enable windshift-runner.service >/dev/null
fi

cat <<EOF

Done. windshift-runner + windshift-triage installed to $PREFIX.

Next:
  1. Edit /etc/windshift-runner/runner.env — set WS_API_URL,
     WSRUNNER_REGISTRATION_TOKEN, and WSRUNNER_IMAGE.
     (Set WSRUNNER_TRIAGE_BIN=$PREFIX/windshift-triage and
      WSRUNNER_CACHE_ROOT=$CACHE_DIR if you changed defaults.)
  2. Pull the agent image:  docker pull <WSRUNNER_IMAGE>
  3. Start it:              systemctl start windshift-runner
  4. Watch it register:     journalctl -u windshift-runner -f

The host needs outbound HTTPS to WS_API_URL. The runner holds no SCM
credentials — git reads/writes flow through the orchestrator's git-proxy.
EOF
