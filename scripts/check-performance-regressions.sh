#!/usr/bin/env bash

set -euo pipefail

# These ceilings deliberately leave substantial room for shared CI runners.
# They are intended to catch architectural regressions (returning to the
# 83 ms/O(workspaces) permission path or the 320 us history scan), not small
# machine-to-machine benchmark variance.
item_status_max_ns_per_op="${ITEM_STATUS_MAX_NS_PER_OP:-100000}"
item_status_max_bytes_per_op="${ITEM_STATUS_MAX_BYTES_PER_OP:-4096}"
item_status_max_allocs_per_op="${ITEM_STATUS_MAX_ALLOCS_PER_OP:-100}"
workspace_access_max_ns_per_op="${WORKSPACE_ACCESS_MAX_NS_PER_OP:-1000000}"
workspace_access_max_bytes_per_op="${WORKSPACE_ACCESS_MAX_BYTES_PER_OP:-1048576}"
workspace_access_max_allocs_per_op="${WORKSPACE_ACCESS_MAX_ALLOCS_PER_OP:-10000}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

go test ./internal/repository \
  -run '^$' \
  -bench '^BenchmarkCurrentStatusTransitionLookup/current_status_index_parallel$' \
  -benchmem \
  -count=3 | tee "$tmpdir/item-status.txt"

go test ./internal/services \
  -run '^$' \
  -bench '^BenchmarkAccessibleWorkspaceIDs/snapshot_1000_workspaces_parallel$' \
  -benchmem \
  -count=3 | tee "$tmpdir/workspace-access.txt"

max_metric() {
  local file="$1"
  local label="$2"
  awk -v label="$label" '
    /^Benchmark/ {
      for (i = 2; i <= NF; i++) {
        if ($i == label && $(i - 1) + 0 > max) max = $(i - 1) + 0
      }
    }
    END {
      if (max == "") exit 1
      print max
    }
  ' "$file"
}

assert_at_most() {
  local name="$1"
  local actual="$2"
  local maximum="$3"
  awk -v name="$name" -v actual="$actual" -v maximum="$maximum" 'BEGIN {
    if (actual > maximum) {
      printf "%s regression: %.3f exceeds ceiling %.3f\n", name, actual, maximum > "/dev/stderr"
      exit 1
    }
    printf "%s: %.3f (ceiling %.3f)\n", name, actual, maximum
  }'
}

assert_exactly() {
  local name="$1"
  local actual="$2"
  local expected="$3"
  awk -v name="$name" -v actual="$actual" -v expected="$expected" 'BEGIN {
    if (actual != expected) {
      printf "%s regression: %.3f, want %.3f\n", name, actual, expected > "/dev/stderr"
      exit 1
    }
    printf "%s: %.3f\n", name, actual
  }'
}

item_status_ns="$(max_metric "$tmpdir/item-status.txt" ns/op)"
item_status_bytes="$(max_metric "$tmpdir/item-status.txt" B/op)"
item_status_allocs="$(max_metric "$tmpdir/item-status.txt" allocs/op)"
workspace_access_ns="$(max_metric "$tmpdir/workspace-access.txt" ns/op)"
workspace_access_bytes="$(max_metric "$tmpdir/workspace-access.txt" B/op)"
workspace_access_allocs="$(max_metric "$tmpdir/workspace-access.txt" allocs/op)"
workspace_access_decodes="$(max_metric "$tmpdir/workspace-access.txt" permission-decodes/op)"

assert_at_most "current-status lookup ns/op" "$item_status_ns" "$item_status_max_ns_per_op"
assert_at_most "current-status lookup B/op" "$item_status_bytes" "$item_status_max_bytes_per_op"
assert_at_most "current-status lookup allocs/op" "$item_status_allocs" "$item_status_max_allocs_per_op"
assert_at_most "workspace access ns/op" "$workspace_access_ns" "$workspace_access_max_ns_per_op"
assert_at_most "workspace access B/op" "$workspace_access_bytes" "$workspace_access_max_bytes_per_op"
assert_at_most "workspace access allocs/op" "$workspace_access_allocs" "$workspace_access_max_allocs_per_op"
assert_exactly "workspace permission decodes/op" "$workspace_access_decodes" 1
