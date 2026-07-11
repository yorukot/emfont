#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
backend_dir="$(cd -- "$script_dir/../.." && pwd)"
readonly backend_dir
readonly image="${1:-emfont-postgres:16.14-hardened}"
readonly platform="${2:-linux/amd64}"
readonly skip_build="${EMFONT_VERIFY_SKIP_BUILD:-false}"
readonly buildx_builder="${BUILDX_BUILDER:-}"
readonly trivy_image="${TRIVY_IMAGE:-aquasec/trivy@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f}"
suffix="$$-$(date +%s)"
readonly suffix
readonly container="emfont-postgres-verify-${suffix}"
readonly volume="emfont-postgres-verify-${suffix}"
readonly postgres_user='emfont_verify'
readonly postgres_password='emfont-verify-password'
readonly postgres_db='emfont_verify'

active_container=''
secret_file=''

cleanup() {
	if [[ -n "$active_container" ]]; then
		docker rm --force "$active_container" >/dev/null 2>&1 || true
	fi
	docker volume rm --force "$volume" >/dev/null 2>&1 || true
	if [[ -n "$secret_file" ]]; then
		rm --force "$secret_file"
	fi
}
trap cleanup EXIT

wait_healthy() {
	local name="$1"
	local status

	for _ in {1..60}; do
		status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$name")"
		case "$status" in
			healthy)
				return 0
				;;
			unhealthy|exited|dead)
				docker logs "$name" >&2
				printf >&2 'container %s reached terminal status %s\n' "$name" "$status"
				return 1
				;;
		esac
		sleep 1
	done

	docker logs "$name" >&2
	printf >&2 'container %s did not become healthy\n' "$name"
	return 1
}

start_postgres() {
	docker run --detach \
		--platform "$platform" \
		--name "$container" \
		--security-opt no-new-privileges:true \
		--env "POSTGRES_USER=$postgres_user" \
		--env 'POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password' \
		--env "POSTGRES_DB=$postgres_db" \
		--env 'POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256' \
		--mount "type=bind,source=$secret_file,target=/run/secrets/postgres_password,readonly" \
		--mount "type=volume,source=$volume,target=/var/lib/postgresql/data" \
		--health-cmd "PGPASSWORD=\"\$(cat /run/secrets/postgres_password)\" psql --no-psqlrc --host 127.0.0.1 --username=$postgres_user --dbname=$postgres_db --quiet --tuples-only --no-align --command 'SELECT 1' | grep --quiet --line-regexp '1'" \
		--health-interval 1s \
		--health-timeout 3s \
		--health-retries 30 \
		"$image" >/dev/null
	active_container="$container"
	wait_healthy "$container"
}

case "$skip_build" in
	false)
		buildx_args=()
		if [[ -n "$buildx_builder" ]]; then
			buildx_args+=(--builder "$buildx_builder")
		fi
		docker buildx build \
			"${buildx_args[@]}" \
			--pull \
			--platform "$platform" \
			--output type=docker,rewrite-timestamp=true \
			--tag "$image" \
			--file "$script_dir/Dockerfile" \
			"$backend_dir"
		;;
	true)
		[[ "$image" =~ @sha256:[0-9a-f]{64}$ ]]
		docker pull --platform "$platform" "$image"
		;;
	*)
		printf >&2 'EMFONT_VERIFY_SKIP_BUILD must be true or false\n'
		exit 2
		;;
esac

umask 077
secret_file="$(mktemp "${TMPDIR:-/tmp}/emfont-postgres-verify.XXXXXX")"
printf '%s\n' "$postgres_password" > "$secret_file"
docker volume create "$volume" >/dev/null
start_postgres

expected_uid="$(docker exec "$container" id -u postgres)"
expected_gid="$(docker exec "$container" id -g postgres)"
actual_uid="$(docker exec "$container" awk '/^Uid:/ { print $2; exit }' /proc/1/status)"
actual_gid="$(docker exec "$container" awk '/^Gid:/ { print $2; exit }' /proc/1/status)"
no_new_privileges="$(docker exec "$container" awk '/^NoNewPrivs:/ { print $2; exit }' /proc/1/status)"
effective_capabilities="$(docker exec "$container" awk '/^CapEff:/ { print $2; exit }' /proc/1/status)"
permitted_capabilities="$(docker exec "$container" awk '/^CapPrm:/ { print $2; exit }' /proc/1/status)"
ambient_capabilities="$(docker exec "$container" awk '/^CapAmb:/ { print $2; exit }' /proc/1/status)"
[[ "$actual_uid" == "$expected_uid" ]]
[[ "$actual_gid" == "$expected_gid" ]]
[[ "$no_new_privileges" == '1' ]]
[[ "$effective_capabilities" == '0000000000000000' ]]
[[ "$permitted_capabilities" == '0000000000000000' ]]
[[ "$ambient_capabilities" == '0000000000000000' ]]

docker exec "$container" /bin/sh -ec '
	test ! -e /usr/local/bin/gosu
	test -x /usr/bin/setpriv
	test -x /opt/emfont/scripts/load-secrets.sh
	test -x /opt/emfont/scripts/postgres-permissions.sh
	test -s "$PGDATA/PG_VERSION"
	test "$(stat -c %u:%g "$PGDATA")" = "$(id -u postgres):$(id -g postgres)"
	test "$(postgres --version)" = "postgres (PostgreSQL) 16.14 (Debian 16.14-1.pgdg12+1)"
'

docker exec \
	--env "PGPASSWORD=$postgres_password" \
	"$container" \
	psql --no-psqlrc --set ON_ERROR_STOP=1 \
		--username "$postgres_user" \
		--dbname "$postgres_db" \
		--command 'CREATE TABLE hardened_image_smoke (id integer PRIMARY KEY, value text NOT NULL);' \
		--command "INSERT INTO hardened_image_smoke (id, value) VALUES (1, 'persisted');" >/dev/null

sql_value="$(docker exec \
	--env "PGPASSWORD=$postgres_password" \
	"$container" \
	psql --no-psqlrc --tuples-only --no-align --quiet \
		--username "$postgres_user" \
		--dbname "$postgres_db" \
		--command 'SELECT value FROM hardened_image_smoke WHERE id = 1;')"
[[ "$sql_value" == 'persisted' ]]

docker rm --force "$container" >/dev/null
active_container=''
start_postgres

persisted_value="$(docker exec \
	--env "PGPASSWORD=$postgres_password" \
	"$container" \
	psql --no-psqlrc --tuples-only --no-align --quiet \
		--username "$postgres_user" \
		--dbname "$postgres_db" \
		--command 'SELECT value FROM hardened_image_smoke WHERE id = 1;')"
[[ "$persisted_value" == 'persisted' ]]

mkdir -p "$HOME/.cache/trivy"
printf 'Running blocking scan for fixable HIGH/CRITICAL vulnerabilities\n'
docker run --rm \
	--volume /var/run/docker.sock:/var/run/docker.sock \
	--volume "$HOME/.cache/trivy:/root/.cache/" \
	"$trivy_image" image \
	--exit-code 1 \
	--ignore-unfixed \
	--severity HIGH,CRITICAL \
	--scanners vuln \
	"$image"

printf 'Running non-blocking full HIGH/CRITICAL report, including unfixed findings\n'
docker run --rm \
	--volume /var/run/docker.sock:/var/run/docker.sock \
	--volume "$HOME/.cache/trivy:/root/.cache/" \
	"$trivy_image" image \
	--exit-code 0 \
	--severity HIGH,CRITICAL \
	--scanners vuln \
	"$image"

printf 'image_id=%s\n' "$(docker image inspect --format '{{.Id}}' "$image")"
printf 'runtime_smoke=pass\n'
printf 'trivy_fixable_high_critical=0\n'
printf 'trivy_total_high_critical=review_full_report_above\n'
