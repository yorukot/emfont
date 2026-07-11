#!/bin/sh
set -eu
umask 077

fail() {
    printf 'postgres-permissions: %s\n' "$*" >&2
    exit 1
}

for name in \
    EMFONT_POSTGRES_ADMIN_USER \
    EMFONT_POSTGRES_APP_USER \
    EMFONT_POSTGRES_DB \
    EMFONT_POSTGRES_APP_PASSWORD \
    PGPASSWORD
do
    eval "value=\${${name}:-}"
    [ -n "$value" ] || fail "$name is required"
done

for identifier in \
    "$EMFONT_POSTGRES_ADMIN_USER" \
    "$EMFONT_POSTGRES_APP_USER" \
    "$EMFONT_POSTGRES_DB"
do
    case "$identifier" in
        [0-9]* | *[!a-z0-9_]*)
            fail "database identifiers must use lowercase letters, digits, and underscores"
            ;;
    esac
    [ "${#identifier}" -le 63 ] || fail "database identifiers must not exceed 63 characters"
done

[ "$EMFONT_POSTGRES_ADMIN_USER" != "$EMFONT_POSTGRES_APP_USER" ] || \
    fail "the application role must differ from the PostgreSQL admin role"

app_password=$EMFONT_POSTGRES_APP_PASSWORD
unset EMFONT_POSTGRES_APP_PASSWORD
[ "$(printf '%s' "$app_password" | wc -l)" -eq 0 ] || \
    fail "EMFONT_POSTGRES_APP_PASSWORD must contain exactly one value"
carriage_return=$(printf '\r')
case "$app_password" in
    *"$carriage_return"*)
        fail "EMFONT_POSTGRES_APP_PASSWORD must not contain a carriage return"
        ;;
esac

export PGCONNECT_TIMEOUT="${PGCONNECT_TIMEOUT:-10}"
# The password protocol below computes the verifier in psql. These settings are
# defense in depth against logging the verifier; the raw password is never part
# of a server statement or a psql variable.
export PGOPTIONS='-c password_encryption=scram-sha-256 -c log_statement=none -c log_duration=off -c log_min_duration_statement=-1 -c log_min_duration_sample=-1 -c log_statement_sample_rate=0 -c log_transaction_sample_rate=0 -c log_min_error_statement=panic -c log_parameter_max_length=0 -c log_parameter_max_length_on_error=0 -c pgaudit.log=none -c pgaudit.log_parameter=off'

psql \
    --host "${EMFONT_POSTGRES_HOST:-postgres}" \
    --username "$EMFONT_POSTGRES_ADMIN_USER" \
    --dbname "$EMFONT_POSTGRES_DB" \
    --no-psqlrc \
    --set ON_ERROR_STOP=1 <<'SQL'
\set ON_ERROR_STOP on
\getenv app_user EMFONT_POSTGRES_APP_USER

BEGIN;
SELECT format('CREATE ROLE %I NOLOGIN', :'app_user')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'app_user')
\gexec
SELECT format(
    'ALTER ROLE %I WITH NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS',
    :'app_user'
)
\gexec
COMMIT;
SQL

# \password reads two prompt responses from stdin and sends only a client-side
# SCRAM verifier to PostgreSQL. The validated role name cannot introduce a psql
# command, and the password environment variable was removed before psql starts.
psql \
    --host "${EMFONT_POSTGRES_HOST:-postgres}" \
    --username "$EMFONT_POSTGRES_ADMIN_USER" \
    --dbname "$EMFONT_POSTGRES_DB" \
    --no-psqlrc \
    --set ON_ERROR_STOP=1 <<EOF
\password $EMFONT_POSTGRES_APP_USER
$app_password
$app_password
EOF
unset app_password

psql \
    --host "${EMFONT_POSTGRES_HOST:-postgres}" \
    --username "$EMFONT_POSTGRES_ADMIN_USER" \
    --dbname "$EMFONT_POSTGRES_DB" \
    --no-psqlrc \
    --set ON_ERROR_STOP=1 <<'SQL'
\set ON_ERROR_STOP on
\getenv app_user EMFONT_POSTGRES_APP_USER

BEGIN;
SELECT COALESCE(bool_and(rolpassword LIKE 'SCRAM-SHA-256$%'), false)
    AS password_is_scram
FROM pg_authid
WHERE rolname = :'app_user'
\gset
\if :password_is_scram
\else
    \echo 'application role does not have a SCRAM password verifier'
    \quit 1
\endif

SELECT format(
    'ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS',
    :'app_user'
)
\gexec

SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), :'app_user')
\gexec
SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database())
\gexec
SELECT format('REVOKE TEMPORARY ON DATABASE %I FROM %I', current_database(), :'app_user')
\gexec
SELECT format('GRANT USAGE ON SCHEMA public TO %I', :'app_user')
\gexec
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SELECT format('REVOKE CREATE ON SCHEMA public FROM %I', :'app_user')
\gexec
SELECT format(
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM %I',
    :'app_user'
)
\gexec
SELECT format(
    'REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM %I',
    :'app_user'
)
\gexec
SELECT format(
    'GRANT SELECT ON TABLE public.font_family, public.font_sources, public.version, public.static_fonts, public.system_metadata TO %I',
    :'app_user'
)
\gexec
SELECT format(
    'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.font_artifacts TO %I',
    :'app_user'
)
\gexec
SELECT format(
    'GRANT SELECT, INSERT, UPDATE ON TABLE public.font_build_jobs TO %I',
    :'app_user'
)
\gexec
SELECT format(
    'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.font_terminal_failures TO %I',
    :'app_user'
)
\gexec
SELECT format(
    'GRANT SELECT ON TABLE public.font_artifact_quota TO %I',
    :'app_user'
)
\gexec
SELECT format(
    'GRANT UPDATE (singleton) ON TABLE public.font_artifact_quota TO %I',
    :'app_user'
)
\gexec
SELECT format(
    'GRANT USAGE, SELECT ON SEQUENCE public.font_build_jobs_id_seq, public.font_artifact_fence_seq TO %I',
    :'app_user'
)
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON TABLE public.goose_db_version FROM %I', :'app_user')
WHERE to_regclass('public.goose_db_version') IS NOT NULL
\gexec
COMMIT;
SQL

printf 'PostgreSQL application role %s is ready.\n' "$EMFONT_POSTGRES_APP_USER"
