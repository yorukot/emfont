#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly repo_dir
readonly installer="$repo_dir/backend/scripts/workflow-setup-buildx.sh"

contract="$(bash "$installer" --print-contract)"
readonly contract
readonly expected=$'buildx_repository=https://github.com/docker/buildx\nbuildx_version=v0.35.0\nbuildx_linux_amd64_sha256=d41ece72044243b4f58b343441ae37446d9c29a7d6b5e11c61847bbcf8f7dfda\nbuildx_linux_arm64_sha256=c4248d6cbc4a619a7e0b4609c11e509ad4ac0b475e1c64817c0ac20c5d90c766\nbuildkit_image=moby/buildkit:v0.31.0@sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1'
[[ "$contract" == "$expected" ]]

for checksum in \
    d41ece72044243b4f58b343441ae37446d9c29a7d6b5e11c61847bbcf8f7dfda \
    c4248d6cbc4a619a7e0b4609c11e509ad4ac0b475e1c64817c0ac20c5d90c766
do
    [[ "$checksum" =~ ^[0-9a-f]{64}$ ]]
done
[[ "$contract" != *latest* ]]

if bash "$installer" 'invalid builder name' >/dev/null 2>&1; then
    printf 'Buildx installer accepted an invalid builder name\n' >&2
    exit 1
fi

printf 'Workflow Buildx authentication contract checks passed\n'
