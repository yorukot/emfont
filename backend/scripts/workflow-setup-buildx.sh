#!/usr/bin/env bash
set -Eeuo pipefail

readonly buildx_version='v0.35.0'
readonly buildx_amd64_sha256='d41ece72044243b4f58b343441ae37446d9c29a7d6b5e11c61847bbcf8f7dfda'
readonly buildx_arm64_sha256='c4248d6cbc4a619a7e0b4609c11e509ad4ac0b475e1c64817c0ac20c5d90c766'
readonly buildx_repository='https://github.com/docker/buildx'
readonly buildkit_image='moby/buildkit:v0.31.0@sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1'

print_contract() {
    printf 'buildx_repository=%s\n' "$buildx_repository"
    printf 'buildx_version=%s\n' "$buildx_version"
    printf 'buildx_linux_amd64_sha256=%s\n' "$buildx_amd64_sha256"
    printf 'buildx_linux_arm64_sha256=%s\n' "$buildx_arm64_sha256"
    printf 'buildkit_image=%s\n' "$buildkit_image"
}

if [[ "${1:-}" == '--print-contract' ]]; then
    (($# == 1))
    print_contract
    exit 0
fi
if (($# != 1)); then
    printf 'usage: %s BUILDER_NAME\n' "${0##*/}" >&2
    exit 2
fi

readonly builder_name="$1"
[[ "$builder_name" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$ ]] || {
    printf 'invalid Buildx builder name: %s\n' "$builder_name" >&2
    exit 2
}

case "$(uname -m)" in
    x86_64)
        readonly architecture=amd64
        readonly expected_sha256="$buildx_amd64_sha256"
        ;;
    aarch64 | arm64)
        readonly architecture=arm64
        readonly expected_sha256="$buildx_arm64_sha256"
        ;;
    *)
        printf 'unsupported Buildx host architecture: %s\n' "$(uname -m)" >&2
        exit 2
        ;;
esac

readonly plugin_dir="${DOCKER_CONFIG:-$HOME/.docker}/cli-plugins"
readonly plugin="$plugin_dir/docker-buildx"
temporary_dir="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/emfont-buildx.XXXXXX")"
cleanup() {
    rm -rf "$temporary_dir"
}
trap cleanup EXIT HUP INT TERM

readonly downloaded="$temporary_dir/docker-buildx"
readonly download_url="$buildx_repository/releases/download/$buildx_version/buildx-$buildx_version.linux-$architecture"
curl --fail-with-body --silent --show-error --location \
    --proto '=https' \
    --proto-redir '=https' \
    --tlsv1.2 \
    --retry 3 \
    --max-filesize 134217728 \
    --output "$downloaded" \
    "$download_url"
printf '%s  %s\n' "$expected_sha256" "$downloaded" | \
    sha256sum --check --strict

install -d -m 0700 "$plugin_dir"
install -m 0755 "$downloaded" "$plugin"
[[ "$(sha256sum "$plugin" | awk '{print $1}')" == "$expected_sha256" ]]

direct_version="$($plugin version)"
docker_version="$(docker buildx version)"
[[ "$direct_version" == "$docker_version" ]]
[[ "$direct_version" == "github.com/docker/buildx $buildx_version "* ]]

docker buildx create \
    --name "$builder_name" \
    --driver docker-container \
    --driver-opt "image=$buildkit_image" \
    --use >/dev/null
docker buildx inspect --builder "$builder_name" --bootstrap >/dev/null

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
        printf 'name=%s\n' "$builder_name"
        printf 'version=%s\n' "$buildx_version"
        printf 'binary_sha256=sha256:%s\n' "$expected_sha256"
        printf 'architecture=linux/%s\n' "$architecture"
        printf 'buildkit_image=%s\n' "$buildkit_image"
    } >>"$GITHUB_OUTPUT"
fi

print_contract
printf 'buildx_binary_sha256=sha256:%s\n' "$expected_sha256"
printf 'buildx_host_platform=linux/%s\n' "$architecture"
printf 'buildx_builder=%s\n' "$builder_name"
