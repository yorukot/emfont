#!/bin/sh
set -eu

fail() {
    printf 'minio-entrypoint: %s\n' "$*" >&2
    exit 1
}

load_secret() {
    name=$1
    file_name=${name}_FILE

    eval "value=\${${name}:-}"
    eval "file=\${${file_name}:-}"

    if [ -n "$value" ] && [ -n "$file" ]; then
        fail "$name and $file_name are mutually exclusive"
    fi

    if [ -n "$file" ]; then
        [ -f "$file" ] || fail "$file_name does not name a regular file"
        [ -r "$file" ] || fail "$file_name is not readable"
        [ "$(stat -c '%u' "$file")" = 0 ] || fail "$file_name must be owned by root"
        case "$(stat -c '%a' "$file")" in
            400 | 600) ;;
            *) fail "$file_name must have mode 0400 or 0600" ;;
        esac

        value=$(cat "$file")
        [ -n "$value" ] || fail "$file_name points to an empty secret"
        [ "$(printf '%s' "$value" | wc -l)" -eq 0 ] || fail "$file_name must contain exactly one value"

        export "$name=$value"
        unset "$file_name"
    fi
}

umask 077
load_secret MINIO_ROOT_USER
load_secret MINIO_ROOT_PASSWORD

run_as_uid=${MINIO_RUN_AS_UID:-10001}
run_as_gid=${MINIO_RUN_AS_GID:-$run_as_uid}
case "$run_as_uid:$run_as_gid" in
    *[!0-9:]*) fail "MINIO_RUN_AS_UID and MINIO_RUN_AS_GID must be numeric" ;;
esac

if [ "$#" -eq 0 ]; then
    set -- minio
elif [ "$1" != "minio" ]; then
    set -- minio "$@"
fi

if [ "$(id -u)" -eq 0 ]; then
    command -v setpriv >/dev/null 2>&1 || fail "setpriv is required to drop privileges"
    if ! setpriv --reuid "$run_as_uid" --regid "$run_as_gid" --clear-groups \
        /bin/sh -ec 'test -w /data'
    then
        fail "/data is not writable by UID/GID $run_as_uid:$run_as_gid"
    fi
    exec setpriv --reuid "$run_as_uid" --regid "$run_as_gid" --clear-groups -- "$@"
fi

[ "$(id -u):$(id -g)" = "$run_as_uid:$run_as_gid" ] || \
    fail "runtime identity does not match UID/GID $run_as_uid:$run_as_gid"
exec "$@"
