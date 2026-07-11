#!/usr/bin/env bash
set -Eeuo pipefail

image="${EMFONT_TEST_BACKEND_IMAGE:?Set EMFONT_TEST_BACKEND_IMAGE to an exact backend image reference}"
[[ "$image" =~ @sha256:[0-9a-f]{64}$ ]] || {
    printf 'EMFONT_TEST_BACKEND_IMAGE must use an exact sha256 digest\n' >&2
    exit 2
}
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
temp_dir="$(mktemp -d /tmp/emfont-load-secrets-test.XXXXXX)"
trap 'rm -rf "$temp_dir"' EXIT

secret_file="$temp_dir/postgres-password"
printf '%s' 'staged-secret-value' >"$secret_file"
chmod 0600 "$secret_file"

run_loader() {
    docker run --rm \
        --user 0:0 \
        --read-only \
        --security-opt no-new-privileges:true \
        --cap-drop ALL \
        --cap-add SETGID \
        --cap-add SETUID \
        --mount "type=bind,source=$temp_dir,target=/run/host-secrets,readonly" \
        --mount "type=bind,source=$script_dir/load-secrets.sh,target=/test/load-secrets.sh,readonly" \
        --tmpfs /run/secrets:rw,nosuid,nodev,noexec,mode=0700,uid=0,gid=0 \
        --env PGPASSWORD_FILE=/run/secrets/postgres-password \
        --env EMFONT_RUN_AS_UID=10001 \
        --env EMFONT_RUN_AS_GID=10001 \
        --entrypoint /test/load-secrets.sh \
        "$image" \
        /bin/sh -ec '
            test "$(id -u):$(id -g)" = 10001:10001
            test "$PGPASSWORD" = staged-secret-value
            test -z "${PGPASSWORD_FILE+x}"
        '
}

run_loader

chgrp 1 "$secret_file"
if output="$(run_loader 2>&1)"; then
    printf 'load-secrets accepted a non-root host secret group\n' >&2
    exit 1
fi
grep -F 'must be a regular root:root file with mode 0600 before staging' <<<"$output" >/dev/null

chgrp 0 "$secret_file"
chmod 0644 "$secret_file"
if output="$(run_loader 2>&1)"; then
    printf 'load-secrets accepted a 0644 host secret source\n' >&2
    exit 1
fi
grep -F 'must be a regular root:root file with mode 0600 before staging' <<<"$output" >/dev/null

chmod 0600 "$secret_file"
ln -s postgres-password "$temp_dir/symlink-secret"
if output="$(run_loader 2>&1)"; then
    printf 'load-secrets accepted a symbolic-link host secret source\n' >&2
    exit 1
fi
grep -F 'host secret source must not be a symbolic link' <<<"$output" >/dev/null

printf 'load-secrets staging checks passed\n'
