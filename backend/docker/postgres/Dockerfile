# syntax=docker/dockerfile:1.24.0@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

# Global scope fixes image config/history timestamps. Image exporters must also
# set rewrite-timestamp=true to normalize file mtimes in newly created layers.
ARG SOURCE_DATE_EPOCH=1783714861
ARG BUILDKIT_MULTI_PLATFORM=1

# This OCI index contains both linux/amd64 and linux/arm64 manifests. Keep the
# digest synchronized with the assertions in harden-entrypoint.sh.
FROM postgres:16.14-bookworm@sha256:da788743d2060767375896de4d646f7576f5911461444b372616f19ea61db2ec

LABEL org.opencontainers.image.title="Emfont hardened PostgreSQL 16" \
      org.opencontainers.image.description="PostgreSQL 16 with the upstream gosu handoff replaced by util-linux setpriv" \
      org.opencontainers.image.version="16.14-emfont.1" \
      org.opencontainers.image.base.name="docker.io/library/postgres:16.14-bookworm" \
      org.opencontainers.image.base.digest="sha256:da788743d2060767375896de4d646f7576f5911461444b372616f19ea61db2ec"

# The helper is bind-mounted into this build step, so it does not remain in an
# image layer. It rejects upstream entrypoint or package drift before patching.
RUN --mount=type=bind,source=docker/postgres/harden-entrypoint.sh,target=/tmp/harden-entrypoint.sh,readonly \
    /bin/sh /tmp/harden-entrypoint.sh

COPY --chmod=0555 \
    scripts/load-secrets.sh \
    scripts/postgres-permissions.sh \
    /opt/emfont/scripts/
