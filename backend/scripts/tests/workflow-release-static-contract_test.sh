#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly repo_dir
readonly ci="$repo_dir/.github/workflows/backend.yml"
readonly release="$repo_dir/.github/workflows/backend-release.yml"

command -v rg >/dev/null 2>&1 || {
    printf 'workflow static contract requires ripgrep\n' >&2
    exit 2
}

require_literal() {
    local file="$1"
    local value="$2"
    rg --fixed-strings --quiet -- "$value" "$file" || {
        printf 'workflow contract is missing %q in %s\n' "$value" "$file" >&2
        exit 1
    }
}

reject_literal() {
    local file="$1"
    local value="$2"
    if rg --fixed-strings --quiet -- "$value" "$file"; then
        printf 'workflow contract contains forbidden %q in %s\n' \
            "$value" "$file" >&2
        exit 1
    fi
}

reject_literal "$ci" 'setup-buildx-action@'
reject_literal "$release" 'setup-buildx-action@'
reject_literal "$ci" '--ignore-unfixed'
reject_literal "$release" '--ignore-unfixed'
reject_literal "$release" '--arg token'
reject_literal "$ci" 'run: backend/scripts/tests/fontworker-package-manifest_test.sh'
require_literal "$ci" 'run: scripts/tests/fontworker-package-manifest_test.sh'
require_literal "$ci" 'https://github.com/jqlang/jq/releases/download/jq-1.6/jq-linux64'
require_literal "$ci" 'af986793a515d500ab2d35f8d2aecd656e764504b789b66d7e1a0b727a124c44'
require_literal "$release" 'Match independent rebuild tooling to publisher'
require_literal "$release" 'Reverify authenticated release tooling after approval'
[[ "$(rg --fixed-strings --count \
    'backend/scripts/workflow-setup-buildx.sh' "$release")" == 3 ]]

for artifact_file in \
    compose-config.json \
    compose-contract.env \
    docker-compose.backend.yml \
    verify-compose-release.sh
do
    require_literal "$release" "release-manifest/$artifact_file"
done
require_literal "$release" 'compose_verification_required_for_deploy=true'
require_literal "$release" 'compose_verification_required_for_rollback=true'
require_literal "$release" 'bash verify-compose-release.sh verify . images.env'

[[ "$(rg --fixed-strings --count \
    'backend/scripts/workflow-reviewer-policy.sh' "$release")" == 2 ]]
require_literal "$release" 'reviewer_policy_sha256'
require_literal "$release" 'emfont.reviewer-policy-evidence/v1'
require_literal "$release" 'authorized_users'

require_literal "$ci" 'TestFontSchemaReadyMigration10Integration'
require_literal "$ci" '000010_add_bounded_font_terminal_failure_cache.sql'
require_literal "$ci" "readonly expected_versions='1,2,3,4,5,6,7,8,9,10'"
require_literal "$ci" 'Verify final controller readiness across migration 9 and 10'

printf 'Workflow release static contract checks passed\n'
