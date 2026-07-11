# Hardened PostgreSQL 16 image

This directory builds a minimal child of the official PostgreSQL image. It
keeps the official entrypoint and initialization behavior, but replaces its
root-to-`postgres` `gosu` handoff with the `setpriv` binary already supplied by
Debian's `util-linux` package. The vulnerable `gosu` binary is then removed.

No packages are installed or upgraded in the child image. The complete base
filesystem is locked by the OCI index digest, and the build additionally pins
the relevant PostgreSQL and `util-linux` package versions.

## Locked inputs

- Base: `postgres:16.14-bookworm`
- OCI index: `sha256:da788743d2060767375896de4d646f7576f5911461444b372616f19ea61db2ec`
- Dockerfile frontend: `docker/dockerfile:1.24.0@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89`
- `linux/amd64`: `sha256:b78855cf2d8a6b9c3c1e78ba44f6134533f349e43a21356ecd179f6487ea255d`
- `linux/arm64`: `sha256:516c4e99c50cc8d363c5bbdf1a1ba3e18ae1a11bce0cd7020a9ed8ece6a4b50e`
- PostgreSQL server/client: `16.14-1.pgdg12+1`
- `postgresql-common`: `291.pgdg12+1`
- `util-linux`: `2.38.1-5+deb12u3`

`harden-entrypoint.sh` is a build assertion as well as the patch operation. It
checks the upstream entrypoint checksum, the architecture-specific `gosu`
checksum, package versions and ownership of `setpriv`, and the exact number of
handoff calls. It also verifies the patched checksum and performs a real
`setpriv` identity transition. Any upstream drift makes the image build fail.

The replacement remains an `exec` call and uses `--reuid`, `--regid`, and
`--init-groups`. This preserves signal delivery and the primary and
supplementary group behavior needed by the official first-volume initializer.

## Build

Build and load the native production image:

```bash
docker buildx build \
  --pull \
  --platform linux/amd64 \
  --load \
  --tag emfont-postgres:16.14-hardened \
  --file backend/docker/postgres/Dockerfile \
  backend
```

Publish an amd64/arm64 manifest after validating both builders:

```bash
docker buildx build \
  --pull \
  --platform linux/amd64,linux/arm64 \
  --push \
  --tag registry.example/emfont-postgres:16.14-hardened \
  --file backend/docker/postgres/Dockerfile \
  backend
```

## Verification

`verify.sh` rebuilds linux/amd64, initializes an empty named volume through a
read-only password secret, checks the PID 1 identity, capabilities and
data-directory ownership under `no-new-privileges`, performs SQL write/read
checks, restarts against the same volume, and runs the repository's strict
release gate with the pinned Trivy image. The blocking scan requires zero
fixable HIGH/CRITICAL findings. A second, non-blocking scan prints all
HIGH/CRITICAL findings, including those without a vendor fix, for review.

```bash
backend/docker/postgres/verify.sh emfont-postgres:16.14-hardened
```

Pass `linux/arm64` as the second argument after registering an arm64 binfmt
handler to build and execute the final arm64 runtime image.

The blocking release gate is equivalent to:

```text
trivy image --exit-code 1 --ignore-unfixed \
  --severity HIGH,CRITICAL --scanners vuln IMAGE
```

The full review report then runs without `--ignore-unfixed` and with
`--exit-code 0`. Passing the blocking gate means there are no currently
fixable HIGH/CRITICAL findings; it does not mean the image has zero total
CVEs. Unfixed findings must still be reviewed and rescanned when the
vulnerability database or Debian packages change.

The official image metadata still contains `GOSU_VERSION=1.19`; OCI image
configuration cannot delete an inherited environment key. The executable is
absent from the final root filesystem, which is asserted by both the build and
runtime smoke test.
