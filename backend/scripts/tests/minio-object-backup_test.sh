#!/bin/bash
set -Eeuo pipefail
umask 077

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
temp_dir=$(mktemp -d /tmp/emfont-minio-object-backup-test.XXXXXX)
suffix=$$
network="emfont-object-backup-test-$suffix"
source_container="emfont-object-source-test-$suffix"
target_container="emfont-object-target-test-$suffix"
extract_container="emfont-object-mc-extract-$suffix"

cleanup() {
    docker rm -f "$source_container" "$target_container" \
        "$extract_container" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
    rm -rf "$temp_dir"
}
report_error() {
    local exit_code=$?
    trap - ERR
    if [[ "${GITHUB_ACTIONS:-}" == true ]]; then
        printf '::error title=MinIO backup and restore contract::line=%s status=%s\n' \
            "${BASH_LINENO[0]}" "$exit_code"
    fi
    printf 'minio object backup test failed at line %s\n' \
        "${BASH_LINENO[0]}" >&2
    exit "$exit_code"
}
trap cleanup EXIT HUP INT TERM
trap report_error ERR

command -v docker >/dev/null 2>&1 || {
    printf 'docker is required for the MinIO object backup integration test\n' >&2
    exit 1
}
command -v jq >/dev/null 2>&1 || {
    printf 'jq is required for the MinIO object backup integration test\n' >&2
    exit 1
}

server_image=${EMFONT_MINIO_TEST_IMAGE:-minio/minio:RELEASE.2025-04-22T22-12-26Z}
client_image=${EMFONT_MINIO_MC_TEST_IMAGE:-emfont-minio-mc:final}
docker image inspect "$server_image" >/dev/null
docker image inspect "$client_image" >/dev/null
docker network create "$network" >/dev/null

docker run -d --name "$source_container" --network "$network" \
    -e MINIO_ROOT_USER=backup-root \
    -e MINIO_ROOT_PASSWORD=backup-password \
    "$server_image" server /data >/dev/null
docker run -d --name "$target_container" --network "$network" \
    -e MINIO_ROOT_USER=restore-root \
    -e MINIO_ROOT_PASSWORD=restore-password \
    "$server_image" server /data >/dev/null

docker create --name "$extract_container" --entrypoint /bin/true \
    "$client_image" >/dev/null
docker cp "$extract_container:/usr/local/bin/mc" "$temp_dir/mc"
docker rm "$extract_container" >/dev/null
if [[ "$(id -u)" -ne 0 ]]; then
    command -v sudo >/dev/null 2>&1 || {
        printf 'sudo is required to stage the extracted MinIO client\n' >&2
        exit 2
    }
    sudo chown "$(id -u):$(id -g)" "$temp_dir/mc"
fi
chmod 0500 "$temp_dir/mc"
mkdir -m 0700 "$temp_dir/mc-config"

source_ip=$(docker inspect "$source_container" \
    --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
target_ip=$(docker inspect "$target_container" \
    --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
[[ -n "$source_ip" && -n "$target_ip" ]]

alias_with_retry() {
    local alias_name=$1 endpoint=$2 access_key=$3 secret_key=$4
    for _ in {1..60}; do
        if printf '%s\n%s\n' "$access_key" "$secret_key" | \
            MC_CONFIG_DIR="$temp_dir/mc-config" "$temp_dir/mc" alias set \
                "$alias_name" "$endpoint" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

alias_with_retry source "http://$source_ip:9000" backup-root backup-password
alias_with_retry target "http://$target_ip:9000" restore-root restore-password

mc() {
    MC_CONFIG_DIR="$temp_dir/mc-config" "$temp_dir/mc" "$@"
}
mc mb source/fonts >/dev/null
mc version enable source/fonts >/dev/null
mc mb target/fonts >/dev/null
mc version enable target/fonts >/dev/null

printf 'tagged font payload\n' >"$temp_dir/tagged-font"
printf 'untagged font payload\n' >"$temp_dir/untagged-font"
mc cp --checksum SHA256 \
    --storage-class REDUCED_REDUNDANCY \
    --attr 'Content-Type='\''font/ttf'\'';Cache-Control='\''public, max-age=3600'\'';Content-Disposition='\''attachment; filename="source font.ttf"'\'';Content-Encoding='\''identity'\'';Content-Language='\''en'\'';X-Amz-Meta-Owner='\''font-team'\''' \
    --tags 'family=Source%20Font&stage=production' \
    "$temp_dir/tagged-font" 'source/fonts/source/object name.ttf' \
    >/dev/null
mc cp --checksum SHA256 \
    --attr 'Content-Type='\''font/woff2'\'';X-Amz-Meta-Owner='\''font-team'\''' \
    "$temp_dir/untagged-font" source/fonts/generated/font.woff2 >/dev/null

export_output="$temp_dir/export.out"
MC_BIN="$temp_dir/mc" \
MC_CONFIG_DIR="$temp_dir/mc-config" \
EMFONT_OBJECT_WRITERS_QUIESCED=confirmed \
    "$script_dir/minio-object-export.sh" source/fonts "$temp_dir/export" \
    >"$export_output" 2>&1
grep -Fx 'Exported 2 current object versions from bucket fonts.' \
    "$export_output" >/dev/null
if grep -F 'source/object name.ttf' "$export_output" >/dev/null; then
    printf 'object key appeared in export output\n' >&2
    exit 1
fi

jq -e '
    .format == "emfont-object-export/v1" and
    .version_scope == "current-only-pinned-version-ids" and
    .object_count == 2 and .total_size_bytes > 0 and
    .producer.mc_commit == "7394ce0dd2a80935aded936b09fa12cbb3cb8096"
' "$temp_dir/export/export-manifest.json" >/dev/null
jq -s -e '
    length == 2 and
    any(.[];
        .key == "source/object name.ttf" and
        .metadata["Content-Type"] == "font/ttf" and
        .metadata["Content-Disposition"] ==
            "attachment; filename=\"source font.ttf\"" and
        .metadata["X-Amz-Meta-Owner"] == "font-team" and
        .metadata["X-Amz-Storage-Class"] == "REDUCED_REDUNDANCY" and
        .tags == {family: "Source Font", stage: "production"} and
        (.source.version_id | length > 0) and
        (.checksum.SHA256 | test("^[A-Za-z0-9+/]{43}=$"))) and
    any(.[];
        .key == "generated/font.woff2" and .tags == {} and
        (.metadata | has("X-Amz-Tagging-Count") | not))
' "$temp_dir/export/object-manifest.ndjson" >/dev/null

export EMFONT_MINIO_HELPER_NAME=minio-object-backup-test
export JQ_BIN=jq
# The test sources the reviewed helper selected relative to its own path.
# shellcheck disable=SC1091
source "$script_dir/minio-object-manifest-lib.sh"
first_entry=$(head -n 1 "$temp_dir/export/object-manifest.ndjson")
for quote_case in apostrophe double-quote; do
    quote_entry="$temp_dir/$quote_case-entry.json"
    case "$quote_case" in
        apostrophe) quote_value="owner's font" ;;
        double-quote) quote_value='owner said "font"' ;;
    esac
    jq -cS --arg value "$quote_value" \
        '.metadata["X-Amz-Meta-Quote-Test"] = $value' \
        <<<"$first_entry" >"$quote_entry"
    minio_validate_object_entry "$quote_entry"
done
mixed_quote_entry="$temp_dir/mixed-quote-entry.json"
jq -cS --arg value 'owner said "it'\''s a font"' \
    '.metadata["X-Amz-Meta-Quote-Test"] = $value' \
    <<<"$first_entry" >"$mixed_quote_entry"
if mixed_quote_output="$(
    minio_validate_object_entry "$mixed_quote_entry" 2>&1
)"; then
    printf 'manifest validation accepted unrestorable mixed-quote metadata\n' >&2
    exit 1
fi
grep -F 'metadata value cannot be represented by the reviewed mc client' \
    <<<"$mixed_quote_output" >/dev/null

cp -a "$temp_dir/export" "$temp_dir/unsupported-export"
jq -cS '.metadata.Expires = "Tue, 14 Jul 2026 12:00:00 GMT"' \
    "$temp_dir/unsupported-export/object-manifest.ndjson" \
    >"$temp_dir/unsupported-export/object-manifest.new"
mv "$temp_dir/unsupported-export/object-manifest.new" \
    "$temp_dir/unsupported-export/object-manifest.ndjson"
unsupported_sha=$(sha256sum \
    "$temp_dir/unsupported-export/object-manifest.ndjson" | awk '{print $1}')
jq -S --arg sha "$unsupported_sha" '.object_manifest_sha256 = $sha' \
    "$temp_dir/unsupported-export/export-manifest.json" \
    >"$temp_dir/unsupported-export/export-manifest.new"
mv "$temp_dir/unsupported-export/export-manifest.new" \
    "$temp_dir/unsupported-export/export-manifest.json"
unsupported_output="$temp_dir/unsupported.out"
if MC_BIN="$temp_dir/mc" \
    MC_CONFIG_DIR="$temp_dir/mc-config" \
    EMFONT_RESTORE_TARGET_EMPTY=confirmed \
    "$script_dir/minio-object-restore.sh" "$temp_dir/unsupported-export" \
        target/fonts "$temp_dir/unsupported-result" \
        >"$unsupported_output" 2>&1; then
    printf 'restore accepted unsupported metadata\n' >&2
    exit 1
fi
grep -F 'supported metadata contract' "$unsupported_output" >/dev/null
[[ ! -e "$temp_dir/unsupported-result" ]]

cp -a "$temp_dir/export" "$temp_dir/tampered-export"
first_payload=$(jq -r '.payload' \
    "$temp_dir/tampered-export/object-manifest.ndjson" | head -n 1)
printf 'tampered payload\n' >"$temp_dir/tampered-export/$first_payload"
tampered_output="$temp_dir/tampered.out"
if MC_BIN="$temp_dir/mc" \
    MC_CONFIG_DIR="$temp_dir/mc-config" \
    EMFONT_RESTORE_TARGET_EMPTY=confirmed \
    "$script_dir/minio-object-restore.sh" "$temp_dir/tampered-export" \
        target/fonts "$temp_dir/tampered-result" \
        >"$tampered_output" 2>&1; then
    printf 'restore accepted a tampered payload\n' >&2
    exit 1
fi
grep -F 'payload size does not match' "$tampered_output" >/dev/null
[[ ! -e "$temp_dir/tampered-result" ]]

restore_output="$temp_dir/restore.out"
MC_BIN="$temp_dir/mc" \
MC_CONFIG_DIR="$temp_dir/mc-config" \
EMFONT_RESTORE_TARGET_EMPTY=confirmed \
    "$script_dir/minio-object-restore.sh" "$temp_dir/export" target/fonts \
        "$temp_dir/restore-result" >"$restore_output" 2>&1
grep -Fx 'Restored and verified 2 objects into bucket fonts.' \
    "$restore_output" >/dev/null
if grep -F 'source/object name.ttf' "$restore_output" >/dev/null; then
    printf 'object key appeared in restore output\n' >&2
    exit 1
fi
jq -e '
    .format == "emfont-object-restore/v1" and
    .object_count == 2 and
    .verification == "payload-checksum-metadata-tags-and-current-version"
' "$temp_dir/restore-result/restore-manifest.json" >/dev/null
jq -s -e '
    length == 2 and all(.[];
        .verified == true and
        (.source.version_id | length > 0) and
        (.destination.version_id | length > 0) and
        .source.checksum == .destination.checksum and
        (.content_sha256 | test("^[0-9a-f]{64}$")) and
        (.metadata_sha256 | test("^[0-9a-f]{64}$")) and
        (.tags_sha256 | test("^[0-9a-f]{64}$")))
' "$temp_dir/restore-result/version-map.ndjson" >/dev/null

nonempty_output="$temp_dir/nonempty.out"
if MC_BIN="$temp_dir/mc" \
    MC_CONFIG_DIR="$temp_dir/mc-config" \
    EMFONT_RESTORE_TARGET_EMPTY=confirmed \
    "$script_dir/minio-object-restore.sh" "$temp_dir/export" target/fonts \
        "$temp_dir/nonempty-result" >"$nonempty_output" 2>&1; then
    printf 'restore accepted a nonempty target version namespace\n' >&2
    exit 1
fi
grep -F 'version namespace is not empty' "$nonempty_output" >/dev/null
[[ ! -e "$temp_dir/nonempty-result" ]]

printf 'object without server checksum\n' >"$temp_dir/no-checksum"
mc cp "$temp_dir/no-checksum" source/fonts/no-checksum >/dev/null
missing_checksum_output="$temp_dir/missing-checksum.out"
if MC_BIN="$temp_dir/mc" \
    MC_CONFIG_DIR="$temp_dir/mc-config" \
    EMFONT_OBJECT_WRITERS_QUIESCED=confirmed \
    "$script_dir/minio-object-export.sh" source/fonts \
        "$temp_dir/missing-checksum-export" \
        >"$missing_checksum_output" 2>&1; then
    printf 'export accepted an object without a server SHA-256 checksum\n' >&2
    exit 1
fi
[[ ! -e "$temp_dir/missing-checksum-export" ]]

printf 'MinIO object export/restore manifest checks passed\n'
