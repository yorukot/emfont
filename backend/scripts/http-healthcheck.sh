#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
    printf 'usage: %s HOST PORT PATH\n' "$0" >&2
    exit 2
fi

host=$1
port=$2
path=$3

if [[ -z "$host" || "$host" == *[$'\r\n ']* ]]; then
    printf 'healthcheck: invalid host\n' >&2
    exit 2
fi
if [[ ! "$port" =~ ^[0-9]+$ ]] || ((port < 1 || port > 65535)); then
    printf 'healthcheck: invalid port\n' >&2
    exit 2
fi
if [[ "$path" != /* || "$path" == *[$'\r\n ']* ]]; then
    printf 'healthcheck: invalid path\n' >&2
    exit 2
fi

exec 3<>"/dev/tcp/${host}/${port}"
printf 'GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: emfont-healthcheck\r\n\r\n' \
    "$path" "$host" >&3
IFS= read -r status <&3
exec 3<&-
exec 3>&-

status=${status%$'\r'}
case "$status" in
    "HTTP/1.0 200 "* | "HTTP/1.1 200 "*) exit 0 ;;
    *)
        printf 'healthcheck: unexpected response: %s\n' "$status" >&2
        exit 1
        ;;
esac
