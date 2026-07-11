# syntax=docker/dockerfile:1.24.0@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

# Global scope fixes image config/history timestamps. Image exporters must also
# set rewrite-timestamp=true to normalize file mtimes in newly created layers.
ARG SOURCE_DATE_EPOCH=1783714861
ARG BUILDKIT_MULTI_PLATFORM=1

FROM --platform=$BUILDPLATFORM golang:1.26.5-trixie@sha256:116489021a0d8ca3facf79f84ee69052cff88733547150a644d45c5eaa91dc43 AS objectversionbackfill-build

ARG TARGETARCH
ARG TARGETOS

ENV GOTOOLCHAIN=local
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    go mod download \
    && go mod verify

COPY cmd/objectversionbackfill ./cmd/objectversionbackfill
COPY cmd/minioinitcheck ./cmd/minioinitcheck
COPY internal/platform/objectversionbackfill ./internal/platform/objectversionbackfill
COPY internal/platform/minioinitcheck ./internal/platform/minioinitcheck

RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=1 go test -mod=readonly -race -count=1 \
        ./cmd/objectversionbackfill ./cmd/minioinitcheck \
        ./internal/platform/objectversionbackfill ./internal/platform/minioinitcheck \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
        go build -mod=readonly -trimpath -buildvcs=false \
        -ldflags='-s -w -buildid=' \
        -o /out/objectversionbackfill ./cmd/objectversionbackfill \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
        go build -mod=readonly -trimpath -buildvcs=false \
        -ldflags='-s -w -buildid=' \
        -o /out/minioinitcheck ./cmd/minioinitcheck

FROM --platform=$BUILDPLATFORM golang:1.26.5-trixie@sha256:116489021a0d8ca3facf79f84ee69052cff88733547150a644d45c5eaa91dc43 AS build

ARG SOURCE_DATE_EPOCH
ARG TARGETARCH
ARG TARGETOS

ENV GOTOOLCHAIN=local
WORKDIR /src

COPY docker/minio/mc-go1.26.patch /tmp/mc-go1.26.patch

RUN git init \
    && git remote add origin https://github.com/minio/mc.git \
    && git fetch --depth=1 origin 7394ce0dd2a80935aded936b09fa12cbb3cb8096 \
    && test "$(git rev-parse FETCH_HEAD)" = "7394ce0dd2a80935aded936b09fa12cbb3cb8096" \
    && git checkout --detach FETCH_HEAD \
    && git apply --check /tmp/mc-go1.26.patch \
    && git apply /tmp/mc-go1.26.patch

# RELEASE.2025-08-13T08-35-41Z is the latest tagged community mc release.
# Only dependencies reported in the compiled release binary are raised here.
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    go get \
        github.com/prometheus/prometheus@v0.311.3 \
        golang.org/x/crypto@v0.52.0 \
        golang.org/x/net@v0.55.0 \
        google.golang.org/grpc@v1.79.3 \
    && ldflags="$(MC_RELEASE=RELEASE go run -mod=mod buildscripts/gen-ldflags.go 2025-08-13T08:35:41Z)" \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
        go build -mod=mod -tags kqueue -trimpath -ldflags "$ldflags" -o /out/mc . \
    && printf '%s  %s\n' \
        a8a74422ee75c95ea037e3e3d215e6b3890d1c98ab7cccc0bdb12dc6d95a8ff1 go.mod \
        ff9e6666b6963a2183778f82873213412ed36e8df4b45e2ef45eae41a95ae361 go.sum \
        > /tmp/mc-modules.sha256 \
    && sha256sum -c /tmp/mc-modules.sha256 \
    && go mod verify

FROM scratch AS binary

COPY --from=build /out/mc /mc
COPY --from=objectversionbackfill-build /out/objectversionbackfill /objectversionbackfill
COPY --from=objectversionbackfill-build /out/minioinitcheck /minioinitcheck

FROM build AS test

COPY --from=objectversionbackfill-build /out/objectversionbackfill /tmp/objectversionbackfill

RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go test -mod=mod -count=1 -timeout=15m ./cmd/... ./pkg/...

FROM debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2 AS runtime

ARG SOURCE_DATE_EPOCH
ARG CA_CERTIFICATES_VERSION=20250419
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
        util-linux=${UTIL_LINUX_VERSION} \
    && rm -rf /var/lib/apt/lists/* /var/log/apt/* \
    && rm -f /var/cache/ldconfig/aux-cache /var/log/dpkg.log \
    && mkdir -p /usr/share/licenses/mc \
    && test "$(dpkg-query -W -f='${Version}' util-linux)" = "${UTIL_LINUX_VERSION}" \
    && test -x /usr/bin/setpriv

COPY --from=build /out/mc /usr/local/bin/mc
COPY --from=objectversionbackfill-build /out/objectversionbackfill /usr/local/bin/objectversionbackfill
COPY --from=objectversionbackfill-build /out/minioinitcheck /usr/local/bin/minioinitcheck
COPY --from=build /src/LICENSE /usr/share/licenses/mc/LICENSE
COPY --from=build /src/NOTICE /usr/share/licenses/mc/NOTICE
COPY --from=build /src/CREDITS /usr/share/licenses/mc/CREDITS
COPY --chmod=0555 \
    scripts/load-secrets.sh \
    scripts/minio-init.sh \
    /opt/emfont/scripts/

LABEL org.opencontainers.image.title="Emfont hardened MinIO client" \
      org.opencontainers.image.description="MinIO mc rebuilt with patched Go dependencies" \
      org.opencontainers.image.source="https://github.com/yorukot/emfont" \
      org.opencontainers.image.url="https://github.com/minio/mc/tree/RELEASE.2025-08-13T08-35-41Z" \
      org.opencontainers.image.version="RELEASE.2025-08-13T08-35-41Z-emfont.3" \
      org.opencontainers.image.revision="7394ce0dd2a80935aded936b09fa12cbb3cb8096" \
      org.opencontainers.image.licenses="AGPL-3.0-only"

ENV HOME=/tmp \
    MC_CONFIG_DIR=/tmp/.mc

ENTRYPOINT ["/usr/local/bin/mc"]
CMD ["--help"]
