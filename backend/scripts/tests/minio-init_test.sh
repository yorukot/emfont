#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
temp_dir=$(mktemp -d /tmp/emfont-minio-init-test.XXXXXX)
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

cat >"$temp_dir/mc" <<'SCRIPT'
#!/bin/sh
set -eu

case "$*" in
    *"$MINIO_ROOT_PASSWORD"* | *"$EMFONT_MINIO_SECRET_KEY"* | *"$EMFONT_MINIO_CLEANUP_SECRET_KEY"*)
        printf 'secret appeared in mc arguments\n' >&2
        exit 1
        ;;
    "alias set "*)
        IFS= read -r access_key
        IFS= read -r secret_key
        [ "$access_key" = "$MINIO_ROOT_USER" ]
        [ "$secret_key" = "$MINIO_ROOT_PASSWORD" ]
        printf '%s\n' alias >>"$FAKE_COMMAND_LOG"
        ;;
    "admin config get emfont-bootstrap identity_openid --json")
        printf '%s\n' identity-openid >>"$FAKE_COMMAND_LOG"
        printf '{"status":"success","config":[{"subSystem":"identity_openid","kv":[{"key":"enable","value":"%s"},{"key":"config_url","value":"%s"}]},{"subSystem":"identity_openid","target":"secondary","kv":[{"key":"enable","value":"%s"}]}]}\n' \
            "${FAKE_OPENID_ENABLE-off}" "${FAKE_OPENID_CONFIG_URL:-}" "${FAKE_OPENID_SECONDARY_ENABLE-off}"
        ;;
    "admin config get emfont-bootstrap identity_ldap --json")
        printf '%s\n' identity-ldap >>"$FAKE_COMMAND_LOG"
        printf '{"status":"success","config":[{"subSystem":"identity_ldap","kv":[{"key":"enable","value":"%s"},{"key":"server_addr","value":"%s"}]}]}\n' \
            "${FAKE_LDAP_ENABLE-off}" "${FAKE_LDAP_SERVER_ADDR:-}"
        ;;
    "mb --ignore-existing "*) printf 'bucket:%s\n' "$*" >>"$FAKE_COMMAND_LOG" ;;
    "anonymous set private emfont-bootstrap/emfont-test")
        printf '%s\n' anonymous-private >>"$FAKE_COMMAND_LOG"
        ;;
    "anonymous get emfont-bootstrap/emfont-test --json")
        printf '%s\n' anonymous-info >>"$FAKE_COMMAND_LOG"
        printf '{"operation":"get","status":"success","bucket":"emfont-bootstrap/emfont-test","permission":"%s"}\n' \
            "${FAKE_ANONYMOUS_PERMISSION:-private}"
        ;;
    "version enable "*)
        printf '%s\n' version-enable >>"$FAKE_COMMAND_LOG"
        : >"$FAKE_STATE_DIR/version-enabled"
        ;;
    "admin policy create emfont-bootstrap emfont-controller "*)
        policy_file=${6:?}
        grep -F '"s3:PutObject"' "$policy_file" >/dev/null
        if grep -E 'DeleteObject|Multipart|GetObjectVersion' "$policy_file" >/dev/null; then
            printf 'controller policy contains unnecessary actions\n' >&2
            exit 1
        fi
        printf '%s\n' controller-policy >>"$FAKE_COMMAND_LOG"
        ;;
    "admin policy create emfont-bootstrap emfont-cleanup "*)
        policy_file=${6:?}
        grep -F '"s3:ListBucket"' "$policy_file" >/dev/null
        grep -F '"s3:GetObject"' "$policy_file" >/dev/null
	    grep -F '"s3:DeleteObject"' "$policy_file" >/dev/null
	    grep -F '"s3:DeleteObjectVersion"' "$policy_file" >/dev/null
	    grep -F '"s3:prefix": ["_generated/*"]' "$policy_file" >/dev/null
        if grep -E 'PutObject|Multipart|GetBucketVersioning' "$policy_file" >/dev/null; then
            printf 'cleanup policy contains unexpected actions\n' >&2
            exit 1
        fi
        printf '%s\n' cleanup-policy >>"$FAKE_COMMAND_LOG"
        ;;
    "admin policy info emfont-bootstrap emfont-controller --json")
        printf '%s\n' controller-policy-info >>"$FAKE_COMMAND_LOG"
        printf '%s\n' '{"status":"success","policy":"emfont-controller","policyInfo":{"PolicyName":"emfont-controller","Policy":{"Version":"2012-10-17","Statement":[]},"CreateDate":"2026-07-11T01:37:17Z","UpdateDate":"2026-07-11T01:37:17Z"},"isGroup":false}'
        ;;
    "admin policy info emfont-bootstrap emfont-cleanup --json")
        printf '%s\n' cleanup-policy-info >>"$FAKE_COMMAND_LOG"
        printf '%s\n' '{"status":"success","policy":"emfont-cleanup","policyInfo":{"PolicyName":"emfont-cleanup","Policy":{"Version":"2012-10-17","Statement":[]},"CreateDate":"2026-07-11T01:37:17Z","UpdateDate":"2026-07-11T01:37:17Z"},"isGroup":false}'
        ;;
    "admin user add "*)
        IFS= read -r access_key
        IFS= read -r secret_key
        if [ "$access_key" = "$EMFONT_MINIO_ACCESS_KEY" ] && [ "$secret_key" = "$EMFONT_MINIO_SECRET_KEY" ]; then
            printf '%s\n' controller-user >>"$FAKE_COMMAND_LOG"
        elif [ "$access_key" = "$EMFONT_MINIO_CLEANUP_ACCESS_KEY" ] && [ "$secret_key" = "$EMFONT_MINIO_CLEANUP_SECRET_KEY" ]; then
            printf '%s\n' cleanup-user >>"$FAKE_COMMAND_LOG"
        else
            printf 'unexpected user credentials on stdin\n' >&2
            exit 1
        fi
        ;;
    "admin policy attach emfont-bootstrap emfont-controller --user app-user")
        printf '%s\n' controller-policy-attach >>"$FAKE_COMMAND_LOG"
        ;;
    "admin policy attach emfont-bootstrap emfont-cleanup --user cleanup-user")
        printf '%s\n' cleanup-policy-attach >>"$FAKE_COMMAND_LOG"
        ;;
    "admin user info emfont-bootstrap app-user --json")
        printf '%s\n' controller-user-info >>"$FAKE_COMMAND_LOG"
        printf '%s\n' '{"status":"success","accessKey":"app-user","userStatus":"enabled","policyName":"emfont-controller"}'
        ;;
    "admin user info emfont-bootstrap cleanup-user --json")
        printf '%s\n' cleanup-user-info >>"$FAKE_COMMAND_LOG"
        printf '%s\n' '{"status":"success","accessKey":"cleanup-user","userStatus":"enabled","policyName":"emfont-cleanup"}'
        ;;
    "ilm rule remove "*)
        printf '%s\n' ilm-remove >>"$FAKE_COMMAND_LOG"
        [ "${FAKE_ILM_REMOVE_FAIL:-0}" != 1 ]
        ;;
    "ilm rule add "*) printf '%s\n' ilm-add >>"$FAKE_COMMAND_LOG" ;;
    "ilm rule ls "*)
        printf '%s\n' ilm-list >>"$FAKE_COMMAND_LOG"
        printf '%s\n' '{"status":"success","target":"emfont-bootstrap/emfont-test","config":{"Rules":[{"Expiration":{"ExpiredObjectDeleteMarker":true},"ID":"generated-expiry","Filter":{"Prefix":"_generated/"},"NoncurrentVersionExpiration":{"NoncurrentDays":7},"Status":"Enabled"}]},"updatedAt":"2026-07-11T01:04:38Z"}'
        ;;
    *)
        printf 'unexpected mc command\n' >&2
        exit 1
        ;;
esac
SCRIPT
chmod 0500 "$temp_dir/mc"

cat >"$temp_dir/minioinitcheck" <<'SCRIPT'
#!/bin/sh
set -eu

payload=$(cat)
case "$*" in
    "identity --subsystem identity_openid")
        printf '%s\n' identity-openid-check >>"$FAKE_COMMAND_LOG"
        [ "$(printf '%s' "$payload" | grep -o '"key":"enable","value":"off"' | wc -l)" -eq 2 ]
        ;;
    "identity --subsystem identity_ldap")
        printf '%s\n' identity-ldap-check >>"$FAKE_COMMAND_LOG"
        printf '%s' "$payload" | grep -F '"key":"enable","value":"off"' >/dev/null
        ;;
    "anonymous --target emfont-bootstrap/emfont-test")
        printf '%s\n' anonymous-check >>"$FAKE_COMMAND_LOG"
        printf '%s' "$payload" | grep -F '"permission":"private"' >/dev/null
        [ "${FAKE_ANONYMOUS_CHECK_FAIL:-0}" != 1 ]
        ;;
    "policy --policy emfont-controller --bucket emfont-test")
        printf '%s\n' controller-policy-check >>"$FAKE_COMMAND_LOG"
        printf '%s' "$payload" | grep -F '"policy":"emfont-controller"' >/dev/null
        [ "${FAKE_POLICY_CHECK_FAIL:-0}" != 1 ]
        ;;
    "user --policy emfont-controller")
        printf '%s\n' controller-user-check >>"$FAKE_COMMAND_LOG"
        printf '%s' "$payload" | grep -F '"policyName":"emfont-controller"' >/dev/null
        [ "${FAKE_USER_CHECK_FAIL:-0}" != 1 ]
        ;;
    "policy --role cleanup --policy emfont-cleanup --bucket emfont-test")
        printf '%s\n' cleanup-policy-check >>"$FAKE_COMMAND_LOG"
        printf '%s' "$payload" | grep -F '"policy":"emfont-cleanup"' >/dev/null
        [ "${FAKE_CLEANUP_POLICY_CHECK_FAIL:-0}" != 1 ]
        ;;
    "user --role cleanup --policy emfont-cleanup")
        printf '%s\n' cleanup-user-check >>"$FAKE_COMMAND_LOG"
        printf '%s' "$payload" | grep -F '"policyName":"emfont-cleanup"' >/dev/null
        [ "${FAKE_CLEANUP_USER_CHECK_FAIL:-0}" != 1 ]
        ;;
    "lifecycle --target emfont-bootstrap/emfont-test --prefix _generated/ --noncurrent-days 7")
        printf '%s\n' lifecycle-check >>"$FAKE_COMMAND_LOG"
        printf '%s' "$payload" | grep -F '"NoncurrentDays":7' >/dev/null
        [ "${FAKE_LIFECYCLE_CHECK_FAIL:-0}" != 1 ]
        ;;
    *)
        printf 'unexpected minioinitcheck command\n' >&2
        exit 1
        ;;
esac
SCRIPT
chmod 0500 "$temp_dir/minioinitcheck"

cat >"$temp_dir/objectversionbackfill" <<'SCRIPT'
#!/bin/sh
set -eu

[ "$#" -eq 0 ] || {
    printf 'backfill received command-line arguments\n' >&2
    exit 1
}
[ -f "$FAKE_STATE_DIR/version-enabled" ] || {
    printf 'backfill ran before versioning was enabled\n' >&2
    exit 1
}
if grep -E '^(controller-|cleanup-|ilm-)' "$FAKE_COMMAND_LOG" >/dev/null 2>&1; then
    printf 'backfill ran after application setup began\n' >&2
    exit 1
fi
[ "$EMFONT_MINIO_BOOTSTRAP_ENDPOINT" = "${FAKE_EXPECTED_ENDPOINT:-minio:9000}" ]
[ "$EMFONT_MINIO_BUCKET" = emfont-test ]
[ "$MINIO_ROOT_USER" = root-user ]
[ "$MINIO_ROOT_PASSWORD" = root-password ]
[ "$EMFONT_MINIO_SECURE" = "${FAKE_EXPECTED_SECURE:-false}" ]
[ "${EMFONT_MINIO_REGION+x}" = x ]
[ "$EMFONT_MINIO_REGION" = "${FAKE_EXPECTED_REGION:-}" ]
[ "$EMFONT_MINIO_BACKFILL_CONCURRENCY" = 4 ]
printf '%s\n' backfill >>"$FAKE_COMMAND_LOG"
[ "${FAKE_BACKFILL_FAIL:-0}" != 1 ] || exit 17
SCRIPT
chmod 0500 "$temp_dir/objectversionbackfill"

reset_fake() {
    rm -f "$temp_dir/state/version-enabled" "$temp_dir/commands.log"
    mkdir -p "$temp_dir/state"
    : >"$temp_dir/commands.log"
}

run_init() {
    PATH="$temp_dir:$PATH" \
    FAKE_STATE_DIR="$temp_dir/state" \
    FAKE_COMMAND_LOG="$temp_dir/commands.log" \
    MINIO_ROOT_USER=root-user \
    MINIO_ROOT_PASSWORD=root-password \
    EMFONT_MINIO_ACCESS_KEY=app-user \
    EMFONT_MINIO_SECRET_KEY=app-password \
    EMFONT_MINIO_CLEANUP_ACCESS_KEY=cleanup-user \
    EMFONT_MINIO_CLEANUP_SECRET_KEY=cleanup-password \
    EMFONT_MINIO_CLEANUP_POLICY_NAME=emfont-cleanup \
    EMFONT_MINIO_BUCKET=emfont-test \
    EMFONT_MINIO_BOOTSTRAP_ENDPOINT=${TEST_MINIO_ENDPOINT:-minio:9000} \
    EMFONT_MINIO_SECURE=${TEST_MINIO_SECURE:-false} \
    EMFONT_MINIO_REGION=${TEST_MINIO_REGION:-} \
    FAKE_EXPECTED_ENDPOINT=${TEST_MINIO_ENDPOINT:-minio:9000} \
    FAKE_EXPECTED_SECURE=${TEST_MINIO_SECURE:-false} \
    FAKE_EXPECTED_REGION=${TEST_MINIO_REGION:-} \
    FAKE_OPENID_ENABLE=${FAKE_OPENID_ENABLE-off} \
    FAKE_OPENID_SECONDARY_ENABLE=${FAKE_OPENID_SECONDARY_ENABLE-off} \
    FAKE_OPENID_CONFIG_URL=${FAKE_OPENID_CONFIG_URL:-} \
    FAKE_LDAP_ENABLE=${FAKE_LDAP_ENABLE-off} \
    FAKE_LDAP_SERVER_ADDR=${FAKE_LDAP_SERVER_ADDR:-} \
    FAKE_ANONYMOUS_PERMISSION=${FAKE_ANONYMOUS_PERMISSION:-private} \
    FAKE_BACKFILL_FAIL=${FAKE_BACKFILL_FAIL:-0} \
    FAKE_ILM_REMOVE_FAIL=${FAKE_ILM_REMOVE_FAIL:-0} \
    FAKE_POLICY_CHECK_FAIL=${FAKE_POLICY_CHECK_FAIL:-0} \
    FAKE_USER_CHECK_FAIL=${FAKE_USER_CHECK_FAIL:-0} \
    FAKE_CLEANUP_POLICY_CHECK_FAIL=${FAKE_CLEANUP_POLICY_CHECK_FAIL:-0} \
    FAKE_CLEANUP_USER_CHECK_FAIL=${FAKE_CLEANUP_USER_CHECK_FAIL:-0} \
    FAKE_ANONYMOUS_CHECK_FAIL=${FAKE_ANONYMOUS_CHECK_FAIL:-0} \
    FAKE_LIFECYCLE_CHECK_FAIL=${FAKE_LIFECYCLE_CHECK_FAIL:-0} \
        "$script_dir/minio-init.sh"
}

assert_blocked() {
    provider=$1
    output_file="$temp_dir/$provider.out"
    reset_fake
    if run_init >"$output_file" 2>&1; then
        printf 'minio-init accepted enabled %s\n' "$provider" >&2
        exit 1
    fi
    grep -F "MinIO $provider must remain disabled for every target" "$output_file" >/dev/null
}

FAKE_OPENID_ENABLE=on FAKE_LDAP_ENABLE=off assert_blocked identity_openid
FAKE_OPENID_ENABLE=off FAKE_OPENID_SECONDARY_ENABLE=on assert_blocked identity_openid
FAKE_OPENID_ENABLE='' FAKE_OPENID_CONFIG_URL=https://issuer.invalid assert_blocked identity_openid
FAKE_OPENID_ENABLE=off FAKE_LDAP_ENABLE=on assert_blocked identity_ldap
FAKE_OPENID_ENABLE=off FAKE_LDAP_ENABLE='' FAKE_LDAP_SERVER_ADDR=ldap.internal:636 assert_blocked identity_ldap

FAKE_OPENID_ENABLE=off
FAKE_LDAP_ENABLE=off
reset_fake
output=$(run_init)
printf '%s\n' "$output" | grep -F 'MinIO bucket emfont-test and policies emfont-controller/emfont-cleanup are ready.' >/dev/null
if printf '%s\n' "$output" | grep -E 'app-user|cleanup-user' >/dev/null; then
    printf 'MinIO access-key identifier appeared in initializer output\n' >&2
    exit 1
fi

version_line=$(grep -n '^version-enable$' "$temp_dir/commands.log" | cut -d: -f1)
backfill_line=$(grep -n '^backfill$' "$temp_dir/commands.log" | cut -d: -f1)
policy_line=$(grep -n '^controller-policy$' "$temp_dir/commands.log" | cut -d: -f1)
[ "$version_line" -lt "$backfill_line" ]
[ "$backfill_line" -lt "$policy_line" ]
grep -F 'controller-user-check' "$temp_dir/commands.log" >/dev/null
grep -F 'controller-policy-check' "$temp_dir/commands.log" >/dev/null
grep -F 'cleanup-user-check' "$temp_dir/commands.log" >/dev/null
grep -F 'cleanup-policy-check' "$temp_dir/commands.log" >/dev/null
grep -F 'anonymous-check' "$temp_dir/commands.log" >/dev/null
grep -F 'lifecycle-check' "$temp_dir/commands.log" >/dev/null

reset_fake
FAKE_ILM_REMOVE_FAIL=1 run_init >/dev/null
grep -F 'lifecycle-check' "$temp_dir/commands.log" >/dev/null

reset_fake
TEST_MINIO_REGION=ap-northeast-1 run_init >/dev/null
grep -F 'bucket:mb --ignore-existing --region ap-northeast-1 emfont-bootstrap/emfont-test' \
    "$temp_dir/commands.log" >/dev/null

reset_fake
url_endpoint_output="$temp_dir/url-endpoint.out"
if TEST_MINIO_ENDPOINT=https://minio:9000 TEST_MINIO_SECURE=true \
    run_init >"$url_endpoint_output" 2>&1; then
    printf 'minio-init accepted a URL-form endpoint\n' >&2
    exit 1
fi
grep -F 'must contain only a host and optional port' "$url_endpoint_output" >/dev/null

for failed_check in policy user cleanup-policy cleanup-user anonymous lifecycle; do
    reset_fake
    check_output="$temp_dir/$failed_check-check.out"
    if [ "$failed_check" = policy ]; then
        if FAKE_POLICY_CHECK_FAIL=1 run_init >"$check_output" 2>&1; then
            printf 'minio-init accepted failed policy final-state verification\n' >&2
            exit 1
        fi
    elif [ "$failed_check" = user ]; then
        if FAKE_USER_CHECK_FAIL=1 run_init >"$check_output" 2>&1; then
            printf 'minio-init accepted failed user final-state verification\n' >&2
            exit 1
        fi
    elif [ "$failed_check" = cleanup-policy ]; then
        if FAKE_CLEANUP_POLICY_CHECK_FAIL=1 run_init >"$check_output" 2>&1; then
            printf 'minio-init accepted failed cleanup-policy final-state verification\n' >&2
            exit 1
        fi
    elif [ "$failed_check" = cleanup-user ]; then
        if FAKE_CLEANUP_USER_CHECK_FAIL=1 run_init >"$check_output" 2>&1; then
            printf 'minio-init accepted failed cleanup-user final-state verification\n' >&2
            exit 1
        fi
    elif [ "$failed_check" = anonymous ]; then
        if FAKE_ANONYMOUS_CHECK_FAIL=1 run_init >"$check_output" 2>&1; then
            printf 'minio-init accepted failed anonymous-policy final-state verification\n' >&2
            exit 1
        fi
    else
        if FAKE_LIFECYCLE_CHECK_FAIL=1 run_init >"$check_output" 2>&1; then
            printf 'minio-init accepted failed lifecycle final-state verification\n' >&2
            exit 1
        fi
    fi
done

reset_fake
failure_output="$temp_dir/backfill-failure.out"
if FAKE_BACKFILL_FAIL=1 run_init >"$failure_output" 2>&1; then
    printf 'minio-init accepted a failed object-version backfill\n' >&2
    exit 1
fi
grep -F 'legacy object-version backfill failed' "$failure_output" >/dev/null
grep -F 'backfill' "$temp_dir/commands.log" >/dev/null
if grep -E '^(controller-|cleanup-|ilm-)' "$temp_dir/commands.log" >/dev/null 2>&1; then
    printf 'minio-init continued application setup after backfill failure\n' >&2
    exit 1
fi

printf 'minio-init identity-provider and object-version backfill checks passed\n'
