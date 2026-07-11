#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    printf 'usage: %s create RELEASE_DIR SOURCE_COMPOSE IMAGES_ENV\n' \
        "${0##*/}" >&2
    printf '       %s verify RELEASE_DIR IMAGES_ENV\n' "${0##*/}" >&2
    exit 2
}

(($# >= 1)) || usage
readonly mode="$1"
shift

case "$mode" in
    create)
        (($# == 3)) || usage
        readonly release_dir="$1"
        readonly source_compose="$2"
        readonly images_env="$3"
        ;;
    verify)
        (($# == 2)) || usage
        readonly release_dir="$1"
        readonly images_env="$2"
        readonly source_compose="$release_dir/docker-compose.backend.yml"
        ;;
    *) usage ;;
esac

readonly contract_env="$release_dir/compose-contract.env"
readonly contract_config="$release_dir/compose-config.json"
readonly release_compose="$release_dir/docker-compose.backend.yml"

for command_name in docker jq sha256sum; do
    command -v "$command_name" >/dev/null 2>&1 || {
        printf 'required Compose contract command is unavailable: %s\n' \
            "$command_name" >&2
        exit 2
    }
done
[[ -s "$images_env" ]] || {
    printf 'release images manifest is missing or empty: %s\n' "$images_env" >&2
    exit 1
}

expected_image_keys=$'backend\nminio\nminio_mc\npostgres'
actual_image_keys="$(awk -F= '
    /^[a-z][a-z0-9_]*=/ { print $1; next }
    { exit 2 }
' "$images_env" | LC_ALL=C sort)"
[[ "$actual_image_keys" == "$expected_image_keys" ]]
[[ -z "$(printf '%s\n' "$actual_image_keys" | uniq --repeated)" ]]

image_value() {
    local key="$1"
    local values=()
    mapfile -t values < <(awk -F= -v key="$key" '$1 == key {
        sub(/^[^=]*=/, "")
        print
    }' "$images_env")
    ((${#values[@]} == 1))
    [[ "${values[0]}" =~ ^ghcr\.io/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$ ]]
    printf '%s' "${values[0]}"
}

backend_ref="$(image_value backend)"
postgres_ref="$(image_value postgres)"
minio_ref="$(image_value minio)"
minio_mc_ref="$(image_value minio_mc)"
readonly backend_ref postgres_ref minio_ref minio_mc_ref

render_contract() {
    local output="$1"
    local temporary
    temporary="$(mktemp "${output}.tmp.XXXXXX")"
    if ! docker compose \
        --env-file "$contract_env" \
        -f "$release_compose" \
        --profile maintenance \
        config --format json | jq --sort-keys '.' >"$temporary"
    then
        rm -f "$temporary"
        return 1
    fi
    mv -f "$temporary" "$output"
}

verify_config() {
    local config="$1"
    jq --exit-status \
        --arg backend "$backend_ref" \
        --arg postgres "$postgres_ref" \
        --arg minio "$minio_ref" \
        --arg minio_mc "$minio_mc_ref" '
        .name == "emfont-release-contract" and
        ([.services[].image] | unique | sort) ==
          ([$backend, $postgres, $minio, $minio_mc] | sort) and
        (.services.controller.image == $backend) and
        (.services.migrate.image == $backend) and
        (.services.fontcleanup.image == $backend) and
        (.services.postgres.image == $postgres) and
        (.services.minio.image == $minio) and
        (.services["minio-init"].image == $minio_mc) and
        ((.services.minio | has("ports")) | not) and
        ((.services.postgres | has("ports")) | not) and
        (.services.minio.expose == ["9000"]) and
        (.services.minio.networks | keys == ["object-store"]) and
        (.services["minio-init"].networks | keys == ["object-store"]) and
        (.networks["object-store"].internal == true) and
        (.networks["object-store"].attachable == true) and
        (.services.postgres.read_only == true) and
        (.services.minio.read_only == true) and
        ((.services.postgres.cap_drop | index("ALL")) != null) and
        ((.services.postgres.cap_add | sort) ==
          ["CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"]) and
        ((.services.controller.cap_drop | index("ALL")) != null) and
        ((.services.controller.cap_add | sort) == ["KILL", "SETGID", "SETUID"]) and
        (.services.controller.environment.EMFONT_ENV == "production") and
        (.services.controller.environment.EMFONT_MINIO_PUBLIC_BASE_URL ==
          "https://objects.release.invalid/fonts") and
        (.services.controller.environment.EMFONT_RATE_LIMIT_ENABLED == "true") and
        (.services.controller.environment.EMFONT_CORS_ALLOWED_ORIGINS != "*")
    ' "$config" >/dev/null
}

if [[ "$mode" == create ]]; then
    [[ -s "$source_compose" ]]
    install -d -m 0700 "$release_dir"
    for output in "$release_compose" "$contract_env" "$contract_config"; do
        [[ ! -e "$output" ]] || {
            printf 'refusing to overwrite Compose release contract file: %s\n' \
                "$output" >&2
            exit 1
        }
    done
    install -m 0444 "$source_compose" "$release_compose"
    cat >"$contract_env" <<EOF
COMPOSE_PROJECT_NAME=emfont-release-contract
EMFONT_BACKEND_IMAGE_REPOSITORY=${backend_ref%@*}
EMFONT_BACKEND_IMAGE_SHA256=${backend_ref##*@sha256:}
EMFONT_BACKEND_PULL_POLICY=never
EMFONT_POSTGRES_IMAGE_REPOSITORY=${postgres_ref%@*}
EMFONT_POSTGRES_IMAGE_SHA256=${postgres_ref##*@sha256:}
EMFONT_MINIO_IMAGE_REPOSITORY=${minio_ref%@*}
EMFONT_MINIO_IMAGE_SHA256=${minio_ref##*@sha256:}
EMFONT_MINIO_MC_IMAGE_REPOSITORY=${minio_mc_ref%@*}
EMFONT_MINIO_MC_IMAGE_SHA256=${minio_mc_ref##*@sha256:}
EMFONT_INFRA_PULL_POLICY=never
EMFONT_VERSION=release-contract
EMFONT_POSTGRES_ADMIN_PASSWORD_FILE=/run/emfont-release-contract/postgres-admin-password
EMFONT_POSTGRES_APP_PASSWORD_FILE=/run/emfont-release-contract/postgres-app-password
EMFONT_MINIO_ROOT_USER_FILE=/run/emfont-release-contract/minio-root-user
EMFONT_MINIO_ROOT_PASSWORD_FILE=/run/emfont-release-contract/minio-root-password
EMFONT_MINIO_APP_ACCESS_KEY_FILE=/run/emfont-release-contract/minio-app-access-key
EMFONT_MINIO_APP_SECRET_KEY_FILE=/run/emfont-release-contract/minio-app-secret-key
EMFONT_MINIO_CLEANUP_ACCESS_KEY_FILE=/run/emfont-release-contract/minio-cleanup-access-key
EMFONT_MINIO_CLEANUP_SECRET_KEY_FILE=/run/emfont-release-contract/minio-cleanup-secret-key
EMFONT_METRICS_BEARER_TOKEN_FILE=/run/emfont-release-contract/metrics-bearer-token
EMFONT_MINIO_PUBLIC_BASE_URL=https://objects.release.invalid/fonts
EMFONT_CORS_ALLOWED_ORIGINS=https://app.release.invalid
EMFONT_TRUSTED_PROXY_CIDRS=192.0.2.0/24
EOF
    chmod 0444 "$contract_env"
    render_contract "$contract_config"
    chmod 0444 "$contract_config"
    verify_config "$contract_config"
else
    for required in "$release_compose" "$contract_env" "$contract_config"; do
        [[ -s "$required" ]] || {
            printf 'Compose release contract file is missing or empty: %s\n' \
                "$required" >&2
            exit 1
        }
    done
    verify_config "$contract_config"
    rerendered="$(mktemp "${TMPDIR:-/tmp}/emfont-compose-contract.XXXXXX")"
    cleanup() {
        rm -f "$rerendered"
    }
    trap cleanup EXIT HUP INT TERM
    render_contract "$rerendered"
    cmp --silent "$contract_config" "$rerendered" || {
        printf 'authenticated Compose config does not match a fresh render\n' >&2
        diff --unified "$contract_config" "$rerendered" >&2 || true
        exit 1
    }
fi

printf 'compose_file_sha256=sha256:%s\n' \
    "$(sha256sum "$release_compose" | awk '{print $1}')"
printf 'compose_contract_env_sha256=sha256:%s\n' \
    "$(sha256sum "$contract_env" | awk '{print $1}')"
printf 'compose_config_sha256=sha256:%s\n' \
    "$(sha256sum "$contract_config" | awk '{print $1}')"
printf 'compose_version=%s\n' "$(docker compose version --short)"
