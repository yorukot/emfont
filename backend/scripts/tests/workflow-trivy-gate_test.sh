#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly repo_dir
readonly gate="$repo_dir/backend/scripts/workflow-trivy-gate.sh"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/emfont-trivy-gate.XXXXXX")"
cleanup() {
    rm -rf "$temporary_dir"
}
trap cleanup EXIT HUP INT TERM

cat >"$temporary_dir/accepted.json" <<'JSON'
{
  "SchemaVersion": 2,
  "Results": [{
    "Target": "rootfs",
    "Class": "os-pkgs",
    "Type": "debian",
    "Vulnerabilities": [
      {
        "VulnerabilityID": "CVE-TEST-UNFIXED",
        "PkgName": "unfixed-package",
        "InstalledVersion": "1",
        "FixedVersion": "",
        "Status": "affected",
        "Severity": "CRITICAL"
      },
      {
        "VulnerabilityID": "CVE-TEST-MEDIUM",
        "PkgName": "medium-package",
        "InstalledVersion": "1",
        "FixedVersion": "2",
        "Status": "fixed",
        "Severity": "MEDIUM"
      }
    ]
  }]
}
JSON
bash "$gate" \
    "$temporary_dir/accepted.json" \
    "$temporary_dir/accepted-gate.json"
jq --exit-status \
    --arg sha256 "sha256:$(sha256sum "$temporary_dir/accepted.json" | awk '{print $1}')" '
        .schema == "emfont.trivy-fixable-high-critical/v1" and
        .source_report_sha256 == $sha256 and
        .findings == []
    ' "$temporary_dir/accepted-gate.json" >/dev/null

cat >"$temporary_dir/rejected.json" <<'JSON'
{
  "SchemaVersion": 2,
  "Results": [{
    "Target": "application",
    "Class": "lang-pkgs",
    "Type": "gobinary",
    "Vulnerabilities": [{
      "VulnerabilityID": "CVE-TEST-FIXABLE",
      "PkgName": "fixable-package",
      "InstalledVersion": "1",
      "FixedVersion": "2",
      "Status": "fixed",
      "Severity": "HIGH"
    }]
  }]
}
JSON
if bash "$gate" \
    "$temporary_dir/rejected.json" \
    "$temporary_dir/rejected-gate.json" >/dev/null 2>&1
then
    printf 'Trivy gate accepted a fixable HIGH vulnerability\n' >&2
    exit 1
fi
jq --exit-status '
    .schema == "emfont.trivy-fixable-high-critical/v1" and
    (.findings | length == 1) and
    .findings[0].vulnerability_id == "CVE-TEST-FIXABLE" and
    .findings[0].fixed_version == "2"
' "$temporary_dir/rejected-gate.json" >/dev/null

printf '{"SchemaVersion":2}\n' >"$temporary_dir/malformed.json"
if bash "$gate" \
    "$temporary_dir/malformed.json" \
    "$temporary_dir/malformed-gate.json" >/dev/null 2>&1
then
    printf 'Trivy gate accepted a malformed report\n' >&2
    exit 1
fi
[[ ! -e "$temporary_dir/malformed-gate.json" ]]

if bash "$gate" \
    "$temporary_dir/missing.json" \
    "$temporary_dir/missing-gate.json" >/dev/null 2>&1
then
    printf 'Trivy gate accepted a missing report\n' >&2
    exit 1
fi

printf 'Workflow Trivy gate checks passed\n'
