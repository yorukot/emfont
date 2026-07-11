#!/bin/sh
set -eu

SERVER_IMAGE=${SERVER_IMAGE:-emfont/minio-server:hardened-test}
MC_IMAGE=${MC_IMAGE:-emfont/minio-mc:hardened-test}
PLATFORM=${PLATFORM:-linux/amd64}

suffix="$$"
network="emfont-minio-smoke-$suffix"
server="emfont-minio-smoke-server-$suffix"
volume="emfont-minio-smoke-data-$suffix"
secrets_dir=$(mktemp -d /tmp/emfont-minio-smoke.XXXXXX)

cleanup() {
    docker rm -f "$server" >/dev/null 2>&1 || true
    docker volume rm "$volume" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
    rm -rf "$secrets_dir"
}
trap cleanup EXIT HUP INT TERM

printf '%s' 'emfont-smoke-root' >"$secrets_dir/root-user"
printf '%s' 'emfont-smoke-root-secret-0123456789' >"$secrets_dir/root-password"
printf '%s' 'emfont-smoke-app' >"$secrets_dir/app-user"
printf '%s' 'emfont-smoke-app-secret-0123456789' >"$secrets_dir/app-password"
printf '%s' 'emfont-smoke-cleanup' >"$secrets_dir/cleanup-user"
printf '%s' 'emfont-smoke-cleanup-secret-0123456789' >"$secrets_dir/cleanup-password"
chmod 0600 "$secrets_dir"/*

docker network create "$network" >/dev/null
docker volume create "$volume" >/dev/null
docker run -d \
    --platform "$PLATFORM" \
    --name "$server" \
    --network "$network" \
    --network-alias minio \
    --read-only \
    --tmpfs /tmp:size=64m,mode=1777 \
    --cap-drop ALL \
    --cap-add SETUID \
    --cap-add SETGID \
    --security-opt no-new-privileges:true \
    --env MINIO_RUN_AS_UID=10001 \
    --env MINIO_RUN_AS_GID=10001 \
    --env MINIO_ROOT_USER_FILE=/run/secrets/root-user \
    --env MINIO_ROOT_PASSWORD_FILE=/run/secrets/root-password \
    --volume "$secrets_dir/root-user:/run/secrets/root-user:ro" \
    --volume "$secrets_dir/root-password:/run/secrets/root-password:ro" \
    --volume "$volume:/data" \
    "$SERVER_IMAGE" \
    server /data --address :9000 --console-address :9001 >/dev/null

attempt=0
until docker exec "$server" curl -fsS \
    http://127.0.0.1:9000/minio/health/ready >/dev/null 2>&1
do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 30 ]; then
        docker logs "$server" >&2
        printf 'smoke: MinIO did not become ready\n' >&2
        exit 1
    fi
    sleep 1
done

test "$(docker exec "$server" awk '/^Uid:/ { print $2; exit }' /proc/1/status)" = 10001
test "$(docker exec "$server" awk '/^Gid:/ { print $2; exit }' /proc/1/status)" = 10001
test "$(docker exec "$server" awk '/^NoNewPrivs:/ { print $2; exit }' /proc/1/status)" = 1
test "$(docker exec "$server" awk '/^CapEff:/ { print $2; exit }' /proc/1/status)" = 0000000000000000
test "$(docker exec "$server" stat -c '%u:%g:%a' /data)" = 10001:10001:750

docker run --rm \
    --platform "$PLATFORM" \
    --network "$network" \
    --user 10001:10001 \
    --entrypoint /bin/sh \
    --read-only \
    --tmpfs /tmp:size=32m,mode=1777 \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --env MC_CONFIG_DIR=/tmp/.mc \
    --env ROOT_USER=emfont-smoke-root \
    --env ROOT_PASSWORD=emfont-smoke-root-secret-0123456789 \
    "$MC_IMAGE" -ec '
        set -eu
        printf "%s\n%s\n" "$ROOT_USER" "$ROOT_PASSWORD" | \
            mc alias set root http://minio:9000 --api S3v4 --path on >/dev/null
        mc mb --ignore-existing root/emfont-smoke >/dev/null
        if mc version info root/emfont-smoke | grep -qi "versioning is enabled"; then
            printf "smoke: test bucket unexpectedly started with versioning enabled\n" >&2
            exit 1
        fi
        printf legacy-font-content >/tmp/legacy.ttf
	        mc cp \
	            --attr "Content-Type=font/ttf;Cache-Control=public,max-age=3600" \
	            --tags "family=Legacy&source=pre-versioning" \
	            /tmp/legacy.ttf \
	            root/emfont-smoke/original-fonts/Legacy/400.ttf >/dev/null
	        printf legacy-generated-content >/tmp/legacy-generated.woff2
	        mc cp --checksum SHA256 \
	            /tmp/legacy-generated.woff2 \
	            root/emfont-smoke/_generated/legacy.woff2 >/dev/null
    '

run_minio_init() {
    docker run --rm \
    --platform "$PLATFORM" \
    --network "$network" \
    --entrypoint /bin/sh \
    --read-only \
    --tmpfs /tmp:size=32m,mode=1777 \
    --cap-drop ALL \
    --cap-add SETUID \
    --cap-add SETGID \
    --security-opt no-new-privileges:true \
    --env EMFONT_RUN_AS_UID=10001 \
    --env EMFONT_RUN_AS_GID=10001 \
    --env MINIO_ROOT_USER_FILE=/run/secrets/root-user \
    --env MINIO_ROOT_PASSWORD_FILE=/run/secrets/root-password \
	    --env EMFONT_MINIO_ACCESS_KEY_FILE=/run/secrets/app-user \
	    --env EMFONT_MINIO_SECRET_KEY_FILE=/run/secrets/app-password \
	    --env EMFONT_MINIO_CLEANUP_ACCESS_KEY_FILE=/run/secrets/cleanup-user \
	    --env EMFONT_MINIO_CLEANUP_SECRET_KEY_FILE=/run/secrets/cleanup-password \
	    --env EMFONT_MINIO_BUCKET=emfont-smoke \
	    --env EMFONT_MINIO_POLICY_NAME=emfont-smoke-controller \
	    --env EMFONT_MINIO_CLEANUP_POLICY_NAME=emfont-smoke-cleanup \
    --env EMFONT_MINIO_NONCURRENT_EXPIRE_DAYS=7 \
    --env EMFONT_MINIO_BOOTSTRAP_ENDPOINT=minio:9000 \
    --volume "$secrets_dir/root-user:/run/secrets/root-user:ro" \
    --volume "$secrets_dir/root-password:/run/secrets/root-password:ro" \
	    --volume "$secrets_dir/app-user:/run/secrets/app-user:ro" \
	    --volume "$secrets_dir/app-password:/run/secrets/app-password:ro" \
	    --volume "$secrets_dir/cleanup-user:/run/secrets/cleanup-user:ro" \
	    --volume "$secrets_dir/cleanup-password:/run/secrets/cleanup-password:ro" \
    "$MC_IMAGE" \
    /opt/emfont/scripts/load-secrets.sh \
    /bin/sh /opt/emfont/scripts/minio-init.sh
}

run_minio_init | tee "$secrets_dir/minio-init-first.log"
grep -F \
	    'object-version-backfill: scanned=2 null_versions=2 rewritten=2 already_versioned=0' \
    "$secrets_dir/minio-init-first.log" >/dev/null

run_minio_init | tee "$secrets_dir/minio-init-second.log"
grep -F \
	    'object-version-backfill: scanned=2 null_versions=0 rewritten=0 already_versioned=2' \
    "$secrets_dir/minio-init-second.log" >/dev/null

run_root_admin() {
    action=$1
    docker run --rm \
        --platform "$PLATFORM" \
        --network "$network" \
        --user 10001:10001 \
        --entrypoint /bin/sh \
        --read-only \
        --tmpfs /tmp:size=32m,mode=1777 \
        --cap-drop ALL \
        --security-opt no-new-privileges:true \
        --env MC_CONFIG_DIR=/tmp/.mc \
        --env ROOT_USER=emfont-smoke-root \
	        --env ROOT_PASSWORD=emfont-smoke-root-secret-0123456789 \
	        --env APP_USER=emfont-smoke-app \
	        --env CLEANUP_USER=emfont-smoke-cleanup \
        --env ACTION="$action" \
        "$MC_IMAGE" -ec '
            set -eu
            printf "%s\n%s\n" "$ROOT_USER" "$ROOT_PASSWORD" | \
                mc alias set root http://minio:9000 --api S3v4 --path on >/dev/null
	            case "$ACTION" in
	                set-anonymous-download)
	                    mc anonymous set download root/emfont-smoke >/dev/null
	                    ;;
	                attach-extra-policy)
                    mc admin policy attach root readwrite --user "$APP_USER" >/dev/null
                    ;;
                detach-extra-policy)
                    mc admin policy detach root readwrite --user "$APP_USER" >/dev/null
                    ;;
                add-group)
                    mc admin group add root emfont-smoke-extra "$APP_USER" >/dev/null
                    ;;
                remove-group)
                    mc admin group remove root emfont-smoke-extra "$APP_USER" >/dev/null
                    mc admin group remove root emfont-smoke-extra >/dev/null
                    ;;
                attach-extra-cleanup-policy)
                    mc admin policy attach root readwrite --user "$CLEANUP_USER" >/dev/null
                    ;;
                detach-extra-cleanup-policy)
                    mc admin policy detach root readwrite --user "$CLEANUP_USER" >/dev/null
                    ;;
                *) exit 2 ;;
            esac
        '
}

run_root_admin set-anonymous-download
run_minio_init >/dev/null

assert_init_rejects_identity() {
    fixture=$1
    output="$secrets_dir/minio-init-$fixture.log"
    if run_minio_init >"$output" 2>&1; then
        printf 'smoke: initializer accepted %s\n' "$fixture" >&2
        exit 1
    fi
    grep -F 'MinIO application user has unexpected policy or group membership' \
        "$output" >/dev/null
    if grep -F 'emfont-smoke-app' "$output" >/dev/null; then
        printf 'smoke: initializer leaked the application access-key identifier\n' >&2
        exit 1
    fi
}

run_root_admin attach-extra-policy
assert_init_rejects_identity extra-policy
run_root_admin detach-extra-policy
run_minio_init >/dev/null

run_root_admin add-group
assert_init_rejects_identity group-membership
run_root_admin remove-group
run_minio_init >/dev/null

run_root_admin attach-extra-cleanup-policy
if run_minio_init >"$secrets_dir/minio-init-cleanup-extra-policy.log" 2>&1; then
    printf 'smoke: initializer accepted cleanup extra-policy membership\n' >&2
    exit 1
fi
grep -F 'MinIO cleanup user has unexpected policy or group membership' \
    "$secrets_dir/minio-init-cleanup-extra-policy.log" >/dev/null
run_root_admin detach-extra-cleanup-policy
run_minio_init >/dev/null

docker run --rm \
    --platform "$PLATFORM" \
    --network "$network" \
    --user 10001:10001 \
    --entrypoint /bin/sh \
    --read-only \
    --tmpfs /tmp:size=32m,mode=1777 \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --env MC_CONFIG_DIR=/tmp/.mc \
    --env ROOT_USER=emfont-smoke-root \
    --env ROOT_PASSWORD=emfont-smoke-root-secret-0123456789 \
	    --env APP_USER=emfont-smoke-app \
	    --env APP_PASSWORD=emfont-smoke-app-secret-0123456789 \
	    --env CLEANUP_USER=emfont-smoke-cleanup \
	    --env CLEANUP_PASSWORD=emfont-smoke-cleanup-secret-0123456789 \
	    --env EMFONT_MINIO_ACCESS_KEY=emfont-smoke-app \
	    --env EMFONT_MINIO_CLEANUP_ACCESS_KEY=emfont-smoke-cleanup \
    "$MC_IMAGE" -ec '
        set -eu
        printf "%s\n%s\n" "$ROOT_USER" "$ROOT_PASSWORD" | \
            mc alias set root http://minio:9000 --api S3v4 --path on >/dev/null
	        mc version info root/emfont-smoke | grep -qi "versioning is enabled"
	        mc admin policy info root emfont-smoke-controller --json | \
	            minioinitcheck policy --policy emfont-smoke-controller --bucket emfont-smoke
	        mc admin policy info root emfont-smoke-cleanup --json | \
	            minioinitcheck policy --role cleanup --policy emfont-smoke-cleanup --bucket emfont-smoke
	        mc admin user info root "$APP_USER" --json | \
	            minioinitcheck user --policy emfont-smoke-controller
	        mc admin user info root "$CLEANUP_USER" --json | \
	            minioinitcheck user --role cleanup --policy emfont-smoke-cleanup
	        mc anonymous get root/emfont-smoke --json | \
	            minioinitcheck anonymous --target root/emfont-smoke
        mc ilm rule ls root/emfont-smoke | grep -q "_generated/"
	        test "$(mc cat root/emfont-smoke/original-fonts/Legacy/400.ttf)" = legacy-font-content
	        mc stat root/emfont-smoke/original-fonts/Legacy/400.ttf | grep -q "font/ttf"
	        source_version_id=$(mc stat --json root/emfont-smoke/original-fonts/Legacy/400.ttf | \
	            sed -n "s/.*\"versionID\":\"\([^\"]*\)\".*/\1/p")
	        test -n "$source_version_id"
	        mc tag list --json root/emfont-smoke/original-fonts/Legacy/400.ttf | \
	            grep -Fq "\"family\":\"Legacy\""

	        printf "%s\n%s\n" "$APP_USER" "$APP_PASSWORD" | \
	            mc alias set app http://minio:9000 --api S3v4 --path on >/dev/null
	        test "$(mc cat app/emfont-smoke/original-fonts/Legacy/400.ttf)" = legacy-font-content
	        test "$(mc cat --vid "$source_version_id" app/emfont-smoke/original-fonts/Legacy/400.ttf)" = legacy-font-content
        printf generated >/tmp/generated.woff2
	        mc cp /tmp/generated.woff2 app/emfont-smoke/_generated/generated.woff2 >/dev/null
	        mc stat app/emfont-smoke/_generated/generated.woff2 >/dev/null
	        if mc rm app/emfont-smoke/_generated/generated.woff2 >/dev/null 2>&1; then
	            printf "smoke: controller credentials deleted a generated object\n" >&2
	            exit 1
	        fi
        if mc rm app/emfont-smoke/original-fonts/Legacy/400.ttf >/dev/null 2>&1; then
            printf "smoke: app credentials deleted an original object\n" >&2
            exit 1
	        fi
	        mc stat root/emfont-smoke/original-fonts/Legacy/400.ttf >/dev/null

	        printf "%s\n%s\n" "$CLEANUP_USER" "$CLEANUP_PASSWORD" | \
	            mc alias set cleanup http://minio:9000 --api S3v4 --path on >/dev/null
	        test "$(mc cat cleanup/emfont-smoke/_generated/generated.woff2)" = generated
	        mc ls cleanup/emfont-smoke/_generated/ | grep -q generated.woff2
	        if mc cp /tmp/generated.woff2 cleanup/emfont-smoke/_generated/other.woff2 >/dev/null 2>&1; then
	            printf "smoke: cleanup credentials published an object\n" >&2
	            exit 1
	        fi
	        if mc cat cleanup/emfont-smoke/original-fonts/Legacy/400.ttf >/dev/null 2>&1; then
	            printf "smoke: cleanup credentials read an original object\n" >&2
	            exit 1
	        fi
	        if mc ls cleanup/emfont-smoke/original-fonts/ >/dev/null 2>&1; then
	            printf "smoke: cleanup credentials listed the original-object prefix\n" >&2
	            exit 1
	        fi

	        legacy_version_id=$(mc stat --json cleanup/emfont-smoke/_generated/legacy.woff2 | \
	            sed -n "s/.*\"versionID\":\"\([^\"]*\)\".*/\1/p")
	        test -n "$legacy_version_id"
	        mc rm cleanup/emfont-smoke/_generated/legacy.woff2 >/dev/null
	        mc rm --vid "$legacy_version_id" \
	            cleanup/emfont-smoke/_generated/legacy.woff2 >/dev/null
	        if mc stat cleanup/emfont-smoke/_generated/legacy.woff2 >/dev/null 2>&1; then
	            printf "smoke: cleanup tombstone did not remain current\n" >&2
	            exit 1
	        fi
	        test "$(mc cat --vid null cleanup/emfont-smoke/_generated/legacy.woff2)" = \
	            legacy-generated-content
	        if mc stat --vid "$legacy_version_id" \
	            cleanup/emfont-smoke/_generated/legacy.woff2 >/dev/null 2>&1; then
	            printf "smoke: cleanup did not remove the validated generated version\n" >&2
	            exit 1
	        fi
	        mc rm cleanup/emfont-smoke/_generated/generated.woff2 >/dev/null
    '

printf 'smoke: server, backfill, dual policies, lifecycle, and scoped principal access passed\n'
