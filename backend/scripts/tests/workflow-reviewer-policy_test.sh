#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly repo_dir
readonly verifier="$repo_dir/backend/scripts/workflow-reviewer-policy.sh"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/emfont-reviewer-policy.XXXXXX")"
cleanup() {
    rm -rf "$temporary_dir"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$temporary_dir/bin"
cat >"$temporary_dir/bin/gh" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
case "${*: -1}" in
  organizations/17/team/release-reviewers)
    printf '%s\n' '{"id":73,"node_id":"TEAM_node","slug":"release-reviewers","organization":{"id":17,"node_id":"ORG_node","login":"emfont-org"}}'
    ;;
  orgs/emfont-org/teams/release-reviewers/members?role=all\&per_page=100)
    printf '%s\n' '[[{"id":202,"node_id":"USER_team","login":"team-reviewer"}]]'
    ;;
  *)
    printf 'unexpected gh fixture request: %s\n' "${*: -1}" >&2
    exit 1
    ;;
esac
SH
chmod 0755 "$temporary_dir/bin/gh"

cat >"$temporary_dir/environment.json" <<'JSON'
{
  "id": 41,
  "name": "backend-production",
  "updated_at": "2026-07-11T00:00:00Z",
  "protection_rules": [{
    "type": "required_reviewers",
    "reviewers": [
      {
        "type": "User",
        "reviewer": {"id": 101, "node_id": "USER_direct", "login": "direct-reviewer"}
      },
      {
        "type": "Team",
        "reviewer": {
          "id": 73,
          "node_id": "TEAM_node",
          "slug": "release-reviewers",
          "url": "https://api.github.com/organizations/17/team/release-reviewers"
        }
      }
    ]
  }]
}
JSON
readonly comment='emfont-release-approval/v1 source_sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa run_attempt=2 cve_acceptance_sha256=none'
cat >"$temporary_dir/approvals.json" <<JSON
[
  {
    "state": "approved",
    "user": {"id": 202, "node_id": "USER_team", "login": "team-reviewer"},
    "comment": "$comment",
    "environments": [{"id": 41, "name": "backend-production"}]
  }
]
JSON

PATH="$temporary_dir/bin:$PATH" bash "$verifier" \
    "$temporary_dir/environment.json" \
    "$temporary_dir/approvals.json" \
    "$comment" \
    release-actor \
    "$temporary_dir/evidence.json"
jq --exit-status '
    .schema == "emfont.reviewer-policy-evidence/v1" and
    (.required_reviewers | length == 2) and
    (.required_reviewers[] | select(.type == "Team") |
      .reviewer.id == 73 and
      .reviewer.organization.id == 17 and
      .authorized_users == [{
        id: 202,
        login: "team-reviewer",
        node_id: "USER_team"
      }]) and
    .approvals[0].user.id == 202
' "$temporary_dir/evidence.json" >/dev/null

jq '.[0].user.id = 999 | .[0].user.node_id = "USER_untrusted" |
    .[0].user.login = "untrusted"' \
    "$temporary_dir/approvals.json" >"$temporary_dir/untrusted-approvals.json"
if PATH="$temporary_dir/bin:$PATH" bash "$verifier" \
    "$temporary_dir/environment.json" \
    "$temporary_dir/untrusted-approvals.json" \
    "$comment" \
    release-actor \
    "$temporary_dir/untrusted-evidence.json" >/dev/null 2>&1
then
    printf 'Reviewer policy accepted an unauthorized immutable user ID\n' >&2
    exit 1
fi

printf 'Workflow reviewer policy checks passed\n'
