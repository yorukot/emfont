#!/bin/sh
set -eu
umask 077

fail() {
    printf 'minio-init: %s\n' "$*" >&2
    exit 1
}

require() {
    name=$1
    eval "value=\${${name}:-}"
    [ -n "$value" ] || fail "$name is required"
}

for required_name in \
    MINIO_ROOT_USER \
    MINIO_ROOT_PASSWORD \
    EMFONT_MINIO_ACCESS_KEY \
    EMFONT_MINIO_SECRET_KEY \
    EMFONT_MINIO_CLEANUP_ACCESS_KEY \
    EMFONT_MINIO_CLEANUP_SECRET_KEY \
    EMFONT_MINIO_BUCKET
do
    require "$required_name"
done

[ "$MINIO_ROOT_USER" != "$EMFONT_MINIO_ACCESS_KEY" ] || \
    fail "the controller access key must not be the MinIO root user"
[ "$MINIO_ROOT_USER" != "$EMFONT_MINIO_CLEANUP_ACCESS_KEY" ] || \
    fail "the cleanup access key must not be the MinIO root user"
[ "$EMFONT_MINIO_ACCESS_KEY" != "$EMFONT_MINIO_CLEANUP_ACCESS_KEY" ] || \
    fail "the controller and cleanup access keys must be distinct"

bucket=$EMFONT_MINIO_BUCKET
case "$bucket" in
    *[!a-z0-9.-]* | .* | *. | *..* | *.-* | *-.*)
        fail "EMFONT_MINIO_BUCKET is not a valid DNS-style bucket name"
        ;;
esac
if [ "${#bucket}" -lt 3 ] || [ "${#bucket}" -gt 63 ]; then
    fail "EMFONT_MINIO_BUCKET must contain between 3 and 63 characters"
fi

policy_name=${EMFONT_MINIO_POLICY_NAME:-emfont-controller}
case "$policy_name" in
    *[!A-Za-z0-9+=,.@_-]*) fail "EMFONT_MINIO_POLICY_NAME contains unsupported characters" ;;
esac
cleanup_policy_name=${EMFONT_MINIO_CLEANUP_POLICY_NAME:-emfont-cleanup}
case "$cleanup_policy_name" in
    *[!A-Za-z0-9+=,.@_-]*) fail "EMFONT_MINIO_CLEANUP_POLICY_NAME contains unsupported characters" ;;
esac
[ "$policy_name" != "$cleanup_policy_name" ] || \
    fail "the controller and cleanup policy names must be distinct"

noncurrent_expire_days=${EMFONT_MINIO_NONCURRENT_EXPIRE_DAYS:-7}
case "$noncurrent_expire_days" in
    '' | *[!0-9]*) fail "EMFONT_MINIO_NONCURRENT_EXPIRE_DAYS must be a positive integer" ;;
esac
[ "$noncurrent_expire_days" -gt 0 ] || \
    fail "EMFONT_MINIO_NONCURRENT_EXPIRE_DAYS must be greater than zero"

endpoint=${EMFONT_MINIO_BOOTSTRAP_ENDPOINT:-minio:9000}
case "$endpoint" in
    '' | *://* | */* | *\?* | *\#* | *@* | *[[:space:]]*)
        fail "EMFONT_MINIO_BOOTSTRAP_ENDPOINT must contain only a host and optional port"
        ;;
esac

secure=${EMFONT_MINIO_SECURE:-false}
case "$secure" in
    true | false) ;;
    *) fail "EMFONT_MINIO_SECURE must be true or false" ;;
esac
if [ "$secure" = true ]; then
    endpoint_url="https://$endpoint"
else
    endpoint_url="http://$endpoint"
fi

region=${EMFONT_MINIO_REGION:-}
case "$region" in
    *[!A-Za-z0-9-]*) fail "EMFONT_MINIO_REGION contains unsupported characters" ;;
esac

export EMFONT_MINIO_BOOTSTRAP_ENDPOINT="$endpoint"
export EMFONT_MINIO_SECURE="$secure"
export EMFONT_MINIO_REGION="$region"
export EMFONT_MINIO_BACKFILL_CONCURRENCY="${EMFONT_MINIO_BACKFILL_CONCURRENCY:-4}"

export MC_CONFIG_DIR="${MC_CONFIG_DIR:-/tmp/.mc}"
attempt=0
until printf '%s\n%s\n' "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" | \
    mc alias set emfont-bootstrap "$endpoint_url" \
        --api S3v4 --path on >/dev/null 2>&1
do
    attempt=$((attempt + 1))
    [ "$attempt" -lt 30 ] || fail "MinIO did not accept bootstrap credentials"
    sleep 2
done

for subsystem in identity_openid identity_ldap; do
    if ! mc admin config get emfont-bootstrap "$subsystem" --json | \
        minioinitcheck identity --subsystem "$subsystem"
    then
        fail "MinIO $subsystem must remain disabled for every target"
    fi
done

state_dir=$(mktemp -d /tmp/emfont-init.XXXXXX)
policy_file=$state_dir/policy.json
policy_info_file=$state_dir/policy-info.json
user_info_file=$state_dir/user-info.json
cleanup_policy_file=$state_dir/cleanup-policy.json
cleanup_policy_info_file=$state_dir/cleanup-policy-info.json
cleanup_user_info_file=$state_dir/cleanup-user-info.json
anonymous_info_file=$state_dir/anonymous-info.json
lifecycle_info_file=$state_dir/lifecycle-info.json
trap 'rm -rf "$state_dir"' EXIT HUP INT TERM

if [ -n "$region" ]; then
    mc mb --ignore-existing --region "$region" "emfont-bootstrap/$bucket" >/dev/null
else
    mc mb --ignore-existing "emfont-bootstrap/$bucket" >/dev/null
fi
if ! mc anonymous set private "emfont-bootstrap/$bucket" >/dev/null 2>&1; then
    fail "could not remove the MinIO bucket anonymous policy"
fi
if ! mc anonymous get "emfont-bootstrap/$bucket" --json >"$anonymous_info_file"; then
    fail "could not inspect the MinIO bucket anonymous policy"
fi
minioinitcheck anonymous --target "emfont-bootstrap/$bucket" \
    <"$anonymous_info_file" || \
    fail "MinIO bucket still has an anonymous policy"
mc version enable "emfont-bootstrap/$bucket" >/dev/null
objectversionbackfill || fail "legacy object-version backfill failed"

cat >"$policy_file" <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:GetBucketLocation",
        "s3:GetBucketVersioning",
        "s3:ListBucket"
      ],
      "Resource": ["arn:aws:s3:::$bucket"]
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:GetObject"
      ],
      "Resource": ["arn:aws:s3:::$bucket/*"]
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:PutObject"
      ],
      "Resource": ["arn:aws:s3:::$bucket/_generated/*"]
    }
  ]
}
EOF

cat >"$cleanup_policy_file" <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
	    {
	      "Effect": "Allow",
	      "Action": [
	        "s3:GetBucketLocation"
	      ],
	      "Resource": ["arn:aws:s3:::$bucket"]
	    },
	    {
	      "Effect": "Allow",
	      "Action": [
	        "s3:ListBucket"
	      ],
	      "Resource": ["arn:aws:s3:::$bucket"],
	      "Condition": {
	        "StringLike": {
	          "s3:prefix": ["_generated/*"]
	        }
	      }
	    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:DeleteObject",
        "s3:DeleteObjectVersion",
        "s3:GetObject"
      ],
      "Resource": ["arn:aws:s3:::$bucket/_generated/*"]
    }
  ]
}
EOF

mc admin policy create emfont-bootstrap "$policy_name" "$policy_file" >/dev/null
if ! mc admin policy info emfont-bootstrap "$policy_name" --json \
    >"$policy_info_file"; then
    fail "could not inspect the MinIO application policy"
fi
minioinitcheck policy --policy "$policy_name" --bucket "$bucket" \
    <"$policy_info_file" || \
    fail "MinIO application policy does not match the least-privilege policy"
if ! printf '%s\n%s\n' "$EMFONT_MINIO_ACCESS_KEY" "$EMFONT_MINIO_SECRET_KEY" | \
    mc admin user add emfont-bootstrap >/dev/null 2>&1; then
    fail "could not create or update the MinIO application user"
fi
if ! mc admin policy attach emfont-bootstrap "$policy_name" \
    --user "$EMFONT_MINIO_ACCESS_KEY" >/dev/null 2>&1; then
    fail "could not attach the MinIO application policy"
fi
if ! mc admin user info emfont-bootstrap "$EMFONT_MINIO_ACCESS_KEY" --json \
    >"$user_info_file" 2>/dev/null; then
    fail "could not inspect the MinIO application user"
fi
minioinitcheck user --policy "$policy_name" <"$user_info_file" || \
    fail "MinIO application user has unexpected policy or group membership"

mc admin policy create emfont-bootstrap "$cleanup_policy_name" "$cleanup_policy_file" >/dev/null
if ! mc admin policy info emfont-bootstrap "$cleanup_policy_name" --json \
    >"$cleanup_policy_info_file"; then
    fail "could not inspect the MinIO cleanup policy"
fi
minioinitcheck policy --role cleanup --policy "$cleanup_policy_name" --bucket "$bucket" \
    <"$cleanup_policy_info_file" || \
    fail "MinIO cleanup policy does not match the least-privilege policy"
if ! printf '%s\n%s\n' "$EMFONT_MINIO_CLEANUP_ACCESS_KEY" "$EMFONT_MINIO_CLEANUP_SECRET_KEY" | \
    mc admin user add emfont-bootstrap >/dev/null 2>&1; then
    fail "could not create or update the MinIO cleanup user"
fi
if ! mc admin policy attach emfont-bootstrap "$cleanup_policy_name" \
    --user "$EMFONT_MINIO_CLEANUP_ACCESS_KEY" >/dev/null 2>&1; then
    fail "could not attach the MinIO cleanup policy"
fi
if ! mc admin user info emfont-bootstrap "$EMFONT_MINIO_CLEANUP_ACCESS_KEY" --json \
    >"$cleanup_user_info_file" 2>/dev/null; then
    fail "could not inspect the MinIO cleanup user"
fi
minioinitcheck user --role cleanup --policy "$cleanup_policy_name" \
    <"$cleanup_user_info_file" || \
    fail "MinIO cleanup user has unexpected policy or group membership"

mc ilm rule remove --all --force "emfont-bootstrap/$bucket" >/dev/null 2>&1 || true
mc ilm rule add \
    --prefix "_generated/" \
    --noncurrent-expire-days "$noncurrent_expire_days" \
    --expire-delete-marker \
    "emfont-bootstrap/$bucket" >/dev/null
if ! mc ilm rule ls "emfont-bootstrap/$bucket" --json \
    >"$lifecycle_info_file"; then
    fail "could not inspect the MinIO bucket lifecycle"
fi
minioinitcheck lifecycle \
    --target "emfont-bootstrap/$bucket" \
    --prefix "_generated/" \
    --noncurrent-days "$noncurrent_expire_days" \
    <"$lifecycle_info_file" || \
    fail "MinIO bucket lifecycle does not match the required policy"

printf 'MinIO bucket %s and policies %s/%s are ready.\n' \
    "$bucket" "$policy_name" "$cleanup_policy_name"
