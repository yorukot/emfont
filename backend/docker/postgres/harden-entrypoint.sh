#!/bin/sh
set -eu

entrypoint='/usr/local/bin/docker-entrypoint.sh'
gosu='/usr/local/bin/gosu'
setpriv='/usr/bin/setpriv'

upstream_entrypoint_sha256='9c440299ae04a0a79d55b8bf03307036d890a40979d2fb698073c9050d4b20a5'
patched_entrypoint_sha256='3fec31042dd4ca860976a4db64f659d34552f07a173754aecc12ac5ef1bcc1ae'
postgres_version='16.14-1.pgdg12+1'
postgres_common_version='291.pgdg12+1'
util_linux_version='2.38.1-5+deb12u3'

fail() {
	printf >&2 'postgres hardening assertion failed: %s\n' "$*"
	exit 1
}

sha256() {
	sha256sum "$1" | awk '{ print $1 }'
}

package_version() {
	dpkg-query -W -f='${Version}' "$1"
}

assert_equal() {
	actual="$1"
	expected="$2"
	description="$3"
	[ "$actual" = "$expected" ] || fail "$description: expected $expected, got $actual"
}

architecture="$(dpkg --print-architecture)"
case "$architecture" in
	amd64)
		gosu_sha256='52c8749d0142edd234e9d6bd5237dff2d81e71f43537e2f4f66f75dd4b243dd0'
		;;
	arm64)
		gosu_sha256='3a8ef022d82c0bc4a98bcb144e77da714c25fcfa64dccc57f6aba7ae47ff1a44'
		;;
	*)
		fail "unsupported architecture $architecture (supported: amd64, arm64)"
		;;
esac

assert_equal "${PG_MAJOR:-}" '16' 'PG_MAJOR'
assert_equal "${PG_VERSION:-}" "$postgres_version" 'PG_VERSION'
assert_equal "${GOSU_VERSION:-}" '1.19' 'GOSU_VERSION'
assert_equal "$(package_version postgresql-16)" "$postgres_version" 'postgresql-16 package version'
assert_equal "$(package_version postgresql-client-16)" "$postgres_version" 'postgresql-client-16 package version'
assert_equal "$(package_version postgresql-common)" "$postgres_common_version" 'postgresql-common package version'
assert_equal "$(package_version util-linux)" "$util_linux_version" 'util-linux package version'
assert_equal "$(dpkg-query -W -f='${Architecture}' util-linux)" "$architecture" 'util-linux package architecture'
assert_equal "$(dpkg-query -S "$setpriv")" "util-linux: $setpriv" 'setpriv package owner'

[ -x "$entrypoint" ] || fail "$entrypoint is not executable"
[ -x "$gosu" ] || fail "$gosu is not executable"
[ -x "$setpriv" ] || fail "$setpriv is not executable"
assert_equal "$(sha256 "$entrypoint")" "$upstream_entrypoint_sha256" 'upstream entrypoint SHA-256'
assert_equal "$(sha256 "$gosu")" "$gosu_sha256" 'upstream gosu SHA-256'

upstream_call="exec gosu postgres \"\$BASH_SOURCE\" \"\$@\""
patched_call="exec /usr/bin/setpriv --reuid=postgres --regid=postgres --init-groups \"\$BASH_SOURCE\" \"\$@\""
assert_equal "$(grep -Fc "$upstream_call" "$entrypoint")" '1' 'upstream gosu handoff count'
assert_equal "$(grep -Fc 'gosu' "$entrypoint")" '1' 'all upstream gosu references count'

expected_identity="$(id -u postgres):$(id -g postgres):$(id -G postgres)"
identity_command="printf '%s:%s:%s' \"\$(id -u)\" \"\$(id -g)\" \"\$(id -G)\""
setpriv_identity="$("$setpriv" --reuid=postgres --regid=postgres --init-groups /bin/sh -c "$identity_command")"
assert_equal "$setpriv_identity" "$expected_identity" 'setpriv postgres identity'

LC_ALL=C sed -i \
	's|exec gosu postgres |exec /usr/bin/setpriv --reuid=postgres --regid=postgres --init-groups |' \
	"$entrypoint"

assert_equal "$(grep -Fc "$patched_call" "$entrypoint")" '1' 'patched setpriv handoff count'
assert_equal "$(grep -Fc 'gosu' "$entrypoint")" '0' 'remaining entrypoint gosu references count'
assert_equal "$(sha256 "$entrypoint")" "$patched_entrypoint_sha256" 'patched entrypoint SHA-256'

rm -f "$gosu"
[ ! -e "$gosu" ] || fail "$gosu still exists"
if command -v gosu >/dev/null 2>&1; then
	fail 'gosu remains discoverable through PATH'
fi

printf 'hardened PostgreSQL entrypoint for %s using util-linux %s\n' "$architecture" "$util_linux_version"
