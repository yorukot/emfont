#!/usr/bin/env bash
set -Eeuo pipefail

readonly schema='emfont-fontworker-native-packages-v1'

usage() {
    printf 'usage: %s build WORKER BUILD_MANIFEST RUNTIME_MANIFEST\n' "${0##*/}" >&2
    printf '       %s runtime WORKER RUNTIME_MANIFEST\n' "${0##*/}" >&2
    printf '       %s validate MANIFEST\n' "${0##*/}" >&2
    printf '       %s digest MANIFEST\n' "${0##*/}" >&2
    exit 2
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || {
        printf 'required command is unavailable: %s\n' "$1" >&2
        exit 2
    }
}

canonical_path() {
    local path="$1"
    path="$(readlink -f -- "$path")"
    [[ -n "$path" && -f "$path" ]] || {
        printf 'package manifest path is not a regular file: %s\n' "$1" >&2
        return 1
    }
    printf '%s\n' "$path"
}

package_record() {
    local kind="$1"
    local name="$2"
    local input_path="$3"
    local path owner package version architecture query_path candidate

    path="$(canonical_path "$input_path")"
    owner=''
    for candidate in "$input_path" "$path"; do
        [[ "$candidate" == /* ]] || continue
        query_path="${candidate%/}"
        owner="$(dpkg-query -S "$query_path" 2>/dev/null | awk -F': ' -v path="$query_path" '
            $1 !~ /^diversion / && $2 == path { print $1; found = 1; exit }
            END { if (!found) exit 1 }
        ')" || owner=''
        [[ -n "$owner" ]] && break
    done
    if [[ -z "$owner" && "$path" == /usr/* ]]; then
        query_path="/${path#/usr/}"
        owner="$(dpkg-query -S "$query_path" 2>/dev/null | awk -F': ' -v path="$query_path" '
            $1 !~ /^diversion / && $2 == path { print $1; found = 1; exit }
            END { if (!found) exit 1 }
        ')" || owner=''
    fi
    if [[ -z "$owner" ]]; then
        printf 'no Debian package owns %s (%s)\n' "$name" "$path" >&2
        return 1
    fi
    IFS=$'\t' read -r package version architecture < <(
        dpkg-query -W -f='${binary:Package}\t${Version}\t${Architecture}\n' "$owner"
    )
    for value in "$kind" "$name" "$path" "$package" "$version" "$architecture"; do
        [[ -n "$value" && "$value" != *$'\t'* && "$value" != *$'\n'* ]] || {
            printf 'invalid package manifest field for %s\n' "$name" >&2
            return 1
        }
    done
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$kind" "$name" "$path" "$package" "$version" "$architecture"
}

runtime_records() (
    local worker="$1"
    local ldd_output name path
    ldd_output="$(mktemp "${TMPDIR:-/tmp}/emfont-fontworker-ldd.XXXXXX")"
    trap 'rm -f "${ldd_output:-}"' EXIT

    LC_ALL=C ldd "$worker" >"$ldd_output"
    if grep -Fq 'not found' "$ldd_output"; then
        printf 'fontworker has unresolved runtime libraries:\n' >&2
        cat "$ldd_output" >&2
        return 1
    fi

    while IFS=$'\t' read -r name path; do
        [[ -n "$name" && -n "$path" ]] || continue
        package_record runtime "$name" "$path"
    done < <(
        awk '
            $2 == "=>" && $3 ~ /^\// { print $1 "\t" $3; next }
            $1 ~ /^\// { path = $1; sub(/^.*\//, "", $1); print $1 "\t" path }
        ' "$ldd_output" | LC_ALL=C sort -u
    )
)

tool_records() {
    local cc cxx path
    cc="$(go env CC)"
    cxx="$(go env CXX)"
    [[ "$cc" != *[[:space:]]* && "$cxx" != *[[:space:]]* ]] || {
        printf 'compiler command names must not contain whitespace\n' >&2
        return 1
    }

    package_record tool cc "$(command -v "$cc")"
    package_record tool cxx "$(command -v "$cxx")"
    for tool_spec in \
        "cc1|$($cc -print-prog-name=cc1)" \
        "cc1plus|$($cxx -print-prog-name=cc1plus)" \
        "collect2|$($cxx -print-prog-name=collect2)" \
        "ld|$($cxx -print-prog-name=ld)" \
        "as|$($cc -print-prog-name=as)" \
        "ar|$($cc -print-prog-name=ar)" \
        "pkg-config|$(command -v pkg-config)"
    do
        IFS='|' read -r name path <<<"$tool_spec"
        if [[ "$path" != /* ]]; then
            path="$(command -v "$path")"
        fi
        package_record tool "$name" "$path"
    done
}

write_manifest() (
    local output="$1"
    local records="$2"
    local temporary
    temporary="$(mktemp "${TMPDIR:-/tmp}/emfont-fontworker-manifest.XXXXXX")"
    trap 'rm -f "${temporary:-}"' EXIT
    {
        printf 'schema\t%s\n' "$schema"
        LC_ALL=C sort -u "$records"
    } >"$temporary"
    validate_manifest "$temporary"
    install -m 0444 "$temporary" "$output"
)

validate_manifest() {
    local manifest="$1"
    [[ -f "$manifest" ]] || {
        printf 'package manifest is not a regular file: %s\n' "$manifest" >&2
        return 1
    }
    awk -F '\t' -v schema="$schema" '
        NR == 1 {
            if (NF != 2 || $1 != "schema" || $2 != schema) exit 1
            next
        }
        {
            if (NF != 6 || ($1 != "tool" && $1 != "runtime")) exit 1
            for (field = 2; field <= 6; field++) if ($field == "") exit 1
            if ($3 !~ /^\//) exit 1
            line = $0
            if (previous != "" && line <= previous) exit 1
            previous = line
            records++
        }
        END { if (NR < 2 || records < 1) exit 1 }
    ' "$manifest" || {
        printf 'invalid or non-canonical fontworker package manifest: %s\n' "$manifest" >&2
        return 1
    }
}

build_manifests() (
    local worker="$1"
    local build_manifest="$2"
    local runtime_manifest="$3"
    local runtime_records_file build_records_file
    [[ -x "$worker" ]] || {
        printf 'fontworker is not executable: %s\n' "$worker" >&2
        return 1
    }
    runtime_records_file="$(mktemp "${TMPDIR:-/tmp}/emfont-runtime-records.XXXXXX")"
    build_records_file="$(mktemp "${TMPDIR:-/tmp}/emfont-build-records.XXXXXX")"
    trap 'rm -f "${runtime_records_file:-}" "${build_records_file:-}"' EXIT

    runtime_records "$worker" >"$runtime_records_file"
    cp "$runtime_records_file" "$build_records_file"
    tool_records >>"$build_records_file"
    write_manifest "$runtime_manifest" "$runtime_records_file"
    write_manifest "$build_manifest" "$build_records_file"
)

runtime_manifest() (
    local worker="$1"
    local output="$2"
    local records
    [[ -x "$worker" ]] || {
        printf 'fontworker is not executable: %s\n' "$worker" >&2
        return 1
    }
    records="$(mktemp "${TMPDIR:-/tmp}/emfont-runtime-records.XXXXXX")"
    trap 'rm -f "${records:-}"' EXIT
    runtime_records "$worker" >"$records"
    write_manifest "$output" "$records"
)

for command_name in awk cp dpkg-query grep install ldd mktemp readlink sha256sum sort; do
    require_command "$command_name"
done

case "${1:-}" in
    build)
        (($# == 4)) || usage
        require_command go
        build_manifests "$2" "$3" "$4"
        ;;
    runtime)
        (($# == 3)) || usage
        runtime_manifest "$2" "$3"
        ;;
    validate)
        (($# == 2)) || usage
        validate_manifest "$2"
        ;;
    digest)
        (($# == 2)) || usage
        validate_manifest "$2"
        sha256sum "$2" | awk '{print $1}'
        ;;
    *)
        usage
        ;;
esac
