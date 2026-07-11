#!/bin/bash
# The single-quoted jq programs intentionally reference jq variables.
# shellcheck disable=SC2016
set -Eeuo pipefail
umask 077

export EMFONT_MINIO_HELPER_NAME=minio-object-restore
helper_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
# The library is selected relative to the installed helper at runtime.
# shellcheck disable=SC1091
source "$helper_dir/minio-object-manifest-lib.sh"

case $- in
    *x*) minio_manifest_fail "shell tracing must be disabled" ;;
esac
[[ $# -eq 3 ]] || minio_manifest_fail \
    "usage: minio-object-restore.sh EXPORT_DIRECTORY ALIAS/BUCKET RESULT_DIRECTORY"
[[ "${EMFONT_RESTORE_TARGET_EMPTY:-}" == confirmed ]] || \
    minio_manifest_fail "EMFONT_RESTORE_TARGET_EMPTY=confirmed is required"

export_dir=$1
remote=$2
requested_result=$3
minio_init_tools
minio_require_command cmp
minio_require_command find
minio_require_command mv
minio_require_command sort
minio_validate_remote "$remote"

[[ -d "$export_dir" && ! -L "$export_dir" ]] || \
    minio_manifest_fail "export directory must be a real directory"
export_dir=$(CDPATH='' cd -- "$export_dir" && pwd -P)
export_manifest="$export_dir/export-manifest.json"
object_manifest="$export_dir/object-manifest.ndjson"
[[ -f "$export_manifest" && ! -L "$export_manifest" ]] || \
    minio_manifest_fail "export manifest is missing or is not a regular file"
[[ -f "$object_manifest" && ! -L "$object_manifest" ]] || \
    minio_manifest_fail "object manifest is missing or is not a regular file"

result_parent=$(dirname -- "$requested_result")
result_name=$(basename -- "$requested_result")
[[ "$result_name" != . && "$result_name" != .. && -n "$result_name" ]] || \
    minio_manifest_fail "result directory name is invalid"
[[ -d "$result_parent" && ! -L "$result_parent" ]] || \
    minio_manifest_fail "result parent must be an existing directory"
result_parent=$(CDPATH='' cd -- "$result_parent" && pwd -P)
result="$result_parent/$result_name"
[[ ! -e "$result" && ! -L "$result" ]] || \
    minio_manifest_fail "result directory already exists"

stage=$(mktemp -d "$result_parent/.${result_name}.tmp.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
version_map="$stage/version-map.ndjson"
: >"$version_map"

"$JQ_BIN" -e \
    --arg bucket "$EMFONT_REMOTE_BUCKET" \
    --arg mc_commit "$EMFONT_MC_COMMIT" \
    --arg mc_release "$EMFONT_MC_RELEASE" '
    keys == [
        "bucket", "checksum_contract", "format", "metadata_contract",
        "object_count", "object_manifest", "object_manifest_sha256",
        "payload_directory", "producer", "total_size_bytes", "version_scope"
    ] and
    .format == "emfont-object-export/v1" and
    .version_scope == "current-only-pinned-version-ids" and
    .checksum_contract == "server-and-payload-sha256" and
    .metadata_contract == "emfont-minio-metadata/v1" and
    .bucket == $bucket and
    (.object_count | type == "number" and . >= 0 and floor == .) and
    (.total_size_bytes | type == "number" and . >= 0 and floor == .) and
    .object_manifest == "object-manifest.ndjson" and
    (.object_manifest_sha256 | type == "string" and
        test("^[0-9a-f]{64}$")) and
    .payload_directory == "payloads" and
    (.producer | keys == [
        "helper", "jq_version", "mc_binary_sha256", "mc_commit", "mc_release"
    ]) and
    .producer.helper == "minio-object-export.sh" and
    .producer.jq_version == "jq-1.6" and
    (.producer.mc_binary_sha256 | type == "string" and
        test("^[0-9a-f]{64}$")) and
    .producer.mc_commit == $mc_commit and
    .producer.mc_release == $mc_release
    ' "$export_manifest" >/dev/null || \
    minio_manifest_fail "export manifest violates the reviewed contract"

expected_manifest_sha256=$("$JQ_BIN" -er \
    '.object_manifest_sha256' "$export_manifest")
[[ "$(minio_sha256_file "$object_manifest")" == "$expected_manifest_sha256" ]] || \
    minio_manifest_fail "object manifest checksum does not match export manifest"

"$JQ_BIN" -s '.' "$object_manifest" >"$stage/.objects.json" || \
    minio_manifest_fail "object manifest is not newline-delimited JSON"
"$JQ_BIN" -e '
    ([.[].key] == ([.[].key] | sort)) and
    ([.[].key] | length == (unique | length)) and
    ([.[].key_sha256] | length == (unique | length)) and
    ([.[].payload] | length == (unique | length))
    ' "$stage/.objects.json" >/dev/null || \
    minio_manifest_fail "object manifest keys are not unique and sorted"

expected_count=$("$JQ_BIN" -er '.object_count' "$export_manifest")
expected_bytes=$("$JQ_BIN" -er '.total_size_bytes' "$export_manifest")
actual_count=$("$JQ_BIN" -r 'length' "$stage/.objects.json")
actual_bytes=$("$JQ_BIN" -r 'map(.size_bytes) | add // 0' "$stage/.objects.json")
[[ "$actual_count" == "$expected_count" && "$actual_bytes" == "$expected_bytes" ]] || \
    minio_manifest_fail "object manifest summary does not match export manifest"
[[ "$(wc -l <"$object_manifest")" == "$expected_count" ]] || \
    minio_manifest_fail "object manifest line count is inconsistent"

: >"$stage/.expected-files"
printf '%s\n' export-manifest.json object-manifest.ndjson \
    >>"$stage/.expected-files"

preflight_index=0
while IFS= read -r encoded_entry; do
    entry_file="$stage/.preflight-$preflight_index.json"
    printf '%s' "$encoded_entry" | base64 --decode >"$entry_file" || \
        minio_manifest_fail "could not decode object manifest entry"
    minio_validate_object_entry "$entry_file"
    key=$("$JQ_BIN" -er '.key' "$entry_file")
    recorded_key_sha256=$("$JQ_BIN" -er '.key_sha256' "$entry_file")
    calculated_key_sha256=$(printf '%s' "$key" | sha256sum | awk '{print $1}')
    [[ "$recorded_key_sha256" == "$calculated_key_sha256" ]] || \
        minio_manifest_fail "object key digest does not match its manifest entry"
    payload=$("$JQ_BIN" -er '.payload' "$entry_file")
    payload_path="$export_dir/$payload"
    [[ -f "$payload_path" && ! -L "$payload_path" ]] || \
        minio_manifest_fail "manifest payload is missing or is not a regular file"
    recorded_size=$("$JQ_BIN" -er '.size_bytes' "$entry_file")
    recorded_sha256=$("$JQ_BIN" -er '.sha256' "$entry_file")
    server_checksum=$("$JQ_BIN" -er '.checksum.SHA256' "$entry_file")
    [[ "$(stat -c '%s' -- "$payload_path")" == "$recorded_size" ]] || \
        minio_manifest_fail "payload size does not match object manifest"
    [[ "$(minio_sha256_file "$payload_path")" == "$recorded_sha256" ]] || \
        minio_manifest_fail "payload SHA-256 does not match object manifest"
    [[ "$(minio_checksum_hex "$server_checksum")" == "$recorded_sha256" ]] || \
        minio_manifest_fail "source server checksum does not match payload SHA-256"
	minio_tags_query "$entry_file" >/dev/null
    printf '%s\n' "$payload" >>"$stage/.expected-files"
    preflight_index=$((preflight_index + 1))
done < <("$JQ_BIN" -r '.[] | @base64' "$stage/.objects.json")
[[ "$preflight_index" == "$expected_count" ]] || \
    minio_manifest_fail "preflight object count is inconsistent"

LC_ALL=C sort -o "$stage/.expected-files" "$stage/.expected-files"
if find "$export_dir" -mindepth 1 -type l -print -quit | grep -q .; then
    minio_manifest_fail "export directory must not contain symbolic links"
fi
find "$export_dir" -mindepth 1 -type f -printf '%P\n' | \
    LC_ALL=C sort >"$stage/.actual-files"
cmp --silent "$stage/.expected-files" "$stage/.actual-files" || \
    minio_manifest_fail "export directory has missing or unmanifested files"
if find "$export_dir" -mindepth 1 -type d ! -path "$export_dir/payloads" \
    -print -quit | grep -q .; then
    minio_manifest_fail "export directory has an unmanifested directory"
fi

minio_require_versioning "$remote" "$stage/.target-version-info.json"
if ! "$MC_BIN" ls --recursive --versions --json "$remote" \
    >"$stage/.target-versions.raw" 2>/dev/null; then
    minio_manifest_fail "could not inspect restore target version history"
fi
"$JQ_BIN" -s -e 'length == 0' "$stage/.target-versions.raw" >/dev/null || \
    minio_manifest_fail "restore target version namespace is not empty"

restore_index=0
while IFS= read -r encoded_entry; do
    entry_file="$stage/.restore-$restore_index.json"
    destination_stat="$stage/.destination-stat-$restore_index.json"
    destination_tags="$stage/.destination-tags-$restore_index.json"
    verify_payload="$stage/.verify-payload-$restore_index"
    mapping_entry="$stage/.mapping-$restore_index.json"
    printf '%s' "$encoded_entry" | base64 --decode >"$entry_file" || \
        minio_manifest_fail "could not decode restore manifest entry"

    key=$("$JQ_BIN" -er '.key' "$entry_file")
    payload=$("$JQ_BIN" -er '.payload' "$entry_file")
    payload_path="$export_dir/$payload"
    storage_class=$("$JQ_BIN" -er '.storage_class' "$entry_file")
    metadata_attr=$(minio_metadata_attr "$entry_file")
    tags_query=$(minio_tags_query "$entry_file")
    destination_url="$remote/$key"
    copy_arguments=(
        cp --quiet --checksum SHA256
        --storage-class "$storage_class"
        --attr "$metadata_attr"
    )
    if [[ -n "$tags_query" ]]; then
        copy_arguments+=(--tags "$tags_query")
    fi
    copy_arguments+=("$payload_path" "$destination_url")
    if ! "$MC_BIN" "${copy_arguments[@]}" >/dev/null 2>&1; then
        minio_manifest_fail "could not upload a manifested object"
    fi

    if ! "$MC_BIN" stat --json "$destination_url" \
        >"$destination_stat" 2>/dev/null; then
        minio_manifest_fail "could not stat a restored object"
    fi
    "$JQ_BIN" -e --slurpfile expected "$entry_file" '
        keys == [
            "checksum", "etag", "lastModified", "metadata", "name",
            "size", "status", "type", "versionID"
        ] and
        .status == "success" and .type == "file" and
        (.versionID | type == "string" and length > 0 and . != "null") and
        (.etag | type == "string" and
            test("^[0-9a-f]{32}(-[1-9][0-9]*)?$")) and
        .size == $expected[0].size_bytes and
        .checksum == $expected[0].checksum and
        .metadata == $expected[0].metadata
    ' "$destination_stat" >/dev/null || \
        minio_manifest_fail "restored object identity, checksum, or metadata differs"
    destination_version=$("$JQ_BIN" -er '.versionID' "$destination_stat")

    if ! "$MC_BIN" tag list --json --version-id "$destination_version" \
        "$destination_url" >"$destination_tags" 2>/dev/null; then
        minio_manifest_fail "could not read restored object tags"
    fi
    "$JQ_BIN" -e \
        --arg version_id "$destination_version" \
        --slurpfile expected "$entry_file" '
        .status == "success" and .versionID == $version_id and
        (.tagset // {}) == $expected[0].tags
    ' "$destination_tags" >/dev/null || \
        minio_manifest_fail "restored object tags differ from the manifest"

    if ! "$MC_BIN" cp --quiet --version-id "$destination_version" \
        "$destination_url" "$verify_payload" >/dev/null 2>&1; then
        minio_manifest_fail "could not download a restored pinned version"
    fi
    [[ "$(minio_sha256_file "$verify_payload")" == \
        "$("$JQ_BIN" -er '.sha256' "$entry_file")" ]] || \
        minio_manifest_fail "restored payload SHA-256 differs from the manifest"
    rm -f -- "$verify_payload"

    "$JQ_BIN" -cS '.metadata' "$entry_file" >"$stage/.metadata-$restore_index.json"
    "$JQ_BIN" -cS '.tags' "$entry_file" >"$stage/.tags-map-$restore_index.json"
    metadata_sha256=$(minio_sha256_file "$stage/.metadata-$restore_index.json")
    tags_sha256=$(minio_sha256_file "$stage/.tags-map-$restore_index.json")
    "$JQ_BIN" -cnS \
        --arg key "$key" \
        --arg metadata_sha256 "$metadata_sha256" \
        --arg tags_sha256 "$tags_sha256" \
        --slurpfile source "$entry_file" \
        --slurpfile destination "$destination_stat" '
        {
            content_sha256: $source[0].sha256,
            destination: {
                checksum: $destination[0].checksum,
                etag: $destination[0].etag,
                version_id: $destination[0].versionID
            },
            key: $key,
            metadata_sha256: $metadata_sha256,
            size_bytes: $source[0].size_bytes,
            source: {
                checksum: $source[0].checksum,
                etag: $source[0].source.etag,
                version_id: $source[0].source.version_id
            },
            tags_sha256: $tags_sha256,
            verified: true
        }
    ' >"$mapping_entry"
    "$JQ_BIN" -cS . "$mapping_entry" >>"$version_map"
    restore_index=$((restore_index + 1))
done < <("$JQ_BIN" -r '.[] | @base64' "$stage/.objects.json")
[[ "$restore_index" == "$expected_count" ]] || \
    minio_manifest_fail "restored object count is inconsistent"

if ! "$MC_BIN" ls --recursive --versions --json "$remote" \
    >"$stage/.restored-versions.raw" 2>/dev/null; then
    minio_manifest_fail "could not inspect restored version history"
fi
"$JQ_BIN" -s -e --argjson count "$expected_count" '
    length == $count and
    all(.[];
        .status == "success" and .type == "file" and
        (.versionId | type == "string" and length > 0 and . != "null"))
    ' "$stage/.restored-versions.raw" >/dev/null || \
    minio_manifest_fail "restored version namespace contains unexpected entries"

version_map_sha256=$(minio_sha256_file "$version_map")
source_export_manifest_sha256=$(minio_sha256_file "$export_manifest")
"$JQ_BIN" -nS \
    --arg bucket "$EMFONT_REMOTE_BUCKET" \
    --argjson object_count "$expected_count" \
    --argjson total_size_bytes "$expected_bytes" \
    --arg source_export_manifest_sha256 "$source_export_manifest_sha256" \
    --arg source_object_manifest_sha256 "$expected_manifest_sha256" \
    --arg version_map_sha256 "$version_map_sha256" \
    --arg mc_binary_sha256 "$MC_BINARY_SHA256" \
    --arg mc_commit "$EMFONT_MC_COMMIT" \
    --arg mc_release "$EMFONT_MC_RELEASE" \
    --arg jq_version "$JQ_VERSION" '
    {
        destination_bucket: $bucket,
        format: "emfont-object-restore/v1",
        object_count: $object_count,
        producer: {
            helper: "minio-object-restore.sh",
            jq_version: $jq_version,
            mc_binary_sha256: $mc_binary_sha256,
            mc_commit: $mc_commit,
            mc_release: $mc_release
        },
        source_export_manifest_sha256: $source_export_manifest_sha256,
        source_object_manifest_sha256: $source_object_manifest_sha256,
        total_size_bytes: $total_size_bytes,
        verification: "payload-checksum-metadata-tags-and-current-version",
        version_map: "version-map.ndjson",
        version_map_sha256: $version_map_sha256
    }
    ' >"$stage/restore-manifest.json"

rm -f -- "$stage"/.* 2>/dev/null || true
[[ ! -e "$result" && ! -L "$result" ]] || \
    minio_manifest_fail "result directory appeared during restore"
mv -T -- "$stage" "$result"
trap - EXIT HUP INT TERM
printf 'Restored and verified %s objects into bucket %s.\n' \
    "$expected_count" "$EMFONT_REMOTE_BUCKET"
