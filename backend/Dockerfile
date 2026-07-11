# syntax=docker/dockerfile:1.24.0@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

# Global scope fixes image config/history timestamps. Image exporters must also
# set rewrite-timestamp=true to normalize file mtimes in newly created layers.
ARG SOURCE_DATE_EPOCH=1783714861
ARG BUILDKIT_MULTI_PLATFORM=1

FROM golang:1.26.5-trixie@sha256:116489021a0d8ca3facf79f84ee69052cff88733547150a644d45c5eaa91dc43 AS build

ARG SOURCE_DATE_EPOCH
ARG GXX_VERSION=4:14.2.0-1
ARG HARFBUZZ_VERSION=10.2.0-1+deb13u1
ARG LIBSSH2_VERSION=1.11.1-1+deb13u1
ARG PKG_CONFIG_VERSION=1.8.1-4
ARG WOFF2_VERSION=1.0.2-2+b2

RUN printf '%s\n' \
        'Types: deb' \
        'URIs: http://snapshot.debian.org/archive/debian/20260710T202101Z' \
        'Suites: trixie trixie-updates' \
        'Components: main' \
        'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
        'Check-Valid-Until: no' \
        '' \
        'Types: deb' \
        'URIs: http://snapshot.debian.org/archive/debian-security/20260710T185902Z' \
        'Suites: trixie-security' \
        'Components: main' \
        'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
        'Check-Valid-Until: no' \
        > /etc/apt/sources.list.d/debian.sources \
    && rm -f /etc/apt/sources.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        g++=${GXX_VERSION} \
        libharfbuzz-dev=${HARFBUZZ_VERSION} \
        libssh2-1=${LIBSSH2_VERSION} \
        libwoff-dev=${WOFF2_VERSION} \
        pkg-config=${PKG_CONFIG_VERSION} \
    && rm -rf /var/lib/apt/lists/* /var/log/apt/* \
    && rm -f /var/cache/ldconfig/aux-cache /var/log/dpkg.log

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/emfont-controller ./cmd/controller
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/emfont-migrate ./cmd/migrate
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/emfont-fontcleanup ./cmd/fontcleanup
RUN WOFF2_LINK_VERSION="$(pkg-config --modversion libwoff2enc)" \
    && CGO_ENABLED=1 go build -trimpath \
        -ldflags="-s -w \
            -X github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzznative.woff2Version=${WOFF2_LINK_VERSION} \
            -X main.workerBuildRevision=package-manifest-probe" \
        -o /tmp/emfont-fontworker-probe ./cmd/fontworker \
    && scripts/fontworker-package-manifest.sh build \
        /tmp/emfont-fontworker-probe \
        /out/fontworker-build-packages.tsv \
        /out/fontworker-runtime-packages.tsv \
    && rm -f /tmp/emfont-fontworker-probe
RUN WORKER_GOOS="$(go env GOOS)" \
    && WORKER_GOARCH="$(go env GOARCH)" \
    && WORKER_GO_VERSION="$(go env GOVERSION)" \
    && HARFBUZZ_PACKAGE_VERSION="$(dpkg-query -W -f='${Version}' libharfbuzz-dev)" \
    && WOFF2_PACKAGE_VERSION="$(dpkg-query -W -f='${Version}' libwoff-dev)" \
    && PACKAGE_MANIFEST_DIGEST="$( \
        scripts/fontworker-package-manifest.sh digest \
            /out/fontworker-build-packages.tsv \
    )" \
    && test "$HARFBUZZ_PACKAGE_VERSION" = "$HARFBUZZ_VERSION" \
    && test "$WOFF2_PACKAGE_VERSION" = "$WOFF2_VERSION" \
    && test "${#PACKAGE_MANIFEST_DIGEST}" -eq 64 \
    && WORKER_INPUT_DIGEST="$( \
        { \
            printf '%s\n' \
                'identity-schema=3' \
                "goos=$WORKER_GOOS" \
                "goarch=$WORKER_GOARCH" \
                "go-version=$WORKER_GO_VERSION" \
                "harfbuzz-debian-version=$HARFBUZZ_PACKAGE_VERSION" \
                "woff2-debian-version=$WOFF2_PACKAGE_VERSION" \
                "package-manifest-sha256=$PACKAGE_MANIFEST_DIGEST"; \
            sha256sum \
                Dockerfile \
                go.mod \
                go.sum \
                scripts/fontworker-package-manifest.sh; \
            find \
                cmd/fontworker \
                internal/controller/infrastructure/fontbuild/harfbuzz/protocol \
                internal/controller/infrastructure/fontbuild/harfbuzznative \
                internal/platform/processsecurity \
                -type f \( -name '*.go' -o -name '*.cc' -o -name '*.h' \) \
                ! -name '*_test.go' ! -path '*/testdata/*' -print0 \
            | LC_ALL=C sort -z \
            | xargs -0 sha256sum; \
        } \
        | sha256sum \
        | cut -d ' ' -f1 \
    )" \
    && test "${#WORKER_INPUT_DIGEST}" -eq 64 \
    && WORKER_BUILD_REVISION="${WORKER_GOOS}-${WORKER_GOARCH}-${WORKER_GO_VERSION}-hb-${HARFBUZZ_PACKAGE_VERSION}-w2-${WOFF2_PACKAGE_VERSION}-src-${WORKER_INPUT_DIGEST}-pkg-${PACKAGE_MANIFEST_DIGEST}" \
    && WOFF2_LINK_VERSION="$(pkg-config --modversion libwoff2enc)" \
    && CGO_ENABLED=1 go build -trimpath \
        -ldflags="-s -w \
            -X github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzznative.woff2Version=${WOFF2_LINK_VERSION} \
            -X main.workerBuildRevision=${WORKER_BUILD_REVISION}" \
        -o /out/emfont-fontworker ./cmd/fontworker \
    && scripts/fontworker-package-manifest.sh build \
        /out/emfont-fontworker \
        /tmp/fontworker-build-packages.tsv \
        /tmp/fontworker-runtime-packages.tsv \
    && cmp /out/fontworker-build-packages.tsv \
        /tmp/fontworker-build-packages.tsv \
    && cmp /out/fontworker-runtime-packages.tsv \
        /tmp/fontworker-runtime-packages.tsv \
    && printf '%s\n' "$PACKAGE_MANIFEST_DIGEST" \
        > /out/fontworker-package-manifest.sha256 \
    && EXPECTED_IDENTITY="harfbuzz-$(pkg-config --modversion harfbuzz)-woff2-${WOFF2_LINK_VERSION}-worker-${WORKER_BUILD_REVISION}-worker-protocol-1" \
    && test "${#EXPECTED_IDENTITY}" -le 256 \
    && { printf 'EMFONTWQ\000\001\000\001'; head -c 20 /dev/zero; } \
        > /tmp/fontworker-version.request \
    && /out/emfont-fontworker \
        < /tmp/fontworker-version.request \
        > /tmp/fontworker-version.response \
    && test "$(od -An -tx1 -N32 /tmp/fontworker-version.response | tr -d ' \n')" = \
        '454d464f4e545750000100000000000000000000000000000000000000000000' \
    && VERSION_LENGTH_BYTES="$( \
        od -An -tu1 -j32 -N2 /tmp/fontworker-version.response \
    )" \
    && set -- $VERSION_LENGTH_BYTES \
    && test "$#" -eq 2 \
    && VERSION_LENGTH_HIGH="$1" \
    && VERSION_LENGTH_LOW="$2" \
    && VERSION_LENGTH="$((VERSION_LENGTH_HIGH * 256 + VERSION_LENGTH_LOW))" \
    && test "$VERSION_LENGTH" -eq "${#EXPECTED_IDENTITY}" \
    && test "$VERSION_LENGTH" -le 256 \
    && test "$(od -An -tu2 -j34 -N2 /tmp/fontworker-version.response | tr -d ' ')" -eq 0 \
    && test "$(stat -c %s /tmp/fontworker-version.response)" \
        -eq "$((36 + VERSION_LENGTH))" \
    && dd if=/tmp/fontworker-version.response \
        of=/tmp/fontworker-version.actual bs=1 skip=36 status=none \
    && printf '%s' "$EXPECTED_IDENTITY" > /tmp/fontworker-version.expected \
    && cmp /tmp/fontworker-version.expected /tmp/fontworker-version.actual \
    && rm -f /tmp/fontworker-*
RUN if ldd /out/emfont-controller 2>&1 | grep -Eiq 'harfbuzz|woff2'; then \
        echo "controller must not link native font libraries" >&2; \
        exit 1; \
    fi \
    && ldd /out/emfont-fontworker | grep -q 'libharfbuzz-subset\.so' \
    && ldd /out/emfont-fontworker | grep -q 'libwoff2enc\.so'

FROM debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2 AS runtime-base

ARG SOURCE_DATE_EPOCH
ARG CA_CERTIFICATES_VERSION=20250419
ARG HARFBUZZ_VERSION=10.2.0-1+deb13u1
ARG WOFF2_VERSION=1.0.2-2+b2
ARG BASH_VERSION=5.2.37-2+b9
ARG PASSWD_VERSION=1:4.17.4-2
ARG UTIL_LINUX_VERSION=2.41-5

RUN printf '%s\n' \
        'Types: deb' \
        'URIs: http://snapshot.debian.org/archive/debian/20260710T202101Z' \
        'Suites: trixie trixie-updates' \
        'Components: main' \
        'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
        'Check-Valid-Until: no' \
        '' \
        'Types: deb' \
        'URIs: http://snapshot.debian.org/archive/debian-security/20260710T185902Z' \
        'Suites: trixie-security' \
        'Components: main' \
        'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
        'Check-Valid-Until: no' \
        > /etc/apt/sources.list.d/debian.sources \
    && rm -f /etc/apt/sources.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates=${CA_CERTIFICATES_VERSION} \
        libharfbuzz-subset0=${HARFBUZZ_VERSION} \
        libharfbuzz0b=${HARFBUZZ_VERSION} \
        libwoff1=${WOFF2_VERSION} \
    && rm -rf /var/lib/apt/lists/* /var/log/apt/* \
    && rm -f /var/cache/ldconfig/aux-cache /var/log/dpkg.log \
    && useradd --create-home --no-log-init --uid 10001 emfont \
    && chage --lastday "$((SOURCE_DATE_EPOCH / 86400))" emfont \
    && test "$(dpkg-query -W -f='${Version}' bash)" = "${BASH_VERSION}" \
    && test "$(dpkg-query -W -f='${Version}' passwd)" = "${PASSWD_VERSION}" \
    && test "$(dpkg-query -W -f='${Version}' util-linux)" = "${UTIL_LINUX_VERSION}" \
    && test -x /usr/bin/bash \
    && test -x /usr/bin/setpriv

FROM runtime-base AS font-reference

ARG HARFBUZZ_VERSION
ARG WOFF2_VERSION

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libharfbuzz-bin=${HARFBUZZ_VERSION} \
        woff2=${WOFF2_VERSION} \
    && test "$(dpkg-query -W -f='${Version}' libharfbuzz-bin)" = \
        "$HARFBUZZ_VERSION" \
    && test "$(dpkg-query -W -f='${Version}' woff2)" = "$WOFF2_VERSION" \
    && rm -rf /var/lib/apt/lists/* /var/log/apt/* \
    && rm -f /var/cache/ldconfig/aux-cache /var/log/dpkg.log

FROM runtime-base AS runtime

WORKDIR /app
COPY --from=build /out/emfont-controller /usr/local/bin/emfont-controller
COPY --from=build /out/emfont-migrate /usr/local/bin/emfont-migrate
COPY --from=build /out/emfont-fontcleanup /usr/local/bin/emfont-fontcleanup
COPY --from=build /out/emfont-fontworker /usr/local/bin/emfont-fontworker
COPY --from=build \
    /out/fontworker-build-packages.tsv \
    /out/fontworker-runtime-packages.tsv \
    /out/fontworker-package-manifest.sha256 \
    /usr/local/share/emfont/
COPY db/migrations ./db/migrations
COPY --chmod=0555 \
    scripts/load-secrets.sh \
    scripts/http-healthcheck.sh \
    /opt/emfont/scripts/
COPY --chmod=0555 scripts/fontworker-package-manifest.sh /tmp/fontworker-package-manifest.sh

RUN EXPECTED_DIGEST="$(cat /usr/local/share/emfont/fontworker-package-manifest.sha256)" \
    && test "${#EXPECTED_DIGEST}" -eq 64 \
    && test "$(sha256sum \
        /usr/local/share/emfont/fontworker-build-packages.tsv | \
        awk '{print $1}' \
    )" = "$EXPECTED_DIGEST" \
    && /tmp/fontworker-package-manifest.sh runtime \
        /usr/local/bin/emfont-fontworker \
        /tmp/fontworker-runtime-packages.tsv \
    && cmp /usr/local/share/emfont/fontworker-runtime-packages.tsv \
        /tmp/fontworker-runtime-packages.tsv \
    && rm -f \
        /tmp/fontworker-package-manifest.sh \
        /tmp/fontworker-runtime-packages.tsv

USER emfont
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/emfont-controller"]
