#!/bin/bash

EMFONT_MC_RELEASE=RELEASE.2025-08-13T08-35-41Z
EMFONT_MC_COMMIT=7394ce0dd2a80935aded936b09fa12cbb3cb8096

minio_manifest_fail() {
    printf '%s: %s\n' "$EMFONT_MINIO_HELPER_NAME" "$*" >&2
    exit 1
}

minio_require_command() {
    command -v "$1" >/dev/null 2>&1 || \
        minio_manifest_fail "$1 is required"
}

minio_sha256_file() {
    local result
    result=$(sha256sum -- "$1") || return
    result=${result%% *}
    result=${result#\\}
    [[ "$result" =~ ^[0-9a-f]{64}$ ]] || \
        minio_manifest_fail "could not calculate a canonical SHA-256 digest"
    printf '%s\n' "$result"
}

minio_init_tools() {
    MC_BIN=${MC_BIN:-mc}
    JQ_BIN=${JQ_BIN:-jq}
    minio_require_command "$MC_BIN"
    minio_require_command "$JQ_BIN"
    minio_require_command base64
    minio_require_command od
    minio_require_command readlink
    minio_require_command sed
    minio_require_command sha256sum
    minio_require_command stat
    minio_require_command tr

    [[ -n "${MC_CONFIG_DIR:-}" && -d "$MC_CONFIG_DIR" && ! -L "$MC_CONFIG_DIR" ]] || \
        minio_manifest_fail "MC_CONFIG_DIR must name a private directory"
    local config_metadata
    config_metadata=$(stat -c '%u:%a' -- "$MC_CONFIG_DIR") || \
        minio_manifest_fail "could not inspect MC_CONFIG_DIR"
    [[ "$config_metadata" == "$(id -u):700" ]] || \
        minio_manifest_fail "MC_CONFIG_DIR must be owned by the current user with mode 0700"

    local mc_path version_output runtime_line
    mc_path=$(command -v "$MC_BIN") || minio_manifest_fail "could not resolve mc"
    mc_path=$(readlink -f -- "$mc_path") || \
        minio_manifest_fail "could not resolve the mc binary"
    [[ -f "$mc_path" && -x "$mc_path" ]] || \
        minio_manifest_fail "mc must resolve to an executable regular file"
    version_output=$("$MC_BIN" --version) || \
        minio_manifest_fail "could not inspect mc version"
    MC_VERSION_LINE=${version_output%%$'\n'*}
    [[ "$MC_VERSION_LINE" == \
        "mc version $EMFONT_MC_RELEASE (commit-id=$EMFONT_MC_COMMIT)" ]] || \
        minio_manifest_fail "mc release or commit does not match the reviewed client"
    runtime_line=$(printf '%s\n' "$version_output" | sed -n '2p')
    [[ "$runtime_line" =~ ^Runtime:\ go1\.26\.5\ linux/(amd64|arm64)$ ]] || \
        minio_manifest_fail "mc runtime does not match the reviewed client"
    MC_BINARY_SHA256=$(minio_sha256_file "$mc_path")
    export MC_BINARY_SHA256

    JQ_VERSION=$("$JQ_BIN" --version) || \
        minio_manifest_fail "could not inspect jq version"
    [[ "$JQ_VERSION" == jq-1.6 ]] || \
        minio_manifest_fail "jq 1.6 is required by the manifest contract"
}

minio_validate_remote() {
    local remote=$1 alias bucket
    [[ "$remote" != */*/* ]] || \
        minio_manifest_fail "remote must contain exactly an alias and bucket"
    alias=${remote%%/*}
    bucket=${remote#*/}
    [[ "$alias" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$ ]] || \
        minio_manifest_fail "remote alias contains unsupported characters"
    [[ "$bucket" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] || \
        minio_manifest_fail "remote bucket is not a DNS-style bucket name"
    [[ "$bucket" != *..* && "$bucket" != *.-* && "$bucket" != *-.* ]] || \
        minio_manifest_fail "remote bucket is not a DNS-style bucket name"
    EMFONT_REMOTE_BUCKET=$bucket
    export EMFONT_REMOTE_BUCKET
}

minio_require_versioning() {
    local remote=$1 output=$2
    if ! "$MC_BIN" version info --json "$remote" >"$output" 2>/dev/null; then
        minio_manifest_fail "could not inspect bucket versioning"
    fi
    $JQ_BIN -e '
        keys == ["Op", "status", "url", "versioning"] and
        .Op == "info" and
        .status == "success" and
        (.url | type == "string") and
        (.versioning | keys == ["MFADelete", "status"]) and
        .versioning.status == "Enabled"
    ' "$output" >/dev/null || \
        minio_manifest_fail "bucket versioning must be enabled"
}

minio_validate_object_entry() {
    local entry_file=$1
    "$JQ_BIN" -e '
        def safe_text:
            type == "string" and
            (test("[\u0000-\u001f\u007f]") | not);
        def safe_key:
            safe_text and length > 0 and (startswith("/") | not) and
            (split("/") | all(. != "" and . != "." and . != ".."));
        def metadata_key:
            . == "Content-Type" or
            . == "Cache-Control" or
            . == "Content-Disposition" or
            . == "Content-Encoding" or
            . == "Content-Language" or
            . == "X-Amz-Storage-Class" or
            . == "X-Amz-Tagging-Count" or
            test("^X-Amz-Meta-[A-Za-z0-9._-]+$");
	    def metadata_value:
	        safe_text;
        keys == [
            "checksum", "key", "key_sha256", "metadata", "payload",
            "sha256", "size_bytes", "source", "storage_class", "tags"
        ] and
        (.key | safe_key) and
        (.key_sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
        .payload == ("payloads/" + .key_sha256) and
        (.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
        (.size_bytes | type == "number" and . >= 0 and floor == .) and
        (.storage_class | type == "string" and test("^[A-Z0-9_-]+$")) and
        (.checksum | keys == ["SHA256"]) and
        (.checksum.SHA256 | type == "string" and
            test("^[A-Za-z0-9+/]{43}=$")) and
        (.source | keys == ["etag", "last_modified", "version_id"]) and
        (.source.etag | type == "string" and
            test("^[0-9a-f]{32}(-[1-9][0-9]*)?$")) and
        (.source.last_modified | safe_text and length > 0) and
        (.source.version_id | safe_text and length > 0 and . != "null") and
        (.metadata | type == "object" and
            has("Content-Type") and
            (."Content-Type" | safe_text and length > 0) and
            all(to_entries[]; (.key | metadata_key) and
                (.value | metadata_value))) and
        (.tags | type == "object" and
            all(to_entries[];
                (.key | safe_text and length > 0) and
                (.value | safe_text))) and
        ((.tags | length) <= 10) and
        (if (.tags | length) == 0 then
            (.metadata | has("X-Amz-Tagging-Count") | not)
         else
            .metadata["X-Amz-Tagging-Count"] == ((.tags | length) | tostring)
         end) and
        (if .storage_class == "STANDARD" then
            (.metadata["X-Amz-Storage-Class"] // "STANDARD") == "STANDARD"
         else
            .metadata["X-Amz-Storage-Class"] == .storage_class
         end)
	' "$entry_file" >/dev/null || \
	    minio_manifest_fail "object manifest entry violates the supported metadata contract"
	minio_metadata_attr "$entry_file" >/dev/null
}

minio_checksum_hex() {
    local checksum=$1 result
    result=$(printf '%s' "$checksum" | base64 --decode | \
        od -An -v -tx1 | tr -d ' \n') || \
        minio_manifest_fail "object checksum is not valid base64"
    [[ "$result" =~ ^[0-9a-f]{64}$ ]] || \
        minio_manifest_fail "object checksum is not a 32-byte SHA-256 value"
    printf '%s\n' "$result"
}

minio_metadata_attr() {
    local entry_file=$1 encoded pair key value item result=
    while IFS= read -r encoded; do
        pair=$(printf '%s' "$encoded" | base64 --decode) || \
            minio_manifest_fail "could not decode metadata"
        key=$("$JQ_BIN" -er '.[0]' <<<"$pair") || \
            minio_manifest_fail "could not decode metadata key"
        value=$("$JQ_BIN" -er '.[1]' <<<"$pair") || \
            minio_manifest_fail "could not decode metadata value"
        if [[ "$value" != *"'"* ]]; then
            item="$key='$value'"
        elif [[ "$value" != *'"'* ]]; then
            item="$key=\"$value\""
        else
            minio_manifest_fail "metadata value cannot be represented by the reviewed mc client"
        fi
        if [[ -n "$result" ]]; then
            result+=';'
        fi
        result+=$item
    done < <("$JQ_BIN" -r '
        .metadata | to_entries | sort_by(.key)[] |
        select(.key != "X-Amz-Storage-Class" and
               .key != "X-Amz-Tagging-Count") |
        [.key, .value] | @base64
    ' "$entry_file")
    [[ -n "$result" ]] || \
        minio_manifest_fail "object metadata has no uploadable content type"
    printf '%s\n' "$result"
}

minio_tags_query() {
    "$JQ_BIN" -er '
        .tags | to_entries | sort_by(.key) |
        map((.key | @uri) + "=" + (.value | @uri)) | join("&")
    ' "$1"
}
