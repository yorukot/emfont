#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
compose_file="$repo_dir/docker-compose.backend.yml"
temp_dir="$(mktemp -d /tmp/emfont-compose-security-test.XXXXXX)"
trap 'rm -rf "$temp_dir"' EXIT

for secret_name in \
    postgres-admin-password \
    postgres-app-password \
    minio-root-user \
    minio-root-password \
    minio-app-access-key \
    minio-app-secret-key \
    minio-cleanup-access-key \
    minio-cleanup-secret-key \
    metrics-bearer-token
do
    printf '%s' "test-$secret_name" >"$temp_dir/$secret_name"
    chmod 0600 "$temp_dir/$secret_name"
done

digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
env_file="$temp_dir/compose.env"
cat >"$env_file" <<EOF
EMFONT_BACKEND_IMAGE_REPOSITORY=registry.example.invalid/emfont/backend
EMFONT_BACKEND_IMAGE_SHA256=$digest
EMFONT_POSTGRES_IMAGE_REPOSITORY=registry.example.invalid/emfont/postgres
EMFONT_POSTGRES_IMAGE_SHA256=$digest
EMFONT_MINIO_IMAGE_REPOSITORY=registry.example.invalid/emfont/minio
EMFONT_MINIO_IMAGE_SHA256=$digest
EMFONT_MINIO_MC_IMAGE_REPOSITORY=registry.example.invalid/emfont/minio-mc
EMFONT_MINIO_MC_IMAGE_SHA256=$digest
EMFONT_VERSION=compose-security-test
EMFONT_CORS_ALLOWED_ORIGINS=https://fonts.example.invalid
EMFONT_TRUSTED_PROXY_CIDRS=192.0.2.0/24
EMFONT_RATE_LIMIT_ENABLED=false
EMFONT_POSTGRES_ADMIN_PASSWORD_FILE=$temp_dir/postgres-admin-password
EMFONT_POSTGRES_APP_PASSWORD_FILE=$temp_dir/postgres-app-password
EMFONT_MINIO_ROOT_USER_FILE=$temp_dir/minio-root-user
EMFONT_MINIO_ROOT_PASSWORD_FILE=$temp_dir/minio-root-password
EMFONT_MINIO_APP_ACCESS_KEY_FILE=$temp_dir/minio-app-access-key
EMFONT_MINIO_APP_SECRET_KEY_FILE=$temp_dir/minio-app-secret-key
EMFONT_MINIO_CLEANUP_ACCESS_KEY_FILE=$temp_dir/minio-cleanup-access-key
EMFONT_MINIO_CLEANUP_SECRET_KEY_FILE=$temp_dir/minio-cleanup-secret-key
EMFONT_METRICS_BEARER_TOKEN_FILE=$temp_dir/metrics-bearer-token
EOF

compose=(docker compose --env-file "$env_file" -f "$compose_file")
if EMFONT_MINIO_PUBLIC_BASE_URL='' "${compose[@]}" config --quiet >"$temp_dir/empty.out" 2>&1; then
    printf 'Compose accepted an empty production object gateway base\n' >&2
    exit 1
fi
grep -F 'EMFONT_MINIO_PUBLIC_BASE_URL' "$temp_dir/empty.out" >/dev/null

gateway=https://objects.example.invalid/fonts
rendered="$(EMFONT_MINIO_PUBLIC_BASE_URL="$gateway" "${compose[@]}" --profile maintenance config --format json)"
jq --exit-status --arg gateway "$gateway" '
  .services.controller.environment.EMFONT_ENV == "production" and
  .services.controller.environment.EMFONT_RATE_LIMIT_ENABLED == "true" and
  .services.controller.environment.EMFONT_MINIO_PUBLIC_BASE_URL == $gateway and
  .services.controller.entrypoint == ["/opt/emfont/scripts/load-secrets.sh"] and
  ((.services.controller.cap_add | sort) == ["KILL", "SETGID", "SETUID"]) and
  ((.services.controller.cap_drop | index("ALL")) != null) and
  .services.postgres.read_only == true and
  ((.services.postgres.cap_drop | index("ALL")) != null) and
  ((.services.postgres.cap_add | sort) == ["CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"]) and
  .networks["object-store"].internal == true and
  .networks["object-store"].attachable == true and
  (.services.minio.networks | keys == ["object-store"]) and
  (.services["minio-init"].networks | keys == ["object-store"]) and
  (.services.controller.networks | has("object-store")) and
  (.services.fontcleanup.networks | keys == ["database", "object-store"]) and
  .services.controller.environment.EMFONT_MINIO_ACCESS_KEY_FILE == "/run/secrets/minio_app_access_key" and
  .services.controller.environment.EMFONT_MINIO_SECRET_KEY_FILE == "/run/secrets/minio_app_secret_key" and
  .services.fontcleanup.environment.EMFONT_MINIO_ACCESS_KEY_FILE == "/run/secrets/minio_cleanup_access_key" and
  .services.fontcleanup.environment.EMFONT_MINIO_SECRET_KEY_FILE == "/run/secrets/minio_cleanup_secret_key" and
  (.services.controller.secrets | map(.source) | sort) ==
    ["metrics_bearer_token", "minio_app_access_key", "minio_app_secret_key", "postgres_app_password"] and
  (.services.fontcleanup.secrets | map(.source) | sort) ==
    ["minio_cleanup_access_key", "minio_cleanup_secret_key", "postgres_app_password"] and
  (.services["minio-init"].secrets | map(.source) | sort) ==
    ["minio_app_access_key", "minio_app_secret_key", "minio_cleanup_access_key", "minio_cleanup_secret_key", "minio_root_password", "minio_root_user"] and
  ((.services.minio | has("ports")) | not) and
  (.services.minio.expose == ["9000"])
' <<<"$rendered" >/dev/null

printf 'Compose security render checks passed\n'
