#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

export GOLANGCI_LINT_CACHE="$REPO_ROOT/.golangci-lint-cache"
mkdir -p "$GOLANGCI_LINT_CACHE"

cd "$REPO_ROOT"
exec golangci-lint "$@"
