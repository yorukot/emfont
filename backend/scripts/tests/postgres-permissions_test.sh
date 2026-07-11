#!/bin/bash
set -Eeuo pipefail
umask 077

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
temp_dir=$(mktemp -d /tmp/emfont-postgres-permissions-test.XXXXXX)
postgres_container="emfont-postgres-permissions-test-$$"
cleanup() {
    docker rm -f "$postgres_container" >/dev/null 2>&1 || true
    rm -rf "$temp_dir"
}
report_error() {
    local exit_code=$?
    trap - ERR
    printf 'postgres-permissions test failed at line %s\n' \
        "${BASH_LINENO[0]}" >&2
    exit "$exit_code"
}
trap cleanup EXIT HUP INT TERM
trap report_error ERR

app_password=$'app secret \' " ; : $() back\\slash 20260711'
postgres_image=${EMFONT_POSTGRES_TEST_IMAGE:-emfont-postgres:final}
printf '%s' "$app_password" >"$temp_dir/expected-password"
: >"$temp_dir/fake-calls"

cat >"$temp_dir/psql" <<'FAKE'
#!/bin/sh
set -eu

expected=$(cat "$FAKE_EXPECTED_PASSWORD_FILE")
for argument in "$@"; do
    case "$argument" in
        *"$expected"*)
            printf 'application password reached psql argv\n' >&2
            exit 90
            ;;
    esac
done
[ "${EMFONT_POSTGRES_APP_PASSWORD+x}" != x ] || {
    printf 'application password reached psql environment\n' >&2
    exit 91
}
case "${PGOPTIONS:-}" in
    *'password_encryption=scram-sha-256'*'log_statement=none'*'log_min_error_statement=panic'*'log_parameter_max_length_on_error=0'*) ;;
    *)
        printf 'restrictive PostgreSQL logging options are missing\n' >&2
        exit 92
        ;;
esac

call=$(( $(wc -l <"$FAKE_CALL_LOG") + 1 ))
input=$(cat)
printf 'call-%s\n' "$call" >>"$FAKE_CALL_LOG"

case "$call" in
    1)
        case "$input" in
            *"$expected"* | *'CREATE ROLE %I LOGIN'* | *'PASSWORD %L'*)
                printf 'password-bearing initial SQL\n' >&2
                exit 93
                ;;
        esac
        case "$input" in
            *'CREATE ROLE %I NOLOGIN'*'ALTER ROLE %I WITH NOLOGIN'*) ;;
            *)
                printf 'initial NOLOGIN reconciliation is missing\n' >&2
                exit 94
                ;;
        esac
        ;;
    2)
        expected_input=$(printf '\\password emfont_app\n%s\n%s' "$expected" "$expected")
        [ "$input" = "$expected_input" ] || {
            printf 'password prompt protocol is malformed\n' >&2
            exit 95
        }
        if [ "${FAKE_FAIL_PASSWORD:-0}" = 1 ]; then
            printf 'simulated password protocol failure\n' >&2
            exit 96
        fi
        ;;
    3)
        case "$input" in
            *"$expected"*)
                printf 'application password reached final SQL\n' >&2
                exit 97
                ;;
        esac
        case "$input" in
            *"rolpassword LIKE 'SCRAM-SHA-256\$%'"*'ALTER ROLE %I WITH LOGIN'*'COMMIT;'*) ;;
            *)
                printf 'SCRAM verification or final reconciliation is missing\n' >&2
                exit 98
                ;;
        esac
        ;;
    *)
        printf 'unexpected psql invocation\n' >&2
        exit 99
        ;;
esac
FAKE
chmod 0500 "$temp_dir/psql"

run_fake() {
    PATH="$temp_dir:$PATH" \
    FAKE_EXPECTED_PASSWORD_FILE="$temp_dir/expected-password" \
    FAKE_CALL_LOG="$temp_dir/fake-calls" \
    FAKE_FAIL_PASSWORD="${FAKE_FAIL_PASSWORD:-0}" \
    EMFONT_POSTGRES_HOST=postgres \
    EMFONT_POSTGRES_ADMIN_USER=emfont_admin \
    EMFONT_POSTGRES_APP_USER=emfont_app \
    EMFONT_POSTGRES_DB=emfont \
    EMFONT_POSTGRES_APP_PASSWORD="${TEST_APP_PASSWORD:-$app_password}" \
    PGPASSWORD=admin-secret \
        "$script_dir/postgres-permissions.sh"
}

fake_output="$temp_dir/fake-success.out"
run_fake >"$fake_output" 2>&1
grep -Fx 'call-1' "$temp_dir/fake-calls" >/dev/null
grep -Fx 'call-2' "$temp_dir/fake-calls" >/dev/null
grep -Fx 'call-3' "$temp_dir/fake-calls" >/dev/null
if grep -F -- "$app_password" "$fake_output" "$temp_dir/fake-calls" >/dev/null; then
    printf 'application password appeared in fake-client output or audit log\n' >&2
    exit 1
fi

: >"$temp_dir/fake-calls"
fake_failure="$temp_dir/fake-failure.out"
if FAKE_FAIL_PASSWORD=1 run_fake >"$fake_failure" 2>&1; then
    printf 'password protocol failure was accepted\n' >&2
    exit 1
fi
[[ $(wc -l <"$temp_dir/fake-calls") -eq 2 ]]
if grep -F -- "$app_password" "$fake_failure" "$temp_dir/fake-calls" >/dev/null; then
    printf 'application password appeared on the fake failure path\n' >&2
    exit 1
fi

: >"$temp_dir/fake-calls"
multiline_output="$temp_dir/multiline.out"
if TEST_APP_PASSWORD=$'first\nsecond' run_fake \
    >"$multiline_output" 2>&1; then
    printf 'multiline application password was accepted\n' >&2
    exit 1
fi
[[ ! -s "$temp_dir/fake-calls" ]]

if [[ "${EMFONT_SKIP_DOCKER_TESTS:-0}" == 1 ]]; then
    printf 'postgres-permissions protocol checks passed (Docker integration skipped)\n'
    exit 0
fi
command -v docker >/dev/null 2>&1 || {
    printf 'docker is required; set EMFONT_SKIP_DOCKER_TESTS=1 to run protocol checks only\n' >&2
    exit 1
}
docker image inspect "$postgres_image" >/dev/null 2>&1 || {
    printf 'PostgreSQL test image is unavailable: %s\n' "$postgres_image" >&2
    exit 1
}

docker run -d --name "$postgres_container" \
    -e POSTGRES_USER=emfont_admin \
    -e POSTGRES_PASSWORD=admin-secret \
    -e POSTGRES_DB=emfont \
    "$postgres_image" \
    -c log_destination=stderr \
    -c logging_collector=off \
    -c log_statement=all \
    -c log_duration=on \
    -c log_min_duration_statement=0 \
    -c log_min_error_statement=error \
    -c log_parameter_max_length=1024 \
    -c log_parameter_max_length_on_error=-1 >/dev/null

for _ in {1..60}; do
    if docker exec "$postgres_container" pg_isready \
        --username=emfont_admin --dbname=emfont >/dev/null 2>&1; then
        break
    fi
    sleep 1
done
docker exec "$postgres_container" pg_isready \
    --username=emfont_admin --dbname=emfont >/dev/null

docker exec -i -e PGPASSWORD=admin-secret "$postgres_container" \
    psql --username=emfont_admin --dbname=emfont --no-psqlrc \
    --set ON_ERROR_STOP=1 >/dev/null <<'SQL'
CREATE TABLE font_family (id bigint);
CREATE TABLE font_sources (id bigint);
CREATE TABLE version (id bigint);
CREATE TABLE static_fonts (id bigint);
CREATE TABLE system_metadata (id bigint);
CREATE TABLE font_artifacts (id bigint);
CREATE TABLE font_build_jobs (id bigint);
CREATE TABLE font_artifact_quota (singleton boolean);
CREATE SEQUENCE font_build_jobs_id_seq;
CREATE SEQUENCE font_artifact_fence_seq;
SQL

run_real() {
    docker run --rm \
        --network "container:$postgres_container" \
        --entrypoint /bin/sh \
        --volume "$script_dir/postgres-permissions.sh:/test/postgres-permissions.sh:ro" \
        -e EMFONT_POSTGRES_HOST=127.0.0.1 \
        -e EMFONT_POSTGRES_ADMIN_USER=emfont_admin \
        -e EMFONT_POSTGRES_APP_USER=emfont_app \
        -e EMFONT_POSTGRES_DB=emfont \
        -e EMFONT_POSTGRES_APP_PASSWORD="$app_password" \
        -e PGPASSWORD=admin-secret \
        "$postgres_image" /test/postgres-permissions.sh
}

real_failure="$temp_dir/real-failure.out"
if run_real >"$real_failure" 2>&1; then
    printf 'missing-table reconciliation unexpectedly succeeded\n' >&2
    exit 1
fi
if grep -F -- "$app_password" "$real_failure" >/dev/null; then
    printf 'application password appeared in real-client error output\n' >&2
    exit 1
fi
[[ $(docker exec -e PGPASSWORD=admin-secret "$postgres_container" \
    psql --username=emfont_admin --dbname=emfont --no-psqlrc --tuples-only \
    --no-align --command="SELECT rolcanlogin FROM pg_authid WHERE rolname = 'emfont_app'") == f ]]

docker exec -i -e PGPASSWORD=admin-secret "$postgres_container" \
    psql --username=emfont_admin --dbname=emfont --no-psqlrc \
    --set ON_ERROR_STOP=1 --command='CREATE TABLE font_terminal_failures (id bigint)' \
    >/dev/null
real_success="$temp_dir/real-success.out"
run_real >"$real_success" 2>&1
if grep -F -- "$app_password" "$real_success" >/dev/null; then
    printf 'application password appeared in real-client success output\n' >&2
    exit 1
fi

role_state=$(docker exec -e PGPASSWORD=admin-secret "$postgres_container" \
    psql --username=emfont_admin --dbname=emfont --no-psqlrc --tuples-only \
    --no-align --command="
        SELECT concat_ws(':', rolcanlogin, rolsuper, rolcreatedb, rolcreaterole,
                         rolreplication, rolbypassrls,
                         rolpassword LIKE 'SCRAM-SHA-256\$%')
        FROM pg_authid WHERE rolname = 'emfont_app'
    ")
[[ "$role_state" == 't:f:f:f:f:f:t' ]]

docker run --rm --network "container:$postgres_container" \
    --entrypoint /bin/sh \
    -e PGPASSWORD="$app_password" \
    "$postgres_image" -ec \
    'psql --host=127.0.0.1 --username=emfont_app --dbname=emfont --no-psqlrc --tuples-only --no-align --command="SELECT 1"' \
    | grep -Fx 1 >/dev/null

docker logs "$postgres_container" >"$temp_dir/postgres.log" 2>&1
if grep -F -- "$app_password" "$temp_dir/postgres.log" >/dev/null; then
    printf 'application password appeared in PostgreSQL logs\n' >&2
    exit 1
fi
# The dollar sign is literal SCRAM verifier syntax, not shell interpolation.
# shellcheck disable=SC2016
if grep -E 'SCRAM-SHA-256\$4096:' "$temp_dir/postgres.log" >/dev/null; then
    printf 'SCRAM verifier appeared in PostgreSQL logs\n' >&2
    exit 1
fi

printf 'postgres-permissions password protocol and log-safety checks passed\n'
