# Hardened MinIO Images

These images rebuild the last relevant MinIO community releases with Go
1.26.5 and the minimum direct dependency upgrades needed for a strict Trivy
scan. They do not track a branch or `latest` tag.

| Image | Upstream release | Source commit |
| --- | --- | --- |
| server | `RELEASE.2025-10-15T17-29-55Z` | `9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a` |
| mc | `RELEASE.2025-08-13T08-35-41Z` | `7394ce0dd2a80935aded936b09fa12cbb3cb8096` |

The server release is the first one containing the upstream fix for
`CVE-2025-62506`. Both Dockerfiles pin the Go builder and Debian runtime by OCI
index digest. Their resolved `go.mod` and `go.sum` hashes are asserted during
the build, so dependency resolution cannot drift silently.

The mc build also carries the seven format-string fixes from upstream commit
`ed0b962588f581ebd84d3e2a21a21f24c2b37fc1`. The reduced patch contains only
the Go 1.26 `printf` analyzer compatibility changes; unrelated modernization
from that commit is intentionally excluded. The `emfont.3` image also embeds
the repository's static `objectversionbackfill` and `minioinitcheck` helpers.
During `minio-init` it rewrites only current legacy `null` object versions
after bucket versioning is enabled, then verifies pinned before/after size,
SHA-256, metadata, tags, and version-set identity before application
credentials are provisioned.

The server build carries a narrow defensive backport for
`CVE-2026-40344`/`CVE-2026-41145`. It forces signature verification for every
`STREAMING-UNSIGNED-PAYLOAD-TRAILER` PUT path, including multipart and Snowball
extract requests. The Dockerfile asserts all vulnerable call sites before the
server tests run. Public gateways must still reject that content-SHA256 value
so the unsupported path is not internet-reachable.

The final community source release is also affected by `CVE-2026-33322` and
`CVE-2026-33419`; fixes are available only in MinIO AIStor. Emfont does not
configure OIDC or LDAP, and `minio-init.sh` fails deployment if either persisted
identity provider is enabled. The server labels these as topology-controlled,
unfixed risks rather than claiming they were patched.

Both projects are licensed under `AGPL-3.0-only`. The final images include the
upstream `LICENSE`, `NOTICE`, and `CREDITS`; the Dockerfiles provide the exact
corresponding-source and modification recipe.

## Build

Run these commands from the repository root:

```sh
docker buildx build \
  --file backend/docker/minio/Dockerfile.server \
  --tag emfont/minio-server:hardened-test \
  --load \
  backend

docker buildx build \
  --file backend/docker/minio/Dockerfile.mc \
  --tag emfont/minio-mc:hardened-test \
  --load \
  backend
```

The Dockerfiles cross-compile static Go binaries and support both production
architectures. A multi-platform final-image build requires native amd64/arm64
builder nodes or registered QEMU/binfmt handlers because Debian package
installation executes target-platform tools:

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  --file backend/docker/minio/Dockerfile.server \
  --output type=oci,dest=/tmp/emfont-minio-server.tar \
  backend

docker buildx build --platform linux/amd64,linux/arm64 \
  --file backend/docker/minio/Dockerfile.mc \
  --output type=oci,dest=/tmp/emfont-minio-mc.tar \
  backend
```

The `binary` targets validate cross-compilation without emulation and export
only the resulting executable:

```sh
docker buildx build --platform linux/arm64 --target binary \
  --file backend/docker/minio/Dockerfile.server \
  --output type=local,dest=/tmp/emfont-minio-server-arm64 \
  backend

docker buildx build --platform linux/arm64 --target binary \
  --file backend/docker/minio/Dockerfile.mc \
  --output type=local,dest=/tmp/emfont-minio-mc-arm64 \
  backend
```

Build the upstream-focused test stages with:

```sh
docker buildx build --target test \
  --file backend/docker/minio/Dockerfile.server backend
docker buildx build --target test \
  --file backend/docker/minio/Dockerfile.mc backend
```

## Compatibility

The server entrypoint preserves the official command behavior, so either
`server /data` or `minio server /data` works. It loads
`MINIO_ROOT_USER_FILE` and `MINIO_ROOT_PASSWORD_FILE` without exposing the
secret values. It validates root ownership and restrictive modes, then drops
to UID/GID 10001 with no supplementary groups before starting MinIO. The
image-owned `/data` directory is prepared for that identity; an older volume
with different ownership must be migrated before using this image. The server
image includes `curl` for the existing readiness probe. The mc image contains
`/bin/sh`, CA certificates, both init verification helpers, and all commands
used by `backend/scripts/minio-init.sh`. Stop every legacy writer before the first
upgrade backfill; the helper detects concurrent version-set changes and fails
closed, but it cannot make an external writer quiescent.

The initializer reads back and structurally verifies the complete application
policy, direct user attachment/group state, and lifecycle rule set before it
reports success. A matching policy name without matching actions and resources
is not accepted.

Run the production-contract smoke test after building both amd64 images:

```sh
backend/docker/minio/smoke.sh
```

## Vulnerability Gate

Use the repository's pinned Trivy image and do not weaken these flags:

```sh
TRIVY_IMAGE='aquasec/trivy@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f'
mkdir -p "$HOME/.cache/trivy"

docker run --rm \
  --volume /var/run/docker.sock:/var/run/docker.sock \
  --volume "$HOME/.cache/trivy:/root/.cache/" \
  "$TRIVY_IMAGE" image --exit-code 1 --ignore-unfixed \
  --severity HIGH,CRITICAL --scanners vuln \
  emfont/minio-server:hardened-test

docker run --rm \
  --volume /var/run/docker.sock:/var/run/docker.sock \
  --volume "$HOME/.cache/trivy:/root/.cache/" \
  "$TRIVY_IMAGE" image --exit-code 1 --ignore-unfixed \
  --severity HIGH,CRITICAL --scanners vuln \
  emfont/minio-mc:hardened-test
```
