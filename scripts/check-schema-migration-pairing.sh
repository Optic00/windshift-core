#!/usr/bin/env bash
# Guard fresh-install schema changes against missing existing-install
# migrations. The pre-commit hook runs this against the staged index; CI runs
# it against the complete PR/push range.

set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
  scripts/check-schema-migration-pairing.sh --staged
  scripts/check-schema-migration-pairing.sh --range <base>...<head>
  scripts/check-schema-migration-pairing.sh --commit <revision>

Executable changes under internal/database/schema/*.sql must be paired with a
new Migration Version in an internal/database migration catalog source. The
same patch must add CheckSQLite + SQLite fields for SQLite schema changes and
CheckPostgres + Postgres fields for PostgreSQL schema changes.

If a schema edit completes an already-existing migration, add this SQL comment
to every affected schema file instead of inventing another migration:

  -- migration: <existing-version>
EOF
}

if [[ "${SCHEMA_MIGRATION_GUARD_SKIP:-0}" == "1" ]]; then
    echo "  Schema/migration pairing guard skipped via SCHEMA_MIGRATION_GUARD_SKIP=1"
    exit 0
fi

if [[ $# -eq 0 ]]; then
    usage >&2
    exit 2
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

DIFF_ARGS=()
TARGET_KIND=""
TARGET_REV=""

case "$1" in
    --staged)
        [[ $# -eq 1 ]] || { usage >&2; exit 2; }
        DIFF_ARGS=(--cached)
        TARGET_KIND="index"
        ;;
    --range)
        [[ $# -eq 2 ]] || { usage >&2; exit 2; }
        DIFF_ARGS=("$2")
        TARGET_KIND="revision"
        if [[ "$2" == *...* ]]; then
            TARGET_REV="${2##*...}"
        elif [[ "$2" == *..* ]]; then
            TARGET_REV="${2##*..}"
        else
            echo "FAIL: --range expects <base>...<head> or <base>..<head>." >&2
            exit 2
        fi
        ;;
    --commit)
        [[ $# -eq 2 ]] || { usage >&2; exit 2; }
        TARGET_KIND="revision"
        TARGET_REV="$2"
        if parent="$(git rev-parse --verify "$2^" 2>/dev/null)"; then
            DIFF_ARGS=("$parent" "$2")
        else
            # Stable empty-tree object used when linting a repository's root
            # commit, which has no parent to diff against.
            DIFF_ARGS=("4b825dc642cb6eb9a060e54bf8d69288fbee4904" "$2")
        fi
        ;;
    -h|--help)
        usage
        exit 0
        ;;
    *)
        usage >&2
        exit 2
        ;;
esac

git_diff() {
    git diff --no-ext-diff --no-renames --ignore-all-space --unified=0 "${DIFF_ARGS[@]}" -- "$@"
}

git_diff_names() {
    git diff --no-ext-diff --no-renames --ignore-all-space --name-only "${DIFF_ARGS[@]}" -- "$@"
}

target_file() {
    local file="$1"
    if [[ "$TARGET_KIND" == "index" ]]; then
        git show ":$file" 2>/dev/null || true
    else
        git show "$TARGET_REV:$file" 2>/dev/null || true
    fi
}

contains_executable_sql_change() {
    local patch="$1"
    local line content trimmed

    while IFS= read -r line; do
        case "$line" in
            +++\ *|---\ *|@@\ *) continue ;;
            +*|-*) content="${line:1}" ;;
            *) continue ;;
        esac

        # Trim leading whitespace. Comment-only and blank changes do not need
        # a migration; any changed executable fragment does.
        trimmed="${content#"${content%%[![:space:]]*}"}"
        case "$trimmed" in
            ""|--*|/\**|\**|\*/) continue ;;
            *) return 0 ;;
        esac
    done <<< "$patch"

    return 1
}

has_added_catalog_field() {
    local patch="$1"
    local field="$2"
    grep -Eq "^\\+[[:space:]]*${field}:[[:space:]]*" <<< "$patch"
}

catalog_versions="$(
    {
        target_file internal/database/migrations.go
        target_file internal/database/canonical_rank_schema.go
        for catalog_file in internal/database/catalog*.go; do
            target_file "$catalog_file"
        done
    } | sed -nE 's/^[[:space:]]*Version:[[:space:]]*"([^"]+)".*/\1/p'
)"

has_valid_existing_migration_reference() {
    local patch="$1"
    local line version

    while IFS= read -r line; do
        if [[ "$line" =~ ^\+[[:space:]]*--[[:space:]]*migration:[[:space:]]*([A-Za-z0-9_.:-]+)[[:space:]]*$ ]]; then
            version="${BASH_REMATCH[1]}"
            if grep -Fxq "$version" <<< "$catalog_versions"; then
                return 0
            fi
        fi
    done <<< "$patch"

    return 1
}

if ! changed_schema_paths="$(git_diff_names internal/database/schema)"; then
    echo "FAIL: unable to inspect schema changes." >&2
    exit 2
fi

schema_files=()
schema_patches=()
sqlite_changed=0
postgres_changed=0

while IFS= read -r file; do
    [[ -n "$file" && "$file" == *.sql ]] || continue
    if ! patch="$(git_diff "$file")"; then
        echo "FAIL: unable to inspect schema change in $file." >&2
        exit 2
    fi
    if ! contains_executable_sql_change "$patch"; then
        continue
    fi

    schema_files+=("$file")
    schema_patches+=("$patch")
    if [[ "$file" == *_postgres.sql ]]; then
        postgres_changed=1
    else
        sqlite_changed=1
    fi
done <<< "$changed_schema_paths"

if [[ "${#schema_files[@]}" -eq 0 ]]; then
    echo "  Schema/migration pairing guard passed (no executable schema changes)."
    exit 0
fi

if ! catalog_patch="$(git_diff internal/database/migrations.go internal/database/canonical_rank_schema.go internal/database/catalog.go)"; then
    echo "FAIL: unable to inspect migration catalog changes." >&2
    exit 2
fi

if ! has_added_catalog_field "$catalog_patch" "Version"; then
    unpaired_files=()
    for ((i = 0; i < ${#schema_files[@]}; i++)); do
        if ! has_valid_existing_migration_reference "${schema_patches[$i]}"; then
            unpaired_files+=("${schema_files[$i]}")
        fi
    done

    if [[ "${#unpaired_files[@]}" -eq 0 ]]; then
        echo "  Schema/migration pairing guard passed (schema changes reference existing migrations)."
        exit 0
    fi

    echo "FAIL: fresh-install schema changed without a new catalog migration:" >&2
    printf '  %s\n' "${unpaired_files[@]}" >&2
    echo >&2
    echo "Add a new Migration entry with a unique Version to" >&2
    echo "internal/database/migrations.go (or catalog.go) in this commit." >&2
    echo "If this is a follow-up for an existing migration, add this comment" >&2
    echo "to every affected schema file:" >&2
    echo "  -- migration: <existing-version>" >&2
    exit 1
fi

missing_fields=()
if [[ "$sqlite_changed" -eq 1 ]]; then
    has_added_catalog_field "$catalog_patch" "CheckSQLite" || missing_fields+=("CheckSQLite")
    has_added_catalog_field "$catalog_patch" "SQLite" || missing_fields+=("SQLite")
fi
if [[ "$postgres_changed" -eq 1 ]]; then
    has_added_catalog_field "$catalog_patch" "CheckPostgres" || missing_fields+=("CheckPostgres")
    has_added_catalog_field "$catalog_patch" "Postgres" || missing_fields+=("Postgres")
fi

if [[ "${#missing_fields[@]}" -gt 0 ]]; then
    echo "FAIL: the new migration is incomplete for the changed schema backends." >&2
    echo "Missing added catalog fields:" >&2
    printf '  %s\n' "${missing_fields[@]}" >&2
    echo >&2
    echo "Fresh schema and existing-database upgrade paths must change together." >&2
    exit 1
fi

echo "  Schema/migration pairing guard passed."
