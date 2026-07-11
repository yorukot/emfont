#!/usr/bin/env bash
set -Eeuo pipefail

if (($# != 5)); then
    printf 'usage: %s ENVIRONMENT_JSON APPROVALS_JSON APPROVAL_COMMENT ACTOR OUTPUT\n' \
        "${0##*/}" >&2
    exit 2
fi

readonly environment_json="$1"
readonly approvals_json="$2"
readonly approval_comment="$3"
readonly actor="$4"
readonly output="$5"

for command_name in gh jq; do
    command -v "$command_name" >/dev/null 2>&1 || {
        printf 'required reviewer-policy command is unavailable: %s\n' \
            "$command_name" >&2
        exit 2
    }
done
[[ -s "$environment_json" && -s "$approvals_json" ]]
[[ "$approval_comment" =~ ^emfont-release-approval/v1[[:space:]] ]]
[[ "$actor" =~ ^[A-Za-z0-9-]{1,39}$ ]]
[[ -d "${output%/*}" ]]

jq --exit-status '
    (.id | type == "number" and . > 0 and floor == .) and
    (.name | type == "string" and length > 0) and
    (.updated_at | type == "string" and length > 0) and
    ([.protection_rules[] | select(.type == "required_reviewers")] as $rules |
      ($rules | length == 1) and
      ($rules[0].reviewers | type == "array" and length >= 1 and length <= 6) and
      ($rules[0].reviewers | all(
        (.type == "User" or .type == "Team") and
        (.reviewer.id | type == "number" and . > 0 and floor == .) and
        (.reviewer.node_id | type == "string" and length > 0)
      )))
' "$environment_json" >/dev/null
jq --exit-status 'type == "array"' "$approvals_json" >/dev/null

temporary_dir="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/emfont-reviewers.XXXXXX")"
cleanup() {
    rm -rf "$temporary_dir"
}
trap cleanup EXIT HUP INT TERM
readonly rules_ndjson="$temporary_dir/rules.ndjson"
install -m 0600 /dev/null "$rules_ndjson"

while IFS=$'\t' read -r reviewer_type reviewer_id reviewer_node identity api_url; do
    case "$reviewer_type" in
        User)
            [[ "$identity" =~ ^[A-Za-z0-9-]{1,39}$ ]]
            jq --null-input --compact-output --sort-keys \
                --arg type "$reviewer_type" \
                --argjson reviewer_id "$reviewer_id" \
                --arg reviewer_node "$reviewer_node" \
                --arg login "$identity" '
                {
                  type: $type,
                  reviewer: {
                    id: $reviewer_id,
                    node_id: $reviewer_node,
                    login: $login
                  },
                  authorized_users: [{
                    id: $reviewer_id,
                    node_id: $reviewer_node,
                    login: $login
                  }]
                }
            ' >>"$rules_ndjson"
            ;;
        Team)
            [[ "$identity" =~ ^[A-Za-z0-9_.-]{1,100}$ ]]
            [[ "$api_url" == https://api.github.com/* ]]
            team_path="${api_url#https://api.github.com/}"
            [[ "$team_path" =~ ^[A-Za-z0-9_./-]+$ ]]
            team_json="$temporary_dir/team-$reviewer_id.json"
            members_pages="$temporary_dir/team-$reviewer_id-members-pages.json"
            members="$temporary_dir/team-$reviewer_id-members.json"
            gh api \
                --method GET \
                --header 'Accept: application/vnd.github+json' \
                --header 'X-GitHub-Api-Version: 2022-11-28' \
                "$team_path" >"$team_json"
            jq --exit-status \
                --argjson reviewer_id "$reviewer_id" \
                --arg reviewer_node "$reviewer_node" \
                --arg slug "$identity" '
                .id == $reviewer_id and
                .node_id == $reviewer_node and
                .slug == $slug and
                (.organization.id | type == "number" and . > 0 and floor == .) and
                (.organization.node_id | type == "string" and length > 0) and
                (.organization.login | type == "string" and
                  test("^[A-Za-z0-9_.-]{1,39}$"))
            ' "$team_json" >/dev/null
            organization="$(jq --raw-output '.organization.login' "$team_json")"
            gh api \
                --method GET \
                --paginate \
                --slurp \
                --header 'Accept: application/vnd.github+json' \
                --header 'X-GitHub-Api-Version: 2022-11-28' \
                "orgs/$organization/teams/$identity/members?role=all&per_page=100" \
                >"$members_pages"
            jq --exit-status --sort-keys '
                (type == "array" and all(type == "array")) and
                (add | length >= 1 and length <= 5000) and
                (add | all(
                  (.id | type == "number" and . > 0 and floor == .) and
                  (.node_id | type == "string" and length > 0) and
                  (.login | type == "string" and test("^[A-Za-z0-9-]{1,39}$"))
                )) and
                ((add | map(.id) | unique | length) == (add | length))
            ' "$members_pages" >/dev/null
            jq --sort-keys '
                add
                | map({id, node_id, login})
                | sort_by(.id, (.login | ascii_downcase))
            ' "$members_pages" >"$members"
            jq --null-input --compact-output --sort-keys \
                --arg type "$reviewer_type" \
                --argjson reviewer_id "$reviewer_id" \
                --arg reviewer_node "$reviewer_node" \
                --arg slug "$identity" \
                --slurpfile team "$team_json" \
                --slurpfile members "$members" '
                {
                  type: $type,
                  reviewer: {
                    id: $reviewer_id,
                    node_id: $reviewer_node,
                    slug: $slug,
                    organization: {
                      id: $team[0].organization.id,
                      node_id: $team[0].organization.node_id,
                      login: $team[0].organization.login
                    }
                  },
                  authorized_users: $members[0]
                }
            ' >>"$rules_ndjson"
            ;;
        *)
            printf 'unsupported environment reviewer type: %s\n' \
                "$reviewer_type" >&2
            exit 1
            ;;
    esac
done < <(jq --exit-status --raw-output '
    [.protection_rules[] | select(.type == "required_reviewers")][0].reviewers[]
    | if .type == "User" then
        [.type, (.reviewer.id | tostring), .reviewer.node_id,
         .reviewer.login, ""]
      else
        [.type, (.reviewer.id | tostring), .reviewer.node_id,
         .reviewer.slug, .reviewer.url]
      end
    | @tsv
' "$environment_json")

rules_json="$temporary_dir/rules.json"
jq --slurp --sort-keys 'sort_by(.type, .reviewer.id)' \
    "$rules_ndjson" >"$rules_json"
expected_rule_count="$(jq '[.protection_rules[] |
    select(.type == "required_reviewers")][0].reviewers | length' \
    "$environment_json")"
[[ "$(jq 'length' "$rules_json")" == "$expected_rule_count" ]]

temporary_output="$(mktemp "${output}.tmp.XXXXXX")"
jq --null-input --sort-keys \
    --arg schema 'emfont.reviewer-policy-evidence/v1' \
    --arg approval_comment "$approval_comment" \
    --arg actor "$actor" \
    --slurpfile environment "$environment_json" \
    --slurpfile approvals "$approvals_json" \
    --slurpfile rules "$rules_json" '
    ($environment[0].id) as $environment_id |
    ([
      $approvals[0][]
      | select(
          .state == "approved" and
          .comment == $approval_comment and
          (.environments | type == "array" and any(
            .id == $environment_id and .name == $environment[0].name
          ))
        )
      | {
          state,
          user: {id: .user.id, node_id: .user.node_id, login: .user.login},
          comment,
          environments: ([.environments[] | {id, name}] | sort_by(.id))
        }
    ] | sort_by(.user.id)) as $matching_approvals |
    {
      schema: $schema,
      environment: {
        id: $environment_id,
        name: $environment[0].name,
        updated_at: $environment[0].updated_at
      },
      approval_comment: $approval_comment,
      actor: $actor,
      required_reviewers: $rules[0],
      approvals: $matching_approvals
    }
' >"$temporary_output"

jq --exit-status '
    .schema == "emfont.reviewer-policy-evidence/v1" and
    (.required_reviewers | type == "array" and length >= 1 and length <= 6) and
    (.approvals | type == "array" and length >= 1) and
    ((.approvals | map(.user.id) | unique | length) == (.approvals | length)) and
    (. as $evidence | .approvals | all(
      (.user.id | type == "number" and . > 0 and floor == .) and
      (.user.node_id | type == "string" and length > 0) and
      (.user.login | type == "string" and test("^[A-Za-z0-9-]{1,39}$")) and
      ((.user.login | ascii_downcase) != ($evidence.actor | ascii_downcase)) and
      (. as $approval | $evidence.required_reviewers | any(
        .authorized_users | any(
          .id == $approval.user.id and
          .node_id == $approval.user.node_id and
          ((.login | ascii_downcase) == ($approval.user.login | ascii_downcase))
        )
      ))
    ))
' "$temporary_output" >/dev/null || {
    rm -f "$temporary_output"
    printf 'environment approval identity is not authorized by captured reviewer policy\n' >&2
    exit 1
}
mv -f "$temporary_output" "$output"
