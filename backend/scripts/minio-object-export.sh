#!/bin/bash
# The single-quoted jq programs intentionally reference jq variables.
# shellcheck disable=SC2016
set -Eeuo pipefail
umask 077

export EMFONT_MINIO_HELPER_NAME=minio-object-export
helper_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
# The library is selected relative to the installed helper at runtime.
# shellcheck disable=SC1091
source "$helper_dir/minio-object-manifest-lib.sh"

case $- in
    *x*) minio_manifest_fail "shell tracing must be disabled" ;;
esac
[[ $# -eq 2 ]] || \
    minio_manifest_fail "usage: minio-object-export.sh ALIAS/BUCKET OUTPUT_DIRECTORY"
[[ "${EMFONT_OBJECT_WRITERS_QUIESCED:-}" == confirmed ]] || \
    minio_manifest_fail "EMFONT_OBJECT_WRITERS_QUIESCED=confirmed is required"

remote=$1
requested_output=$2
minio_init_tools
minio_require_command cmp
minio_require_command mv
minio_require_command sort
minio_validate_remote "$remote"

output_parent=$(dirname -- "$requested_output")
output_name=$(basename -- "$requested_output")
[[ "$output_name" != . && "$output_name" != .. && -n "$output_name" ]] || \
    minio_manifest_fail "output directory name is invalid"
[[ -d "$output_parent" && ! -L "$output_parent" ]] || \
    minio_manifest_fail "output parent must be an existing directory"
output_parent=$(CDPATH='' cd -- "$output_parent" && pwd -P)
output="$output_parent/$output_name"
[[ ! -e "$output" && ! -L "$output" ]] || \
    minio_manifest_fail "output directory already exists"

stage=$(mktemp -d "$output_parent/.${output_name}.tmp.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
mkdir "$stage/payloads"
chmod 0700 "$stage/payloads"

version_info="$stage/.version-info.json"
first_list_raw="$stage/.first-list.raw"
first_catalog="$stage/.first-catalog.ndjson"
final_list_raw="$stage/.final-list.raw"
final_catalog="$stage/.final-catalog.ndjson"
object_manifest="$stage/object-manifest.ndjson"
: >"$object_manifest"

minio_require_versioning "$remote" "$version_info"

canonical_list() {
    local raw=$1 output_file=$2
    "$JQ_BIN" -cS '
        def safe_text:
            type == "string" and
            (test("[\u0000-\u001f\u007f]") | not);
        def safe_key:
            safe_text and length > 0 and (startswith("/") | not) and
            (split("/") | all(. != "" and . != "." and . != ".."));
        if
            all(.[];
                keys == [
                    "etag", "key", "lastModified", "size", "status",
                    "storageClass", "type", "url", "versionOrdinal"
                ] and
                .status == "success" and .type == "file" and
                (.key | safe_key) and
                (.lastModified | safe_text and length > 0) and
                (.size | type == "number" and . >= 0 and floor == .) and
                (.etag | type == "string" and
                    test("^[0-9a-f]{32}(-[1-9][0-9]*)?$")) and
                (.url | safe_text and length > 0) and
                (.versionOrdinal | type == "number" and . >= 1 and floor == .) and
                (.storageClass | type == "string" and
                    test("^[A-Z0-9_-]+$"))) and
            ([.[].key] | length == (unique | length))
        then
            sort_by(.key)[] | {
                etag: .etag,
                key: .key,
                size_bytes: .size,
                storage_class: .storageClass
            }
        else
            error("mc list output violates the current-object contract")
        end
    ' "$raw" >"$output_file" || \
        minio_manifest_fail "could not validate mc current-object listing"
}

if ! "$MC_BIN" ls --recursive --json "$remote" >"$first_list_raw" 2>/dev/null; then
    minio_manifest_fail "could not list current objects"
fi
"$JQ_BIN" -s '.' "$first_list_raw" >"$stage/.first-list.json" || \
    minio_manifest_fail "mc current-object listing is not JSON"
canonical_list "$stage/.first-list.json" "$first_catalog"

object_index=0
while IFS= read -r encoded_record; do
    record_file="$stage/.list-$object_index.json"
    stat_file="$stage/.stat-$object_index.json"
    stat_after_file="$stage/.stat-after-$object_index.json"
    tags_file="$stage/.tags-$object_index.json"
    tags_after_file="$stage/.tags-after-$object_index.json"
    entry_file="$stage/.entry-$object_index.json"

    printf '%s' "$encoded_record" | base64 --decode >"$record_file" || \
        minio_manifest_fail "could not decode object listing entry"
    key=$("$JQ_BIN" -er '.key' "$record_file") || \
        minio_manifest_fail "object listing has no key"
    listed_size=$("$JQ_BIN" -er '.size_bytes' "$record_file")
    listed_etag=$("$JQ_BIN" -er '.etag' "$record_file")
    storage_class=$("$JQ_BIN" -er '.storage_class' "$record_file")
    key_sha256=$(printf '%s' "$key" | sha256sum | awk '{print $1}')
    [[ "$key_sha256" =~ ^[0-9a-f]{64}$ ]] || \
        minio_manifest_fail "could not digest object key"
    payload="payloads/$key_sha256"
    payload_path="$stage/$payload"
    [[ ! -e "$payload_path" ]] || \
        minio_manifest_fail "object key digest collision"
    object_url="$remote/$key"

    if ! "$MC_BIN" stat --json "$object_url" >"$stat_file" 2>/dev/null; then
        minio_manifest_fail "could not stat a current object"
    fi
    "$JQ_BIN" -e \
        --arg etag "$listed_etag" \
        --argjson size "$listed_size" '
        keys == [
            "checksum", "etag", "lastModified", "metadata", "name",
            "size", "status", "type", "versionID"
        ] and
        .status == "success" and .type == "file" and
        .etag == $etag and .size == $size and
        (.lastModified | type == "string" and length > 0) and
        (.name | type == "string" and length > 0) and
        (.versionID | type == "string" and length > 0 and . != "null") and
        (.checksum | keys == ["SHA256"]) and
        (.metadata | type == "object")
    ' "$stat_file" >/dev/null || \
        minio_manifest_fail "current object identity changed after listing"
    version_id=$("$JQ_BIN" -er '.versionID' "$stat_file")

    if ! "$MC_BIN" tag list --json --version-id "$version_id" \
        "$object_url" >"$tags_file" 2>/dev/null; then
        minio_manifest_fail "could not read tags for a pinned object version"
    fi
    "$JQ_BIN" -e --arg version_id "$version_id" '
        ((keys == ["status", "url", "versionID"]) or
         (keys == ["status", "tagset", "url", "versionID"])) and
        .status == "success" and .versionID == $version_id and
        (.url | type == "string" and length > 0) and
        ((.tagset // {}) | type == "object" and
            all(to_entries[];
                (.key | type == "string") and
                (.value | type == "string")))
    ' "$tags_file" >/dev/null || \
        minio_manifest_fail "pinned object tags violate the manifest contract"

    if ! "$MC_BIN" cp --quiet --version-id "$version_id" \
        "$object_url" "$payload_path" >/dev/null 2>&1; then
        minio_manifest_fail "could not download a pinned object version"
    fi
    [[ -f "$payload_path" && ! -L "$payload_path" ]] || \
        minio_manifest_fail "downloaded payload is not a regular file"

    if ! "$MC_BIN" stat --json --version-id "$version_id" \
        "$object_url" >"$stat_after_file" 2>/dev/null; then
        minio_manifest_fail "could not re-stat a pinned object version"
    fi
    cmp --silent \
        <("$JQ_BIN" -cS . "$stat_file") \
        <("$JQ_BIN" -cS . "$stat_after_file") || \
        minio_manifest_fail "pinned object metadata changed during export"
    if ! "$MC_BIN" tag list --json --version-id "$version_id" \
        "$object_url" >"$tags_after_file" 2>/dev/null; then
        minio_manifest_fail "could not re-read pinned object tags"
    fi
    cmp --silent \
        <("$JQ_BIN" -cS '.tagset // {}' "$tags_file") \
        <("$JQ_BIN" -cS '.tagset // {}' "$tags_after_file") || \
        minio_manifest_fail "pinned object tags changed during export"

    payload_sha256=$(minio_sha256_file "$payload_path")
    payload_size=$(stat -c '%s' -- "$payload_path")
    [[ "$payload_size" == "$listed_size" ]] || \
        minio_manifest_fail "downloaded payload size does not match object stat"
    source_checksum=$("$JQ_BIN" -er '.checksum.SHA256' "$stat_file")
    [[ "$(minio_checksum_hex "$source_checksum")" == "$payload_sha256" ]] || \
        minio_manifest_fail "downloaded payload does not match the server SHA-256 checksum"

    "$JQ_BIN" -cnS \
        --arg key "$key" \
        --arg key_sha256 "$key_sha256" \
        --arg payload "$payload" \
        --arg sha256 "$payload_sha256" \
        --arg storage_class "$storage_class" \
        --argjson size_bytes "$payload_size" \
        --slurpfile stat "$stat_file" \
        --slurpfile tags "$tags_file" '
        {
            checksum: $stat[0].checksum,
            key: $key,
            key_sha256: $key_sha256,
            metadata: $stat[0].metadata,
            payload: $payload,
            sha256: $sha256,
            size_bytes: $size_bytes,
            source: {
                etag: $stat[0].etag,
                last_modified: $stat[0].lastModified,
                version_id: $stat[0].versionID
            },
            storage_class: $storage_class,
            tags: ($tags[0].tagset // {})
        }
    ' >"$entry_file"
    minio_validate_object_entry "$entry_file"
    "$JQ_BIN" -cS . "$entry_file" >>"$object_manifest"
    object_index=$((object_index + 1))
done < <("$JQ_BIN" -r '@base64' "$first_catalog")

if ! "$MC_BIN" ls --recursive --json "$remote" >"$final_list_raw" 2>/dev/null; then
    minio_manifest_fail "could not re-list current objects"
fi
"$JQ_BIN" -s '.' "$final_list_raw" >"$stage/.final-list.json" || \
    minio_manifest_fail "final mc current-object listing is not JSON"
canonical_list "$stage/.final-list.json" "$final_catalog"
cmp --silent "$first_catalog" "$final_catalog" || \
    minio_manifest_fail "current object set changed during export"

final_index=0
while IFS= read -r encoded_entry; do
    final_entry="$stage/.final-entry-$final_index.json"
    final_stat="$stage/.final-stat-$final_index.json"
    final_tags="$stage/.final-tags-$final_index.json"
    printf '%s' "$encoded_entry" | base64 --decode >"$final_entry" || \
        minio_manifest_fail "could not decode final object manifest entry"
    key=$("$JQ_BIN" -er '.key' "$final_entry")
    version_id=$("$JQ_BIN" -er '.source.version_id' "$final_entry")
    object_url="$remote/$key"
    if ! "$MC_BIN" stat --json "$object_url" >"$final_stat" 2>/dev/null; then
        minio_manifest_fail "could not perform final current-version stat"
    fi
    "$JQ_BIN" -e --slurpfile expected "$final_entry" '
        .status == "success" and .type == "file" and
        .versionID == $expected[0].source.version_id and
        .etag == $expected[0].source.etag and
        .size == $expected[0].size_bytes and
        .checksum == $expected[0].checksum and
        .metadata == $expected[0].metadata
    ' "$final_stat" >/dev/null || \
        minio_manifest_fail "current object identity changed before export completion"
    if ! "$MC_BIN" tag list --json --version-id "$version_id" \
        "$object_url" >"$final_tags" 2>/dev/null; then
        minio_manifest_fail "could not perform final pinned-version tag read"
    fi
    "$JQ_BIN" -e --slurpfile expected "$final_entry" \
        '(.tagset // {}) == $expected[0].tags' "$final_tags" >/dev/null || \
        minio_manifest_fail "current object tags changed before export completion"
    final_index=$((final_index + 1))
done < <("$JQ_BIN" -r '@base64' "$object_manifest")
[[ "$final_index" -eq "$object_index" ]] || \
    minio_manifest_fail "final object verification count is inconsistent"

object_count=$(wc -l <"$object_manifest")
[[ "$object_count" -eq "$object_index" ]] || \
    minio_manifest_fail "object manifest count is inconsistent"
total_size_bytes=$("$JQ_BIN" -s 'map(.size_bytes) | add // 0' "$object_manifest")
object_manifest_sha256=$(minio_sha256_file "$object_manifest")

"$JQ_BIN" -nS \
    --arg bucket "$EMFONT_REMOTE_BUCKET" \
    --argjson object_count "$object_count" \
    --argjson total_size_bytes "$total_size_bytes" \
    --arg object_manifest_sha256 "$object_manifest_sha256" \
    --arg mc_binary_sha256 "$MC_BINARY_SHA256" \
    --arg mc_commit "$EMFONT_MC_COMMIT" \
    --arg mc_release "$EMFONT_MC_RELEASE" \
    --arg jq_version "$JQ_VERSION" '
    {
        bucket: $bucket,
        checksum_contract: "server-and-payload-sha256",
        format: "emfont-object-export/v1",
        metadata_contract: "emfont-minio-metadata/v1",
        object_count: $object_count,
        object_manifest: "object-manifest.ndjson",
        object_manifest_sha256: $object_manifest_sha256,
        payload_directory: "payloads",
        producer: {
            helper: "minio-object-export.sh",
            jq_version: $jq_version,
            mc_binary_sha256: $mc_binary_sha256,
            mc_commit: $mc_commit,
            mc_release: $mc_release
        },
        total_size_bytes: $total_size_bytes,
        version_scope: "current-only-pinned-version-ids"
    }
    ' >"$stage/export-manifest.json"

rm -f -- "$stage"/.*.json "$stage"/.*.raw "$stage"/.*.ndjson 2>/dev/null || true
[[ ! -e "$output" && ! -L "$output" ]] || \
    minio_manifest_fail "output directory appeared during export"
mv -T -- "$stage" "$output"
trap - EXIT HUP INT TERM
printf 'Exported %s current object versions from bucket %s.\n' \
    "$object_count" "$EMFONT_REMOTE_BUCKET"
