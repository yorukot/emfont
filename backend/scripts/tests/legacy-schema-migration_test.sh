#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
backend_dir="$(cd -- "$script_dir/../.." && pwd)"
migration_dir="$backend_dir/db/migrations"
fixture="$script_dir/fixtures/legacy-production-schema.sql"
postgres_image="${EMFONT_LEGACY_TEST_POSTGRES_IMAGE:-postgres:16.14-bookworm@sha256:da788743d2060767375896de4d646f7576f5911461444b372616f19ea61db2ec}"
postgres_user='emfont_legacy_test'
postgres_password='emfont-legacy-test-password'
container="emfont-legacy-migration-${BASHPID}"
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/emfont-legacy-migration.XXXXXX")"
migrate_bin="$temp_dir/emfont-migrate"

cleanup() {
    docker rm --force "$container" >/dev/null 2>&1 || true
    rm -rf "$temp_dir"
}
trap cleanup EXIT HUP INT TERM

fail() {
    printf 'legacy schema migration test failed: %s\n' "$*" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

assert_equal() {
    local expected="$1"
    local actual="$2"
    local description="$3"
    [[ "$actual" == "$expected" ]] ||
        fail "$description: expected [$expected], got [$actual]"
}

require_command docker
require_command go
require_command diff
docker info >/dev/null 2>&1 || fail 'Docker daemon is unavailable'
[[ -r "$fixture" ]] || fail "fixture is not readable: $fixture"

(
    cd "$backend_dir"
    go build -o "$migrate_bin" ./cmd/migrate
)

docker run --detach \
    --name "$container" \
    --env "POSTGRES_USER=$postgres_user" \
    --env "POSTGRES_PASSWORD=$postgres_password" \
    --env 'POSTGRES_DB=postgres' \
    --publish '127.0.0.1::5432' \
    --health-cmd "pg_isready --username=$postgres_user --dbname=postgres" \
    --health-interval 1s \
    --health-timeout 3s \
    --health-retries 30 \
    "$postgres_image" >/dev/null

for _ in {1..60}; do
    status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container")"
    case "$status" in
        healthy)
            break
            ;;
        unhealthy|exited|dead)
            docker logs "$container" >&2
            fail "PostgreSQL container reached terminal status $status"
            ;;
    esac
    sleep 1
done

status="$(docker inspect --format '{{.State.Health.Status}}' "$container")"
if [[ "$status" != 'healthy' ]]; then
    docker logs "$container" >&2
    fail 'PostgreSQL container did not become healthy'
fi

host_port="$(docker port "$container" 5432/tcp | awk -F: 'NR == 1 { print $NF }')"
[[ "$host_port" =~ ^[0-9]+$ ]] || fail "could not determine PostgreSQL host port: $host_port"

database_url() {
    local database="$1"
    printf 'postgres://%s:%s@127.0.0.1:%s/%s?sslmode=disable' \
        "$postgres_user" "$postgres_password" "$host_port" "$database"
}

psql_command() {
    local database="$1"
    shift
    docker exec --interactive \
        --env "PGPASSWORD=$postgres_password" \
        "$container" \
        psql --no-psqlrc --set ON_ERROR_STOP=1 \
        --username "$postgres_user" --dbname "$database" "$@"
}

sql_scalar() {
    local database="$1"
    local query="$2"
    psql_command "$database" --quiet --tuples-only --no-align --command "$query"
}

sql_exec() {
    local database="$1"
    local query="$2"
    psql_command "$database" --quiet --command "$query" >/dev/null
}

run_migration() {
    local database="$1"
    local command="$2"
    "$migrate_bin" \
        -command "$command" \
        -dir "$migration_dir" \
        -database-connection-string "$(database_url "$database")"
}

applied_versions() {
    local database="$1"
    sql_scalar "$database" "
        WITH latest AS (
            SELECT DISTINCT ON (version_id) version_id, is_applied
            FROM goose_db_version
            ORDER BY version_id, id DESC
        )
        SELECT COALESCE(string_agg(version_id::TEXT, ',' ORDER BY version_id), '')
        FROM latest
        WHERE is_applied
          AND version_id > 0"
}

migration_versions_on_disk() {
    local path
    local filename
    local padded_version
    local version
    local separator=''

    for path in "$migration_dir"/[0-9]*.sql; do
        filename="${path##*/}"
        padded_version="${filename%%_*}"
        version=$((10#$padded_version))
        printf '%s%s' "$separator" "$version"
        separator=','
    done
    printf '\n'
}

latest_applied_version() {
    local database="$1"
    sql_scalar "$database" "
        WITH latest AS (
            SELECT DISTINCT ON (version_id) version_id, is_applied
            FROM goose_db_version
            ORDER BY version_id, id DESC
        )
        SELECT COALESCE(MAX(version_id) FILTER (WHERE is_applied AND version_id > 0), 0)
        FROM latest"
}

catalog_signature() {
    local database="$1"
    docker exec --interactive \
        --env "PGPASSWORD=$postgres_password" \
        "$container" \
        psql --no-psqlrc --set ON_ERROR_STOP=1 --quiet --tuples-only --no-align \
        --field-separator '|' --username "$postgres_user" --dbname "$database" <<'SQL'
SELECT
    'column',
    relation.relname,
    attribute.attname,
    format_type(attribute.atttypid, attribute.atttypmod),
    attribute.attnotnull,
    COALESCE(pg_get_expr(default_value.adbin, default_value.adrelid), '<none>')
FROM pg_class AS relation
JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
JOIN pg_attribute AS attribute ON attribute.attrelid = relation.oid
LEFT JOIN pg_attrdef AS default_value
    ON default_value.adrelid = relation.oid
   AND default_value.adnum = attribute.attnum
WHERE namespace.nspname = 'public'
  AND relation.relname = ANY (ARRAY[
	      'system_metadata', 'demo_sentence', 'font_family', 'version',
	      'static_fonts', 'font_sources', 'font_artifacts', 'font_build_jobs',
	      'font_artifact_quota', 'font_terminal_failures'
  ])
  AND attribute.attnum > 0
  AND NOT attribute.attisdropped
ORDER BY relation.relname, attribute.attnum;

SELECT
    'constraint',
    relation.relname,
    constraint_value.conname,
    constraint_value.contype,
    pg_get_constraintdef(constraint_value.oid, TRUE),
    constraint_value.convalidated
FROM pg_constraint AS constraint_value
JOIN pg_class AS relation ON relation.oid = constraint_value.conrelid
JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = 'public'
  AND relation.relname = ANY (ARRAY[
	      'system_metadata', 'demo_sentence', 'font_family', 'version',
	      'static_fonts', 'font_sources', 'font_artifacts', 'font_build_jobs',
	      'font_artifact_quota', 'font_terminal_failures'
  ])
ORDER BY relation.relname, constraint_value.conname;

SELECT 'index', tablename, indexname, indexdef
FROM pg_indexes
WHERE schemaname = 'public'
  AND tablename = ANY (ARRAY[
	      'system_metadata', 'demo_sentence', 'font_family', 'version',
	      'static_fonts', 'font_sources', 'font_artifacts', 'font_build_jobs',
	      'font_artifact_quota', 'font_terminal_failures'
  ])
ORDER BY tablename, indexname;

SELECT
    'sequence',
    sequencename,
    data_type,
    start_value,
    min_value,
    max_value,
    increment_by,
    cycle,
    cache_size
FROM pg_sequences
WHERE schemaname = 'public'
  AND sequencename = ANY (ARRAY[
      'demo_sentence_sid_seq', 'custom_bullet_seq', 'font_sources_id_seq',
      'font_build_jobs_id_seq', 'font_artifact_fence_seq'
  ])
ORDER BY sequencename;

SELECT
    'sequence-owner',
    sequence_relation.relname,
    COALESCE(table_relation.relname, ''),
    COALESCE(attribute.attname, '')
FROM pg_class AS sequence_relation
JOIN pg_namespace AS namespace ON namespace.oid = sequence_relation.relnamespace
LEFT JOIN pg_depend AS dependency
    ON dependency.classid = 'pg_class'::REGCLASS
   AND dependency.objid = sequence_relation.oid
   AND dependency.objsubid = 0
   AND dependency.refclassid = 'pg_class'::REGCLASS
   AND dependency.refobjsubid > 0
   AND dependency.deptype IN ('a', 'i')
LEFT JOIN pg_class AS table_relation ON table_relation.oid = dependency.refobjid
LEFT JOIN pg_attribute AS attribute
    ON attribute.attrelid = dependency.refobjid
   AND attribute.attnum = dependency.refobjsubid
WHERE namespace.nspname = 'public'
  AND sequence_relation.relkind = 'S'
  AND sequence_relation.relname = ANY (ARRAY[
      'demo_sentence_sid_seq', 'custom_bullet_seq', 'font_sources_id_seq',
      'font_build_jobs_id_seq', 'font_artifact_fence_seq'
  ])
ORDER BY sequence_relation.relname;
SQL
}

data_fingerprint() {
    local database="$1"
    sql_scalar "$database" "
        SELECT md5(concat_ws(E'\\n',
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY sid)::TEXT, '[]') FROM demo_sentence AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY id)::TEXT, '[]') FROM font_family AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY bullet)::TEXT, '[]') FROM version AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY char)::TEXT, '[]') FROM static_fonts AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY id)::TEXT, '[]') FROM dynamic_fonts AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY prefix, file_name)::TEXT, '[]') FROM r2_files AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY id)::TEXT, '[]') FROM usage_log AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY user_id)::TEXT, '[]') FROM admin_users AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY version)::TEXT, '[]') FROM schemaversion AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY metadata_key)::TEXT, '[]') FROM system_metadata AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY id)::TEXT, '[]') FROM font_sources AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY artifact_key)::TEXT, '[]') FROM font_artifacts AS row_value),
            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY id)::TEXT, '[]') FROM font_build_jobs AS row_value),
	            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY singleton)::TEXT, '[]') FROM font_artifact_quota AS row_value),
	            (SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY artifact_key)::TEXT, '[]') FROM font_terminal_failures AS row_value),
            (SELECT last_value::TEXT || ':' || is_called::TEXT FROM demo_sentence_sid_seq),
            (SELECT last_value::TEXT || ':' || is_called::TEXT FROM custom_bullet_seq)
        ))"
}

legacy_only_fingerprint() {
    local database="$1"
    sql_scalar "$database" "
        SELECT md5(concat_ws(E'\\n',
            (SELECT jsonb_agg(to_jsonb(row_value) ORDER BY id)::TEXT FROM dynamic_fonts AS row_value),
            (SELECT jsonb_agg(to_jsonb(row_value) ORDER BY prefix, file_name)::TEXT FROM r2_files AS row_value),
            (SELECT jsonb_agg(to_jsonb(row_value) ORDER BY id)::TEXT FROM usage_log AS row_value),
            (SELECT jsonb_agg(to_jsonb(row_value) ORDER BY user_id)::TEXT FROM admin_users AS row_value),
            (SELECT jsonb_agg(to_jsonb(row_value) ORDER BY version)::TEXT FROM schemaversion AS row_value)
        ))"
}

psql_command postgres --quiet --command 'CREATE DATABASE fresh' >/dev/null
psql_command postgres --quiet --command 'CREATE DATABASE legacy' >/dev/null
psql_command postgres --quiet --command \
    "ALTER DATABASE legacy SET timezone TO 'America/Los_Angeles'" >/dev/null
psql_command legacy --quiet <"$fixture" >/dev/null
legacy_only_before="$(legacy_only_fingerprint legacy)"

expected_versions="$(migration_versions_on_disk)"
[[ ",$expected_versions," == *,1,2,3,4,5,6,7,8,9,10,* ]] ||
	    fail "migration chain does not contain versions 1 through 10: $expected_versions"

run_migration fresh up
run_migration legacy up
assert_equal "$expected_versions" "$(applied_versions fresh)" 'fresh migration versions'
assert_equal "$expected_versions" "$(applied_versions legacy)" 'legacy migration versions'
assert_equal "$legacy_only_before" "$(legacy_only_fingerprint legacy)" \
    'legacy-only data after upgrade'

catalog_signature fresh >"$temp_dir/fresh.catalog"
catalog_signature legacy >"$temp_dir/legacy.catalog"
if ! diff --unified "$temp_dir/fresh.catalog" "$temp_dir/legacy.catalog"; then
    fail 'legacy target catalog differs from a fresh database'
fi

assert_equal '2' "$(sql_scalar legacy "SELECT count(*) FROM font_family WHERE id LIKE 'legacy-%'")" \
    'legacy font family count'
assert_equal 't' "$(sql_scalar legacy "
    SELECT weights = ARRAY[]::SMALLINT[]
       AND tags = ARRAY[]::TEXT[]
       AND authors = ARRAY[]::TEXT[]
       AND format = 'ttf'
       AND languages = '{\"Han\": 1}'::JSONB
       AND demo_content_id = 1
    FROM font_family
    WHERE id = 'legacy-null'")" 'nullable font family backfill'
assert_equal 't' "$(sql_scalar legacy "
    SELECT weights = ARRAY[400]::SMALLINT[]
       AND tags = ARRAY['old']::TEXT[]
       AND authors = ARRAY['author']::TEXT[]
       AND format = 'otf'
       AND languages = '{\"Latin\": 2}'::JSONB
       AND demo_content_id = 42
       AND category = 'serif'
    FROM font_family
    WHERE id = 'legacy-data'")" 'populated font family preservation'
assert_equal 't' "$(sql_scalar legacy "
    SELECT families = ARRAY[]::TEXT[] AND pack = 7 AND use_count = 3
    FROM static_fonts
    WHERE char = 'A'")" 'static font backfill'
assert_equal 't' "$(sql_scalar legacy "
    SELECT start = TIMESTAMPTZ '2024-01-02 03:04:05+00'
    FROM version
    WHERE bullet = 175")" 'legacy UTC timestamp conversion'
assert_equal 't' "$(sql_scalar legacy "
    SELECT to_regclass('public.dynamic_fonts') IS NOT NULL
       AND to_regclass('public.r2_files') IS NOT NULL
       AND to_regclass('public.usage_log') IS NOT NULL
       AND to_regclass('public.admin_users') IS NOT NULL
       AND to_regclass('public.schemaversion') IS NOT NULL
       AND (SELECT count(*) FROM dynamic_fonts) = 1
       AND (SELECT count(*) FROM r2_files) = 1
       AND (SELECT count(*) FROM usage_log) = 1
       AND (SELECT count(*) FROM admin_users WHERE role = 'super_admin') = 1
       AND (SELECT count(*) FROM schemaversion) = 5
       AND (SELECT max(version) FROM schemaversion) = 4")" \
    'legacy-only table preservation'

sql_exec legacy "
    INSERT INTO font_family (id, name, category, format)
    VALUES ('post-upgrade-contract', 'Post Upgrade Contract', 'display', 'woff2')"
assert_equal 't' "$(sql_scalar legacy "
    SELECT weights = ARRAY[]::SMALLINT[]
       AND tags = ARRAY[]::TEXT[]
       AND authors = ARRAY[]::TEXT[]
       AND demo_content_id IS NULL
    FROM font_family
    WHERE id = 'post-upgrade-contract'")" 'reconciled defaults and constraints'
sql_exec legacy "DELETE FROM font_family WHERE id = 'post-upgrade-contract'"

assert_equal '43' "$(sql_scalar legacy "SELECT nextval('demo_sentence_sid_seq')")" \
    'demo sentence sequence reconciliation'
assert_equal '44' "$(sql_scalar legacy "
    INSERT INTO demo_sentence (content, language)
    VALUES ('post-upgrade sentence', 'en')
    RETURNING sid")" 'demo sentence default ID after reconciliation'
assert_equal '176' "$(sql_scalar legacy "SELECT nextval('custom_bullet_seq')")" \
    'version sequence reconciliation'
assert_equal '177' "$(sql_scalar legacy "INSERT INTO version DEFAULT VALUES RETURNING bullet")" \
    'version default ID after reconciliation'
assert_equal 't' "$(sql_scalar legacy "
    SELECT EXISTS (SELECT 1 FROM demo_sentence WHERE sid = 42 AND content = 'legacy sentence forty two')
       AND EXISTS (SELECT 1 FROM version WHERE bullet = 175)")" 'explicit-ID row preservation'

before_reentry="$(data_fingerprint legacy)"
run_migration fresh up
run_migration legacy up
assert_equal "$before_reentry" "$(data_fingerprint legacy)" 'second up data fingerprint'

while (( $(latest_applied_version legacy) > 7 )); do
    run_migration legacy down
done
assert_equal '1,2,3,4,5,6,7' "$(applied_versions legacy)" \
    'versions before reconciliation down'
run_migration legacy down
assert_equal '1,2,3,4,5,6' "$(applied_versions legacy)" 'versions after reconciliation down'
run_migration legacy up
assert_equal "$expected_versions" "$(applied_versions legacy)" 'versions after reconciliation re-up'
assert_equal "$before_reentry" "$(data_fingerprint legacy)" 'down/up data fingerprint'

catalog_signature legacy >"$temp_dir/legacy-reentry.catalog"
if ! diff --unified "$temp_dir/fresh.catalog" "$temp_dir/legacy-reentry.catalog"; then
    fail 'legacy target catalog drifted after down/up re-entry'
fi

printf 'legacy schema migration test passed (PostgreSQL image: %s)\n' "$postgres_image"
