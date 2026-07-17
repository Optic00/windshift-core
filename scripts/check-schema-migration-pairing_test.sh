#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="$SCRIPT_DIR/check-schema-migration-pairing.sh"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/schema-migration-guard-XXXXX")"

cleanup() {
    rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

new_repo() {
    local name="$1"
    local repo="$TEST_ROOT/$name"

    git init -q "$repo"
    git -C "$repo" config user.email "schema-guard@example.invalid"
    git -C "$repo" config user.name "Schema Guard Test"
    mkdir -p "$repo/internal/database/schema"
    printf '%s\n' 'CREATE TABLE widgets (id INTEGER PRIMARY KEY);' > "$repo/internal/database/schema/widgets.sql"
    printf '%s\n' 'CREATE TABLE widgets (id SERIAL PRIMARY KEY);' > "$repo/internal/database/schema/widgets_postgres.sql"
    mkdir -p "$repo/internal/database"
    printf '%s\n' \
        'package database' \
        '' \
        'var Catalog = []Migration{' \
        $'\t{' \
        $'\t\tVersion: "existing_widgets",' \
        $'\t},' \
        '}' > "$repo/internal/database/migrations.go"
    printf '%s\n' 'package database' > "$repo/internal/database/catalog.go"
    git -C "$repo" add .
    git -C "$repo" commit -qm "baseline"
    printf '%s\n' "$repo"
}

expect_pass() {
    local name="$1"
    shift
    local output

    if ! output="$("$@" 2>&1)"; then
        echo "FAIL: $name unexpectedly failed" >&2
        echo "$output" >&2
        exit 1
    fi
}

expect_fail() {
    local name="$1"
    local expected="$2"
    shift 2
    local output

    if output="$("$@" 2>&1)"; then
        echo "FAIL: $name unexpectedly passed" >&2
        exit 1
    fi
    if [[ "$output" != *"$expected"* ]]; then
        echo "FAIL: $name did not report '$expected'" >&2
        echo "$output" >&2
        exit 1
    fi
}

repo="$(new_repo no_changes)"
expect_pass "no staged changes" bash -c "cd '$repo' && '$GUARD' --staged"

repo="$(new_repo comments_only)"
printf '%s\n' '-- clarified table ownership' >> "$repo/internal/database/schema/widgets.sql"
git -C "$repo" add internal/database/schema/widgets.sql
expect_pass "comment-only schema edit" bash -c "cd '$repo' && '$GUARD' --staged"

repo="$(new_repo whitespace_only)"
printf '%s\n' 'CREATE  TABLE widgets ( id INTEGER PRIMARY KEY );' > "$repo/internal/database/schema/widgets.sql"
git -C "$repo" add internal/database/schema/widgets.sql
expect_pass "whitespace-only schema edit" bash -c "cd '$repo' && '$GUARD' --staged"

repo="$(new_repo missing_migration)"
printf '%s\n' "INSERT INTO contact_roles(name) VALUES ('Portal Customer');" >> "$repo/internal/database/schema/widgets.sql"
git -C "$repo" add internal/database/schema/widgets.sql
expect_fail "fresh-schema seed without migration" "without a new catalog migration" \
    bash -c "cd '$repo' && '$GUARD' --staged"

repo="$(new_repo paired_sqlite)"
printf '%s\n' 'CREATE INDEX idx_widgets_id ON widgets(id);' >> "$repo/internal/database/schema/widgets.sql"
printf '%s\n' \
    $'\t{' \
    $'\t\tVersion: "20260717_widgets_index",' \
    $'\t\tCheckSQLite: "SELECT 1",' \
    $'\t\tSQLite: "CREATE INDEX idx_widgets_id ON widgets(id)",' \
    $'\t},' >> "$repo/internal/database/migrations.go"
git -C "$repo" add internal/database/schema/widgets.sql internal/database/migrations.go
expect_pass "paired SQLite schema edit" bash -c "cd '$repo' && '$GUARD' --staged"

repo="$(new_repo incomplete_postgres)"
printf '%s\n' 'CREATE INDEX idx_widgets_id ON widgets(id);' >> "$repo/internal/database/schema/widgets_postgres.sql"
printf '%s\n' \
    $'\t{' \
    $'\t\tVersion: "20260717_widgets_index",' \
    $'\t\tCheckPostgres: "SELECT 1",' \
    $'\t},' >> "$repo/internal/database/migrations.go"
git -C "$repo" add internal/database/schema/widgets_postgres.sql internal/database/migrations.go
expect_fail "PostgreSQL migration without body" "Postgres" \
    bash -c "cd '$repo' && '$GUARD' --staged"

repo="$(new_repo existing_reference)"
printf '%s\n' \
    '-- migration: existing_widgets' \
    'CREATE INDEX idx_widgets_id ON widgets(id);' >> "$repo/internal/database/schema/widgets.sql"
git -C "$repo" add internal/database/schema/widgets.sql
expect_pass "reference to existing migration" bash -c "cd '$repo' && '$GUARD' --staged"

repo="$(new_repo invalid_reference)"
printf '%s\n' \
    '-- migration: does_not_exist' \
    'CREATE INDEX idx_widgets_id ON widgets(id);' >> "$repo/internal/database/schema/widgets.sql"
git -C "$repo" add internal/database/schema/widgets.sql
expect_fail "reference must name a real migration" "without a new catalog migration" \
    bash -c "cd '$repo' && '$GUARD' --staged"

repo="$(new_repo unstaged_migration)"
printf '%s\n' 'CREATE INDEX idx_widgets_id ON widgets(id);' >> "$repo/internal/database/schema/widgets.sql"
git -C "$repo" add internal/database/schema/widgets.sql
printf '%s\n' $'\t{Version: "unstaged_widgets"},' >> "$repo/internal/database/migrations.go"
expect_fail "unstaged migration does not satisfy staged schema" "without a new catalog migration" \
    bash -c "cd '$repo' && '$GUARD' --staged"

repo="$(new_repo range_mode)"
base="$(git -C "$repo" rev-parse HEAD)"
printf '%s\n' 'CREATE INDEX idx_widgets_id ON widgets(id);' >> "$repo/internal/database/schema/widgets.sql"
git -C "$repo" add internal/database/schema/widgets.sql
git -C "$repo" commit -qm "schema only"
expect_fail "range catches schema-only commit" "without a new catalog migration" \
    bash -c "cd '$repo' && '$GUARD' --range '$base...HEAD'"
printf '%s\n' \
    $'\t{' \
    $'\t\tVersion: "20260717_widgets_index",' \
    $'\t\tCheckSQLite: "SELECT 1",' \
    $'\t\tSQLite: "CREATE INDEX idx_widgets_id ON widgets(id)",' \
    $'\t},' >> "$repo/internal/database/migrations.go"
git -C "$repo" add internal/database/migrations.go
git -C "$repo" commit -qm "add migration"
expect_pass "range accepts paired aggregate" bash -c "cd '$repo' && '$GUARD' --range '$base...HEAD'"

echo "Schema/migration pairing guard tests passed."
