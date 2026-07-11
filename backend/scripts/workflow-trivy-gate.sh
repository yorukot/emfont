#!/usr/bin/env bash
set -Eeuo pipefail

if (($# != 2)); then
    printf 'usage: %s FULL_REPORT OUTPUT_REPORT\n' "${0##*/}" >&2
    exit 2
fi

readonly full_report="$1"
readonly output_report="$2"

command -v jq >/dev/null 2>&1 || {
    printf 'jq is required to validate Trivy evidence\n' >&2
    exit 2
}
command -v sha256sum >/dev/null 2>&1 || {
    printf 'sha256sum is required to validate Trivy evidence\n' >&2
    exit 2
}
[[ "$full_report" != "$output_report" ]] || {
    printf 'Trivy input and output reports must be different files\n' >&2
    exit 2
}
[[ -s "$full_report" ]] || {
    printf 'Trivy full report is missing or empty: %s\n' "$full_report" >&2
    exit 1
}
[[ -d "${output_report%/*}" ]] || {
    printf 'Trivy output directory does not exist: %s\n' \
        "${output_report%/*}" >&2
    exit 2
}

jq --exit-status '
    .SchemaVersion == 2 and
    (.Results | type == "array") and
    (.Results | all(
        type == "object" and
        ((.Vulnerabilities // []) | type == "array") and
        ((.Vulnerabilities // []) | all(
            type == "object" and
            (.Severity | type == "string") and
            ((.FixedVersion // "") | type == "string")
        ))
    ))
' "$full_report" >/dev/null || {
    printf 'Trivy full report has an unsupported or malformed schema: %s\n' \
        "$full_report" >&2
    exit 1
}

source_sha256="sha256:$(sha256sum "$full_report" | awk '{print $1}')"
readonly source_sha256
temporary_report="$(mktemp "${output_report}.tmp.XXXXXX")"
cleanup() {
    rm -f "$temporary_report"
}
trap cleanup EXIT HUP INT TERM

jq --sort-keys \
    --arg schema 'emfont.trivy-fixable-high-critical/v1' \
    --arg source_sha256 "$source_sha256" '
    {
        schema: $schema,
        source_report_sha256: $source_sha256,
        findings: ([
            .Results[] as $result
            | ($result.Vulnerabilities // [])[]
            | select(
                (.Severity == "HIGH" or .Severity == "CRITICAL") and
                ((.FixedVersion // "") != "")
            )
            | {
                vulnerability_id: .VulnerabilityID,
                target: $result.Target,
                class: ($result.Class // ""),
                type: ($result.Type // ""),
                package: .PkgName,
                installed_version: .InstalledVersion,
                fixed_version: .FixedVersion,
                status: (.Status // ""),
                severity: .Severity
            }
        ] | unique_by([
            .vulnerability_id,
            .target,
            .class,
            .type,
            .package,
            .installed_version,
            .fixed_version,
            .status,
            .severity
        ]) | sort_by([
            .vulnerability_id,
            .target,
            .class,
            .type,
            .package,
            .installed_version,
            .fixed_version,
            .status,
            .severity
        ]))
    }
' "$full_report" >"$temporary_report"
mv -f "$temporary_report" "$output_report"

finding_count="$(jq --exit-status '.findings | length' "$output_report")"
readonly finding_count
[[ "$finding_count" =~ ^[0-9]+$ ]]
if ((finding_count > 0)); then
    printf 'Trivy found %s fixable HIGH/CRITICAL vulnerabilities in %s:\n' \
        "$finding_count" "$full_report" >&2
    jq --raw-output \
        '.findings[] | "\(.severity) \(.vulnerability_id) " +
            "\(.package) \(.installed_version) -> \(.fixed_version)"' \
        "$output_report" >&2
    exit 1
fi
