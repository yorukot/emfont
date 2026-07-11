#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly repo_dir
readonly contract="$repo_dir/backend/scripts/workflow-compose-release.sh"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/emfont-compose-release.XXXXXX")"
cleanup() {
    rm -rf "$temporary_dir"
}
trap cleanup EXIT HUP INT TERM

readonly digest_a='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
readonly digest_b='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
readonly digest_c='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
readonly digest_d='dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
cat >"$temporary_dir/images.env" <<EOF
backend=ghcr.io/yorukot/emfont-backend@sha256:$digest_a
postgres=ghcr.io/yorukot/emfont-postgres@sha256:$digest_b
minio=ghcr.io/yorukot/emfont-minio@sha256:$digest_c
minio_mc=ghcr.io/yorukot/emfont-minio-mc@sha256:$digest_d
EOF

# Ambient deployment values must never override the signed release contract.
export COMPOSE_PROJECT_NAME=ambient-project
export EMFONT_BACKEND_IMAGE_REPOSITORY=ghcr.io/attacker/ambient
export EMFONT_MINIO_PUBLIC_BASE_URL=https://ambient.invalid/fonts

bash "$contract" create \
    "$temporary_dir/release" \
    "$repo_dir/docker-compose.backend.yml" \
    "$temporary_dir/images.env" >"$temporary_dir/create.env"
bash "$contract" verify \
    "$temporary_dir/release" \
    "$temporary_dir/images.env" >"$temporary_dir/verify.env"
cmp --silent "$temporary_dir/create.env" "$temporary_dir/verify.env"
[[ "$(find "$temporary_dir/release" -mindepth 1 -maxdepth 1 \
    -type f -printf '%f\n' | LC_ALL=C sort)" == \
    $'compose-config.json\ncompose-contract.env\ndocker-compose.backend.yml' ]]

cp -a "$temporary_dir/release" "$temporary_dir/mutated-release"
sed -i '0,/read_only: true/s//read_only: false/' \
    "$temporary_dir/mutated-release/docker-compose.backend.yml"
if bash "$contract" verify \
    "$temporary_dir/mutated-release" \
    "$temporary_dir/images.env" >/dev/null 2>&1
then
    printf 'Compose release verifier accepted a modified Compose file\n' >&2
    exit 1
fi

sed 's/@sha256:[0-9a-f]\{64\}/:mutable/' "$temporary_dir/images.env" \
    >"$temporary_dir/mutable-images.env"
if bash "$contract" create \
    "$temporary_dir/mutable-release" \
    "$repo_dir/docker-compose.backend.yml" \
    "$temporary_dir/mutable-images.env" >/dev/null 2>&1
then
    printf 'Compose release contract accepted a mutable image reference\n' >&2
    exit 1
fi

printf 'Workflow Compose release contract checks passed\n'
