#!/bin/sh
set -eu
umask 077

fail() {
    printf 'load-secrets: %s\n' "$*" >&2
    exit 1
}

stage_host_secrets() {
    [ -d /run/host-secrets ] || return 0

    for source in /run/host-secrets/*; do
        [ -e "$source" ] || [ -L "$source" ] || continue
        if [ -L "$source" ] || readlink "$source" >/dev/null 2>&1; then
            fail "host secret source must not be a symbolic link: $source"
        fi

        source_identity=$(stat -c '%F|%d:%i|%u:%g:%a|%s|%y|%z' -- "$source") || \
            fail "could not read host secret source metadata: $source"
        case "$source_identity" in
            'regular file|'*'|0:0:600|'*) ;;
            *) fail "host secret source must be a regular root:root file with mode 0600 before staging: $source" ;;
        esac

        target="/run/secrets/${source##*/}"
        rm -f "$target" || fail "could not clear staged secret target: $target"
        cp --no-dereference -- "$source" "$target" || fail "could not stage host secret source: $source"
        source_identity_after=$(stat -c '%F|%d:%i|%u:%g:%a|%s|%y|%z' -- "$source") || \
            fail "host secret source changed while staging: $source"
        [ "$source_identity_after" = "$source_identity" ] || \
            fail "host secret source changed while staging: $source"
        if [ -L "$target" ] || readlink "$target" >/dev/null 2>&1; then
            fail "staged secret must not be a symbolic link: $target"
        fi
        chmod 0400 "$target" || fail "could not set staged secret mode: $target"
        target_metadata=$(stat -c '%F|%u:%g:%a' -- "$target") || \
            fail "could not read staged secret metadata: $target"
        [ "$target_metadata" = 'regular file|0:0:400' ] || \
            fail "staged secret must be a regular root:root file with mode 0400: $target"
    done
}

load_secret() {
    name=$1
    file_name="${name}_FILE"
    eval "file=\${${file_name}:-}"
    [ -n "$file" ] || return 0

    eval "direct=\${${name}:-}"
    [ -z "$direct" ] || fail "$name and $file_name cannot both be set"
    if [ -L "$file" ] || readlink "$file" >/dev/null 2>&1; then
        fail "$file_name must not name a symbolic link: $file"
    fi
    file_identity=$(stat -c '%F|%d:%i|%u:%g:%a|%s|%y|%z' -- "$file") || \
        fail "$file_name does not name a regular file: $file"
    case "$file_identity" in
        'regular file|'*) ;;
        *) fail "$file_name does not name a regular file: $file" ;;
    esac
    [ -r "$file" ] || fail "$file_name is not readable: $file"

    if [ -n "${EMFONT_RUN_AS_UID:-}" ]; then
        metadata=$(stat -c '%u:%g:%a' -- "$file") || fail "could not read $file_name ownership and mode"
        case "$metadata" in
            0:0:400 | 0:0:600) ;;
            *) fail "$file_name must be root:root with mode 0400 or 0600 before privilege drop" ;;
        esac
    fi

    value=$(cat -- "$file") || fail "could not read $file_name"
    file_identity_after=$(stat -c '%F|%d:%i|%u:%g:%a|%s|%y|%z' -- "$file") || \
        fail "$file_name changed while it was read"
    [ "$file_identity_after" = "$file_identity" ] || \
        fail "$file_name changed while it was read"
    [ -n "$value" ] || fail "$file_name is empty"
    line_count=$(printf '%s' "$value" | wc -l)
    [ "$line_count" -eq 0 ] || fail "$file_name must contain exactly one value"

    export "$name=$value"
    unset "$file_name"
}

stage_host_secrets

for secret_name in \
    PGPASSWORD \
    EMFONT_POSTGRES_APP_PASSWORD \
    EMFONT_MINIO_ACCESS_KEY \
    EMFONT_MINIO_SECRET_KEY \
    EMFONT_MINIO_CLEANUP_ACCESS_KEY \
    EMFONT_MINIO_CLEANUP_SECRET_KEY \
    EMFONT_MINIO_SESSION_TOKEN \
    EMFONT_METRICS_BEARER_TOKEN \
    MINIO_ROOT_USER \
    MINIO_ROOT_PASSWORD
do
    load_secret "$secret_name"
done

[ "$#" -gt 0 ] || fail "no command was supplied"

if [ -n "${EMFONT_RUN_AS_UID:-}" ]; then
    run_as_gid=${EMFONT_RUN_AS_GID:-$EMFONT_RUN_AS_UID}
    case "$EMFONT_RUN_AS_UID:$run_as_gid" in
        *[!0-9:]*) fail "EMFONT_RUN_AS_UID and EMFONT_RUN_AS_GID must be numeric" ;;
    esac
    command -v setpriv >/dev/null 2>&1 || fail "setpriv is required to drop privileges"
    exec setpriv \
        --reuid "$EMFONT_RUN_AS_UID" \
        --regid "$run_as_gid" \
        --clear-groups \
        -- "$@"
fi

exec "$@"
