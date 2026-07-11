#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
readonly helper="$script_dir/../fontworker-package-manifest.sh"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/emfont-package-manifest-test.XXXXXX")"
cleanup() {
    rm -rf "$temporary_dir"
}
trap cleanup EXIT HUP INT TERM

manifest() {
    local output="$1"
    local compiler_version="$2"
    local runtime_version="$3"
    printf 'schema\temfont-fontworker-native-packages-v1\n' >"$output"
    printf 'runtime\tlibexample.so.1\t/usr/lib/libexample.so.1\tlibexample1:amd64\t%s\tamd64\n' \
        "$runtime_version" >>"$output"
    printf 'tool\tcxx\t/usr/bin/g++\tg++:amd64\t%s\tamd64\n' \
        "$compiler_version" >>"$output"
}

manifest "$temporary_dir/base.tsv" '4:14.2.0-1' '1.0.0-1'
manifest "$temporary_dir/compiler.tsv" '4:14.2.0-2' '1.0.0-1'
manifest "$temporary_dir/runtime.tsv" '4:14.2.0-1' '1.0.0-2'

base_digest="$($helper digest "$temporary_dir/base.tsv")"
compiler_digest="$($helper digest "$temporary_dir/compiler.tsv")"
runtime_digest="$($helper digest "$temporary_dir/runtime.tsv")"
[[ "$base_digest" =~ ^[0-9a-f]{64}$ ]]
[[ "$base_digest" != "$compiler_digest" ]]
[[ "$base_digest" != "$runtime_digest" ]]

sed 's/g++:amd64/g++:arm64/' "$temporary_dir/base.tsv" >"$temporary_dir/owner.tsv"
sed 's/\tamd64$/\tarm64/' "$temporary_dir/base.tsv" >"$temporary_dir/architecture.tsv"
[[ "$base_digest" != "$($helper digest "$temporary_dir/owner.tsv")" ]]
[[ "$base_digest" != "$($helper digest "$temporary_dir/architecture.tsv")" ]]

sed -n '1p' "$temporary_dir/base.tsv" >"$temporary_dir/empty.tsv"
if "$helper" validate "$temporary_dir/empty.tsv" >/dev/null 2>&1; then
    printf 'manifest without package records was accepted\n' >&2
    exit 1
fi

{
    sed -n '1p' "$temporary_dir/base.tsv"
    sed -n '3p' "$temporary_dir/base.tsv"
    sed -n '2p' "$temporary_dir/base.tsv"
} >"$temporary_dir/unsorted.tsv"
if "$helper" validate "$temporary_dir/unsorted.tsv" >/dev/null 2>&1; then
    printf 'non-canonical package manifest was accepted\n' >&2
    exit 1
fi

printf 'fontworker package manifest tests passed\n'
