#!/usr/bin/env bash
set -Eeuo pipefail

readonly image="${1:?usage: verify-fontworker-image.sh IMAGE [PLATFORM [FONT_PATH]]}"
readonly platform="${2:-linux/amd64}"
provided_font="${3:-}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
backend_dir="$(cd -- "$script_dir/.." && pwd)"
readonly backend_dir
readonly source_sha256='864727d210d54f2537bbe23b3a839436c3992af72de9322af5270897246bd44f'
readonly output_sha256='3e365346851cf540ccbef2b61ca7c05c51ff93833c8a928c5a816884373819e2'
readonly output_size=2868
readonly source_url='https://raw.githubusercontent.com/google/fonts/ec0464b978de222073645d6d3366f3fdf03376d8/ofl/notosanstc/NotoSansTC%5Bwght%5D.ttf'

case "$platform" in
    linux/amd64 | linux/arm64) ;;
    *)
        printf 'unsupported worker image platform: %s\n' "$platform" >&2
        exit 2
        ;;
esac

for command_name in cmp docker python3 sha256sum; do
    command -v "$command_name" >/dev/null 2>&1 || {
        printf 'required command is unavailable: %s\n' "$command_name" >&2
        exit 2
    }
done

umask 077
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/emfont-worker-image.XXXXXX")"
reference_image=''
cleanup() {
    if [[ -n "$reference_image" ]]; then
        docker image rm --force "$reference_image" >/dev/null 2>&1 || true
    fi
    rm -rf "$temp_dir"
}
trap cleanup EXIT HUP INT TERM

if [[ -n "$provided_font" ]]; then
    [[ -r "$provided_font" ]] || {
        printf 'provided official font is not readable: %s\n' "$provided_font" >&2
        exit 2
    }
    font_path="$provided_font"
else
    command -v curl >/dev/null 2>&1 || {
        printf 'curl is required when FONT_PATH is omitted\n' >&2
        exit 2
    }
    font_path="$temp_dir/NotoSansTC.ttf"
    curl --fail --location --retry 3 --max-filesize 134217728 \
        --proto '=https' --proto-redir '=https' \
        "$source_url" --output "$font_path"
fi
readonly font_path
printf '%s  %s\n' "$source_sha256" "$font_path" | sha256sum --check --strict

request_path="$temp_dir/request.bin"
response_path="$temp_dir/response.bin"
output_path="$temp_dir/subset.woff2"
codepoints_path="$temp_dir/codepoints.txt"
readonly request_path response_path output_path codepoints_path

python3 - "$font_path" "$request_path" "$codepoints_path" <<'PY'
import pathlib
import struct
import sys

source = pathlib.Path(sys.argv[1]).read_bytes()
codepoints = sorted({ord(value) for value in "測試字型ABC"})
header = struct.pack(
    ">8sHHIQIHH", b"EMFONTWQ", 1, 2, 0,
    len(source), len(codepoints), 1, 0,
)
request = header + b"".join(struct.pack(">I", value) for value in codepoints) + source
pathlib.Path(sys.argv[2]).write_bytes(request)
pathlib.Path(sys.argv[3]).write_text(
    ",".join(f"{value:x}" for value in codepoints), encoding="ascii"
)
PY

docker run --rm --interactive --platform "$platform" \
    --network none \
    --read-only \
    --tmpfs /tmp:size=64m,mode=1777 \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --pids-limit 64 \
    --memory 3g \
    --cpus 2 \
    --entrypoint /usr/local/bin/emfont-fontworker \
    "$image" \
    <"$request_path" >"$response_path"

python3 - "$response_path" "$output_path" "$platform" <<'PY'
import pathlib
import re
import struct
import sys

response = pathlib.Path(sys.argv[1]).read_bytes()
if len(response) < 36:
    raise SystemExit("font worker response is truncated")
fields = struct.unpack(">8sHHIIQIHH", response[:36])
magic, version, status, flags, glyphs, data_length, message_length, version_length, reserved = fields
expected_length = 36 + data_length + message_length + version_length
if (magic, version, status, flags, reserved) != (b"EMFONTWP", 1, 0, 0, 0):
    raise SystemExit(f"font worker response header is invalid: {fields!r}")
if glyphs == 0 or len(response) != expected_length:
    raise SystemExit("font worker response lengths are invalid")

data_start = 36
message_start = data_start + data_length
version_start = message_start + message_length
output = response[data_start:message_start]
message = response[message_start:version_start].decode("utf-8")
builder_version = response[version_start:].decode("utf-8")
if message:
    raise SystemExit(f"successful font worker returned a diagnostic: {message}")
architecture = {"linux/amd64": "amd64", "linux/arm64": "arm64"}[sys.argv[3]]
identity_pattern = re.compile(
    rf"^harfbuzz-10\.2\.0-woff2-1\.0\.2-worker-linux-{architecture}-go1\.26\.5-"
    r"hb-10\.2\.0-1\+deb13u1-w2-1\.0\.2-2\+b2-src-[0-9a-f]{64}-"
    r"pkg-[0-9a-f]{64}-"
    r"worker-protocol-1$"
)
if identity_pattern.fullmatch(builder_version) is None:
    raise SystemExit(f"unexpected font worker identity: {builder_version}")

if not output.startswith(b"wOF2"):
    raise SystemExit("subset is not WOFF2")
pathlib.Path(sys.argv[2]).write_bytes(output)
PY

reference_image="emfont-font-reference:${platform#linux/}-$$"
docker buildx build \
    --pull \
    --platform "$platform" \
    --target font-reference \
    --load \
    --tag "$reference_image" \
    --file "$backend_dir/Dockerfile" \
    "$backend_dir"

reference_dir="$temp_dir/reference"
mkdir -m 0700 "$reference_dir"
readonly reference_dir
docker run --rm --platform "$platform" \
    --network none \
    --read-only \
    --tmpfs /tmp:size=64m,mode=1777 \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --pids-limit 64 \
    --memory 1g \
    --cpus 2 \
    --volume "$font_path:/input/font.ttf:ro" \
    --volume "$reference_dir:/output" \
    --entrypoint /bin/bash \
    "$reference_image" \
    -euc '
        hb-subset /input/font.ttf \
            "--unicodes=$1" \
            --output-file=/output/reference.ttf
        woff2_compress /output/reference.ttf
        test -s /output/reference.woff2
    ' emfont-reference "$(cat "$codepoints_path")"

reference_output="$reference_dir/reference.woff2"
readonly reference_output
cmp --silent "$output_path" "$reference_output" || {
    printf 'worker output differs from same-snapshot hb-subset + woff2_compress reference\n' >&2
    printf 'worker_sha256=%s\nreference_sha256=%s\n' \
        "$(sha256sum "$output_path" | awk '{print $1}')" \
        "$(sha256sum "$reference_output" | awk '{print $1}')" >&2
    exit 1
}

test "$(stat -c %s "$output_path")" -eq "$output_size"
printf '%s  %s\n' "$output_sha256" "$output_path" | \
    sha256sum --check --strict

if command -v woff2_decompress >/dev/null 2>&1; then
    woff2_decompress "$output_path"
    test -s "${output_path%.woff2}.ttf"
    if command -v hb-shape >/dev/null 2>&1; then
        shape="$(hb-shape "${output_path%.woff2}.ttf" '測試字型ABC')"
        [[ -n "$shape" && "$shape" != *'gid0='* ]]
    fi
fi

printf 'fontworker_image=%s\nplatform=%s\nsource_sha256=%s\noutput_size=%s\noutput_sha256=%s\n' \
    "$image" "$platform" "$source_sha256" "$output_size" "$output_sha256"
