# Backend production runbook

This runbook operates the Go controller with the shipped
`docker-compose.backend.yml`. That manifest is specifically a single-host,
self-managed deployment with bundled PostgreSQL and MinIO; it is not a generic
manifest for managed S3. Use the bundled stateful services only after their
exact images pass the release gates below and the accepted RPO/RTO permits loss
of one Docker host.

Managed PostgreSQL and a managed, versioned S3-compatible service may be
preferred production dependencies, but they require a separately reviewed,
orchestrator-specific manifest or override. For external object storage,
pre-provision and independently verify bucket versioning, the least-privilege
application IAM principal and policy, lifecycle rules, backups, and restore
behavior. Omit the bundled `minio` and `minio-init` services and their dependency
edges. Provide network reachability from the controller and separately reviewed
migration/verification tooling to the external endpoint. The bundled
`minio-init` is MinIO-specific; it does not provision a generic managed S3
service, and pointing the current Compose file at one is not a supported
external-store deployment.

This stack deploys only the Go backend. It does not serve or replace the legacy
Node web UI, admin pages, bulletin, lorem page, or frontend static assets. The
frontend, legacy admin writer, and their background jobs remain separate
deployments and must be included explicitly in maintenance and recovery plans.

## Non-negotiable release gates

A release is not production-ready until all of these gates are recorded for
the exact deployment configuration:

- Compose receives a separate repository and 64-hex SHA-256 value for each of
  the backend, PostgreSQL, MinIO, and `mc` images and structurally renders each
  reference as `repository@sha256:<digest>`. The eight values and
  `EMFONT_VERSION` come only from the verified immutable release manifest
  produced by `backend-release.yml`; operators do not type or copy them from a
  tag. A mutable tag, including the workflow's promoted version tag, is not a
  deployment identity or input.
- The GitHub `backend-production` environment exists before dispatch, allows
  only the branch deployment policy `main` (exactly `refs/heads/main`, with no
  tag policy), requires independent production reviewers, has
  `prevent_self_review=true` and `can_admins_bypass=false`, and protects the
  post-gate promotion job. GHCR write/admin access for all four packages is
  restricted to the release workflow and named release administrators. Public
  package visibility or a deployment host's pull credential does not grant
  publish permission.
- Repository and workflow writers, the default-branch/ruleset administrators,
  and GHCR package writers/admins are external fail-closed controls. Workflow
  YAML cannot protect itself from a malicious trusted principal that can alter
  the workflow or replace registry state. Before release, `main` is the default
  branch, branch governance is enabled, and immutable evidence names the
  minimal users, teams, and GitHub Apps holding each of those authorities.
  Missing, wildcard, unexplained, or broader-than-required authority blocks the
  release.
- The eight-file release manifest reports the exact release run attempt and has
  every platform, SBOM, provenance, reproducibility, signature, and
  post-approval reverification boolean set to `true`. The operator validates
  those claims, decodes and validates the retained environment and CVE
  acceptance evidence, and independently re-verifies each exact digest from
  the deployment network. A boolean without its hash-bound retained evidence
  does not pass.
- The backend image and every bundled PostgreSQL, MinIO, and `mc` image report
  zero **fixable** HIGH/CRITICAL findings. Generate a complete Trivy JSON report
  without filtering unfixed findings, then run the release's deterministic
  `workflow-trivy-gate.sh` against it, using a current vulnerability database on
  every deployed platform.
- A second Trivy report for each exact digest retains all severities and all
  unfixed findings. A named security reviewer compares it with the prior
  accepted report and records the owner, rationale, compensating controls,
  ticket, and review expiry for every accepted unfixed risk. Any unreviewed
  finding or delta blocks deployment. Do not describe this gate as zero total
  findings unless that complete report is actually empty.
- The exact backend image passes unit, race, integration, official-font,
  multi-pod fencing, container runtime, and migration tests.
- The final-image font gate runs the built controller and its bundled worker,
  not the Ubuntu host worker. For the pinned official Noto Sans TC source and
  `測試字型ABC`, the HarfBuzz 10.2.0/WOFF2 1.0.2 production image must produce
  2,868 bytes with SHA-256
  `3e365346851cf540ccbef2b61ca7c05c51ff93833c8a928c5a816884373819e2`.
  The host-library integration reference is a separate test and is not a
  production hash oracle.
- PostgreSQL is at schema migration 10 before this controller receives traffic.
- Production schema changes are forward-only. Down migrations 2 through 6, 8,
  9, and 10 discard persisted schema or data. Migration 7's down is an
  intentional no-op and cannot reverse its lossy legacy reconciliation.
  Migration 1 drops
  persistent metadata. None is an application rollback mechanism; the exact
  scope is listed in Rollback. Running any down migration requires a verified
  coordinated backup and every controller, cleanup process, canary, importer,
  and legacy writer to be stopped.
- Object storage has bucket versioning enabled. The controller deliberately
  rejects generated-object publication when the store does not return a real
  version ID.
- For bundled MinIO, the release's `minio-init` implementation must finish with
  a fail-closed read-back of the final state. Success must mean the dedicated
  bucket has exactly the intended enabled `_generated/` lifecycle rule, with
  the configured noncurrent expiry and expired-delete-marker cleanup, and no
  additional rules. It must also mean the configured application principal is
  enabled, identifies the expected access key, has exactly the intended policy
  attachment, and has no group membership. The attached policy document must
  contain exactly the documented bucket-read, object-read, and generated-object
  mutation action/resource sets, with no additional statement or condition.
  A script that only issues lifecycle, user, or policy mutation commands without
  verifying their resulting state does not satisfy this gate and blocks
  production until it is fixed and tested.
- Before the first upgraded controller that requires versioned source objects,
  every legacy/admin writer is stopped. `minio-init` enables versioning, then
  idempotently rewrites every current null-version object to obtain a real
  version ID while byte-hash-verifying that the rewrite preserved content. A
  failed, skipped, or incomplete backfill blocks controller startup. Retain its
  object counts and hash evidence and prove a second run is a no-op. The
  controller's rejection of a null source version remains fail-closed after
  migration.
- The release has a verified, off-host coordinated backup and a current restore
  drill. Versioning is not a backup.
- Before the first production release, a current-tree Gitleaks scan is clean
  and the historical finding identified only by fingerprint
  `ad83a5f8f591bae6abc6c65a46f53797af7e0f48:.env.example:generic-api-key:18`
  has signed owner evidence that it was synthetic and never used, or proof that
  every affected credential was rotated or revoked. Never place the detected
  value in evidence. Rewriting history is not a substitute for rotation or
  revocation.
- The v4 cache warm-up and canary gates in this runbook pass. A low cache hit
  ratio, sustained build queue, or elevated HTTP 429/503 rate blocks promotion.
- The upstream 2025 community MinIO release is affected by CVE-2026-40344 and
  CVE-2026-41145 unauthenticated-write risks. The `emfont.3` server rebuild
  applies the repository source patch, but Trivy still flags the upstream
  version range. Direct public or untrusted-network exposure remains blocked;
  object delivery must pass through a gateway that permits only `GET` and
  `HEAD`. This topology rule cannot be waived through risk acceptance.
- The same release also has unfixed HIGH findings CVE-2026-34204 (replication
  header metadata injection) and CVE-2026-39414 (S3 Select CSV allocation DoS).
  They are not patched by the `emfont.3` source patch. The bundled topology
  permits no public or untrusted path to the MinIO origin, the controller and
  its application policy do not use replication or S3 Select, and the public
  gateway permits only object `GET` and `HEAD`. Each CVE still requires its own
  signed, unexpired acceptance for the exact MinIO digest and deployed platform;
  these feature exclusions are compensating controls, not a finding waiver.
- The same final community release is affected by CVE-2026-33322 (OIDC JWT
  algorithm confusion) and CVE-2026-33419 (LDAP login brute force). Their fixes
  exist only in MinIO AIStor. The bundled deployment must keep both identity
  providers disabled; `minio-init` checks persisted server configuration and
  fails closed. Enabling either provider requires replacing bundled MinIO with
  a reviewed fixed service.

### External release evidence

The following gates depend on GitHub control-plane, registry, backup-provider,
security-review, and deployed-network state outside this repository. CI output
or a working-tree file cannot satisfy them. In particular, a workflow cannot
prove that a trusted repository/workflow writer or GHCR package writer/admin is
non-malicious: those principals can change the program making the claim or the
published state it consumes. Treat their least-privilege governance as an
external, fail-closed prerequisite. Store the signed records in the release
evidence system and put their immutable IDs in the change ticket. A
placeholder, mutable URL, local image ID, or statement that evidence will be
added later keeps the release closed.

- **Control-plane authority, published identity, and provenance:** Record that
  the repository default branch is `main`; the ruleset/branch protection for
  `main`; whether administrators or named actors can bypass it; and the exact
  named users, teams, and GitHub Apps with repository `write`, `maintain`, or
  `admin`, Actions/workflow-write authority, ruleset/default-branch authority,
  and write/admin authority on each of the four GHCR packages. The inventory
  must be minimal, reviewed, and current before dispatch. An organization role
  or team is not evidence unless its effective members and relevant nested
  access are recorded. Also record, for the backend, PostgreSQL, MinIO server,
  and `mc`, the exact
  `registry/repository@sha256:<published-OCI-digest>`, target OS/architecture,
  resolved platform-manifest digest, image-config digest, registry, and publish
  time. For a multi-platform image, the deployed reference is the OCI index
  digest and the record also names the selected platform digest. Retain
  registry resolution output from the deployment network. Retain verified
  provenance whose subject is that exact published digest, including the
  provenance artifact digest, source commit, trusted builder identity,
  workflow/run ID, build inputs, and SBOM digest. A provenance statement for a
  tag, local image, source commit alone, or a different digest does not pass.
  The retained `backend-release-<version>` artifact from a successful
  `Backend Release` run is the deployment authority. It has exactly eight
  files: `images.env`, `release.env`, `verification.env`, `SHA256SUMS`,
  `docker-compose.backend.yml`, `compose-contract.env`, `compose-config.json`,
  and `verify-compose-release.sh`. The checksum file seals the other seven;
  together they identify the exact
  digests, source commit, version, release run and run attempt, source CI run,
  platform gates, SBOM/provenance/reproducibility results, signatures, and
  post-approval reverification. No other file may supply image values to
  Compose. The same record includes the `backend-production` environment rule
  snapshot showing only branch `main`, `prevent_self_review=true`, and
  `can_admins_bypass=false`; the independent required-review approval bound to
  the source SHA, release run attempt, and canonical CVE acceptance SHA-256;
  package visibility; package writer/admin principals; and either anonymous
  exact-digest resolution for all four public packages or the deployment
  credential identity and `read:packages` scope.
- **Off-host recovery point:** Record the backup ID, SHA-256 of
  `backup-manifest.json` and `SHA256SUMS`, exact off-host object/snapshot URI,
  provider account, region and failure domain, immutability/retention policy,
  upload verification, retrieval verification, and the latest successful
  isolated restore-drill ID with measured RPO/RTO. `/srv/backups` on the Docker
  host is staging, not this gate.
- **Unfixed-CVE acceptance:** Attach the full scanner report and a signed,
  unexpired acceptance for every accepted unfixed finding. Each acceptance
  names the exact image digest and platform, CVE, affected package/version,
  rationale, compensating controls, accountable owner, security reviewer,
  ticket, and expiry/review date. It does not transfer to another digest,
  platform, scanner delta, or CVE. Fixable HIGH/CRITICAL findings remain
  ineligible for acceptance.
- **Deployed gateway evidence:** Record the rendered method-policy/configuration
  hash, gateway release identity, public-edge and internal-object-store network
  IDs, runtime proof that the gateway is attached to both, runtime proof that
  MinIO is attached only to the latter with no host port, external probe
  artifact hash, gateway access-log export, and origin audit-log export
  described below. The origin export must contain the allowed control request
  and zero forbidden probe paths.
- **First-release secret-history disposition:** Before the first production
  release, retain the redacted successful current-tree Gitleaks report and a
  signed record from the accountable owner for fingerprint
  `ad83a5f8f591bae6abc6c65a46f53797af7e0f48:.env.example:generic-api-key:18`.
  The record either establishes that the finding was synthetic and never used
  in any environment, or identifies the affected credential and supplies
  rotation/revocation proof. It must not reproduce the detected value. A
  rewritten or deleted Git history does not establish non-use and does not
  replace credential rotation or revocation.

The first four records are required for the exact candidate before any
production traffic promotion; the secret-history disposition is additionally
required before the first production release. Reusing a prior release's record
requires an explicit proof that every authority, digest, platform,
configuration hash, backup ID, report, acceptance, and deployed control named
by that record is unchanged and still current.

The exact references in a verified release manifest are inputs to these gates,
not evidence that every external gate passed. Replace any failing bundled image
through a new release workflow run, or deploy a managed service through the
separate orchestration architecture described above. Do not edit a manifest,
point the bundled Compose stack at a managed service, or deploy a
known-vulnerable digest merely because Compose can render it.

The coordinated backup/restore gate intentionally remains operator-only. A CI
runner can prove `pg_dump` syntax or copy current objects, but it cannot prove
off-host durability, provider snapshot atomicity, retention/immutability,
complete MinIO version history with stable version IDs, production RPO/RTO, or
that every external writer was quiesced. Such a smoke would create false
release evidence. Attach the latest isolated restore-drill record described in
the Backup and Recovery sections to every release instead.

## Topology and invariants

- `postgres` and `minio` hold persistent state in named volumes.
- `migrate` is a one-shot Goose migration using the release image.
- `postgres-permissions` is a one-shot role/grant reconciliation job. Its
  root-only staging entrypoint loads both password files and then uses `setpriv`
  to run the identity-checked SQL helper as UID/GID 10001. Migrations use the
  database admin; the controller uses a separate non-superuser role.
- `minio-init` is a one-shot bucket, versioning, legacy null-version object
  backfill, app-user, policy, and lifecycle reconciliation job for the bundled
  MinIO service. The backfill runs only after versioning is enabled, rewrites
  current null-version objects idempotently, and byte-hash-verifies every
  rewrite. It removes every lifecycle rule before installing the
  controller-owned `_generated/` rule. The bucket must therefore be dedicated
  to Emfont and must not contain lifecycle rules owned by another application.
  The production-authorized script must then fail closed unless final read-back
  proves both the exact lifecycle configuration and the exact least-privilege
  app-principal state described above. Only a release in which that behavior is
  tested may treat an initializer exit as deployment authority. It also blocks
  startup when persisted OIDC or LDAP
  identity configuration is enabled because the final community release has no
  fix for CVE-2026-33322 or CVE-2026-33419.
- `controller` starts only after PostgreSQL and MinIO are healthy and both
  provisioning jobs plus migrations through version 10 completed successfully.
- `/api/v1/livez` checks the process. `/api/v1/readyz` checks PostgreSQL, the
  migration-10 font schema, including reconciled legacy columns, persisted
  reservations, the generated `quota_bytes` column, singleton quota ledger,
  accounting/locking triggers, bounded terminal-failure table, and the
  configured object bucket. Readiness is necessary but does not replace an
  explicit Goose version check, a ledger consistency check, or an end-to-end
  generation and gateway download probe. Successful dependency results are
  cached for 5 seconds and failed results for 2 seconds. All three health
  routes use dedicated 5 requests/second, burst-10 buckets per trusted client
  IP. Public `/livez` additionally has a 100 requests/second, burst-200
  process-wide cap. Private readiness uses a separate bucket, so public
  liveness saturation cannot consume orchestrator probe capacity.
- PostgreSQL is only on the internal `database` network. MinIO is only on the
  internal, attachable `object-store` network and has no host-published API or
  console port. The controller alone publishes its API on `127.0.0.1`. A
  separately managed object gateway must have two network attachments: one to
  its public/TLS edge and one to `object-store`, with `http://minio:9000` as its
  private origin. It must enforce a positive `GET`/`HEAD` allowlist before
  proxying. Never add a MinIO `ports` entry or route an untrusted network to
  the origin.
- All deployed images are immutable digest references and have passed both the
  fixable-vulnerability gate and full-report review for the deployed platform.
- Database and MinIO credentials are mounted as Docker Compose secrets. They
  are not stored as values in the deployment environment file.
- Every host secret source is root:root `0600`. Backend containers stage only
  their assigned files into private tmpfs as root:root `0400`; UID 10001 and
  the native worker must be unable to read both paths. CI and deployment
  verification treat any ownership/mode/readability mismatch as a hard gate.
- The migration entrypoint starts with only `SETUID` and `SETGID`. The
  controller additionally grants `KILL` to Docker's root init so PID 1 can
  forward shutdown signals after its child drops to UID/GID 10001. The
  entrypoint reads root-owned secret files and drops privileges before
  executing a backend binary; the binary has no effective Linux capabilities.
- The bundled MinIO entrypoint follows the same root-only secret-read pattern,
  then drops to UID/GID 10001 before serving. `/data` is owned by that identity,
  and the service uses a read-only root filesystem with only `/tmp` and `/data`
  writable.
- The controller sets `PR_SET_DUMPABLE=0` before application startup, so a
  same-UID process cannot read its procfs environment. The font worker sets
  `NO_NEW_PRIVS` and an architecture-checked seccomp filter that returns
  `EPERM` for filesystem open/mutation/execute, network, namespace/mount,
  cross-process inspection, process creation, process-group escape, and
  cross-process signal and resource-control syscalls (`prlimit64`,
  `setpriority`, `sched_set*`, and `ioprio_set`) while retaining thread
  creation and inherited standard-I/O pipes. Unit gates exercise these
  denials, and the final-image font smoke proves the sandboxed worker still
  builds fonts.
- Production cannot disable the per-client and process-wide rate limiters.
  Their limits remain process-local, so a multi-replica deployment also needs
  a shared edge limiter.
- PostgreSQL uses a read-only root filesystem and a reduced capability set.
  The database app role has no `TEMPORARY` database privilege, schema-create,
  administration, role, or Goose access. It may read the quota ledger and
  update only its `singleton` key for row locking; it cannot update
  `artifact_count` or `accounted_bytes`, which are maintained only by the
  migration-9 security-definer triggers.
- The intended MinIO controller policy can list the configured bucket, read
  objects, and write only `_generated/*` objects. It cannot delete any object,
  administer MinIO, or create buckets. A separate cleanup principal can list,
  read, and delete only `_generated/*`. Final principal read-back must show
  each expected enabled access-key identity, exactly its intended policy
  attachment, and no group membership that could add rights.
- Static generation is accepted only for complete plans with at most 32 packs
  and at most 256 codepoints in each pack. Oversized, malformed, or
  incomplete plans fall back to a dynamic subset. Static CSS emits one
  `@font-face` per pack with that pack's `unicode-range`; builds run in parallel
  only within `EMFONT_FONT_BUILD_CONCURRENCY` and pending-build bounds.
- Generated-object noncurrent versions and delete markers expire through a
  MinIO lifecycle rule. This is required because deleting a versioned object
  alone does not reclaim its older versions.

At the TLS reverse proxy, route the public `/api/v1/*` application endpoints
and compatibility endpoints `/g/*`, `/css/*`, `/list`, and `/info/*` to the Go
controller, but explicitly keep `/api/v1/readyz` and `/api/v1/healthz` private.
Route UI, legacy admin, and static-asset paths to their separate deployments.
Test every route group before removing any legacy process.

The shipped Compose topology and its bundled MinIO initializer enable bucket
versioning. Versioning is not a backup: capacity alerts and independent,
off-host backups remain mandatory.

## Prerequisites

- Linux with Docker Engine 24 or newer, Docker Buildx, and Docker Compose 2.24
  or newer.
- A dedicated host or VM with persistent disks, UTC time synchronization, and
  enough free space for PostgreSQL, MinIO versions, and local backup staging.
- A TLS reverse proxy for the controller. Production object downloads require
  a separately secured HTTPS CDN or gateway that exposes only object
  `GET`/`HEAD`. For the bundled stack that gateway has a public/TLS attachment
  and a second attachment to the Compose `object-store` network; the bundled
  MinIO endpoint itself must not be client-reachable.
- Trivy with a current vulnerability database, plus `gh`, `cosign`, `gitleaks`,
  `jq` 1.6, Python 3, `curl`, `flock`, `sha256sum`, and `tar` on the operator host.
  Configuration validation uses Python's standard-library IP/CIDR parser.
  Authenticate `gh`
  to read workflow runs, attestations, artifacts, and packages for
  `yorukot/emfont`. Before any pull, either prove all four GHCR packages are
  public by resolving their exact digests anonymously from the deployment
  network, or authenticate Docker with a dedicated least-privilege token that
  has `read:packages` only (and organization SSO authorization when required).
  A workflow `GITHUB_TOKEN` is not a deployment-host credential.
- The checksummed verified release manifest from the exact successful
  `Backend Release` run. A green workflow, signature, provenance statement, or
  manifest for another digest is not acceptable evidence.
- Off-host backup storage and a documented recovery owner.
- A complete writer inventory covering all controller replicas and canaries,
  legacy Node/admin services, importers, migrations, and cleanup schedulers.

Do not expose PostgreSQL, the MinIO API/console, or the controller directly to
the Internet. Production Compose publishes only the controller on loopback;
MinIO and PostgreSQL have no host port. A topology that adds an origin bind is
not this reference deployment and requires a separately reviewed Compose file
and threat model; the bundled MinIO origin may not be exposed to public or
untrusted networks. Production Compose also pins proxy-header trust
on, so its sole controller path must be a trusted proxy that discards the
client-supplied forwarding chain and writes a new `X-Forwarded-For` value from
the authenticated peer address. Set
`EMFONT_TRUSTED_PROXY_CIDRS` to the smallest CIDR allowlist that contains only
the controller's observed immediate proxy peers. Do not list client, office,
VPN, or CDN egress ranges unless those addresses connect directly to the
controller. The application rejects malformed/non-canonical networks, CIDRs
with host bits, IPv4-mapped IPv6 CIDRs, and every `/0` prefix.

### First-release secret-history gate

Run a redacted Gitleaks filesystem scan from the repository root against the
current tree immediately before authorizing the first production release. Pin
and record the approved Gitleaks version/configuration with the report. Any
finding fails the gate; the known historical fingerprint is not an allowlist
for a current-tree finding.

```bash
set -Eeuo pipefail
gitleaks dir --no-banner --redact --exit-code 1 .
```

Separately, the accountable owner must sign the historical disposition for
fingerprint
`ad83a5f8f591bae6abc6c65a46f53797af7e0f48:.env.example:generic-api-key:18`.
The disposition must prove either that the finding was synthetic and never
used, or that every potentially affected credential was rotated or revoked.
Reference the evidence ID without copying the detected value into the report,
change ticket, shell, or this runbook. Deleting or rewriting Git history can
reduce future exposure, but it neither proves non-use nor substitutes for
rotation or revocation.

## Verified release input

`backend-release.yml` publishes candidate multi-platform images, emits SBOM
and provenance attestations, signs each exact digest with GitHub OIDC, verifies
the exact digests with signature/provenance plus runtime, font, and
vulnerability gates on both linux/amd64 and linux/arm64, may update mutable
version tags only as non-authoritative registry conveniences after both
platform jobs pass, and finally uploads
`backend-release-<version>`. That final artifact is the only allowed source of
production image values. Its `images.env` contains the four exact references;
`release.env` contains `source_commit`, `version`, `verification_run`, and
`verification_run_attempt`; `verification.env` records the source CI identity,
both platform gates, scanner/attestation/reproducibility results, signatures,
post-approval reverification, and the Compose contract. The artifact also
contains the exact `docker-compose.backend.yml`, `compose-contract.env`,
canonical `compose-config.json`, and `verify-compose-release.sh`; `SHA256SUMS`
seals all seven payload files. Those eight files are one indivisible contract.
Candidate tags and promoted version tags are never read by deployment
automation.

`workflow_dispatch` is the release workflow's only trigger. Only a successful
run whose source ref is exactly `refs/heads/main` and whose workflow and source
SHA are the selected `main` commit is eligible. The protected
`backend-production` environment has exactly one deployment branch policy,
branch `main`; it has no tag policy or tag-triggered authority. Candidate and
version tags remain mutable, non-authoritative outputs and never produce
deployment authority.

### CVE acceptance input and signed approval

`EMFONT_CVE_ACCEPTANCE_B64` is a GitHub Actions environment variable on
`backend-production`, not a repository file, secret value, or dispatch input.
It is RFC 4648 base64 without whitespace of one UTF-8 JSON document. The
encoded value is at most 65,536 bytes and the decoded document at most 49,152
bytes. Before hashing, canonicalize it with `jq --sort-keys --compact-output`.
The document has exactly the top-level keys `acceptances`, `schema`, and
`source_sha`; `schema` is `emfont.cve-acceptance/v1`, and both the document and
every entry use the exact release source SHA.

Each acceptance has exactly these keys:

```text
class compensating_controls component expires_at fixed_version image
installed_version owner package platform rationale report_sha256
security_reviewer severity source_sha status target ticket type
vulnerability_id
```

The finding identity is the exact tuple of `class`, `component`,
`fixed_version`, `image`, `installed_version`, `package`, `platform`,
`report_sha256`, `severity`, `source_sha`, `status`, `target`, `type`, and
`vulnerability_id` emitted by the post-approval Trivy scan. The acceptance set
must equal the required finding set: missing, duplicate, or additional
identities fail. Only unfixed HIGH/CRITICAL findings are eligible, so
`fixed_version` is empty. An acceptance never transfers to another report,
digest, platform, source SHA, package version, or scanner result.

The review fields are also mandatory. `owner` is
`github:user/<login>` or `github:team/<organization>/<team-slug>`;
`security_reviewer` is an independent `github:user/<login>`; `rationale` is
20-1,000 non-control characters; `compensating_controls` contains 1-8 unique,
nonblank strings of 10-500 non-control characters; `ticket` is a 3-256
character non-whitespace identifier containing an alphanumeric character; and
`expires_at` is a future UTC timestamp in `YYYY-MM-DDTHH:MM:SSZ` form no more
than 30 days from validation. Fixable HIGH/CRITICAL findings cannot be accepted.
When no acceptance is required, the environment variable must be empty and the
manifest records count `0`, SHA `none`, and evidence `none`.

Before approving the `backend-production` job, compute the SHA-256 of the
canonical document (`none` when the acceptance count is zero). Every approving
reviewer uses this exact GitHub environment approval comment, with no extra
text:

```text
emfont-release-approval/v1 source_sha=<40-hex-source-sha> run_attempt=<current-positive-run-attempt> cve_acceptance_sha256=<none-or-sha256:64-hex>
```

The approver must differ from the workflow actor, and every named
`security_reviewer` must approve as that GitHub user with the same exact
comment. A rerun requires a new comment with its current run attempt. The
authenticated GitHub environment approval, retained inside the hash-bound
environment evidence together with the canonical acceptance digest, is the
signed acceptance evidence. A ticket attachment, JSON signature claim, or
boolean in `verification.env` does not replace it.

Download by exact successful workflow run ID, validate the run and file set,
verify every digest again from the deployment network, and generate the split
Compose variables without sourcing either manifest file:

For private GHCR packages, log in first with a dedicated machine identity and
a classic personal access token limited to `read:packages`; do not grant
`write:packages`, `delete:packages`, repository write, or workflow permissions:

```bash
read -r -p 'GHCR deployment user: ' GHCR_DEPLOY_USER
read -r -s -p 'GHCR read:packages token: ' GHCR_DEPLOY_TOKEN
printf '\n' >&2
printf '%s' "$GHCR_DEPLOY_TOKEN" | \
  docker login ghcr.io --username "$GHCR_DEPLOY_USER" --password-stdin
unset GHCR_DEPLOY_TOKEN
```

Set `EMFONT_GHCR_ACCESS_MODE=public` only when all four packages are intended
to be public; the procedure then uses an empty Docker config to prove anonymous
exact-digest resolution. Otherwise set it to `authenticated` and retain the
deployment identity/scope review as external evidence.

```bash
export EMFONT_RELEASE_VERSION=v1.2.3
export EMFONT_RELEASE_RUN_ID=123456789
export EMFONT_GHCR_ACCESS_MODE=public

(
  set -Eeuo pipefail
  umask 077
  repository=yorukot/emfont
  version=${EMFONT_RELEASE_VERSION:?Set the verified release version}
  run_id=${EMFONT_RELEASE_RUN_ID:?Set the exact successful workflow run ID}
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z]+)*$ ]]
  [[ "$run_id" =~ ^[0-9]+$ ]]

  stage="$(mktemp -d)"
  trap 'rm -rf "$stage"' EXIT
  manifest_dir="$stage/release-manifest"
  install -d -m 0700 "$manifest_dir"

  gh api --method GET "repos/$repository/actions/runs/$run_id" \
    >"$stage/run.json"
  jq -e --argjson run_id "$run_id" '
    .id == $run_id and
    .conclusion == "success" and
    .name == "Backend Release" and
    .path == ".github/workflows/backend-release.yml" and
    .event == "workflow_dispatch" and
    .head_branch == "main" and
    (.head_sha | test("^[0-9a-f]{40}$")) and
    (.html_url | type == "string" and length > 0) and
    (.run_attempt | type == "number" and . >= 1 and floor == .)
  ' "$stage/run.json" >/dev/null
  gh run download "$run_id" --repo "$repository" \
    --name "backend-release-$version" --dir "$manifest_dir"

  expected_files=$'SHA256SUMS\ncompose-config.json\ncompose-contract.env\ndocker-compose.backend.yml\nimages.env\nrelease.env\nverification.env\nverify-compose-release.sh'
  actual_files="$(find "$manifest_dir" -mindepth 1 -maxdepth 1 \
    -type f -printf '%f\n' | LC_ALL=C sort)"
  [[ "$actual_files" == "$expected_files" ]]
  [[ -z "$(find "$manifest_dir" -mindepth 1 -maxdepth 1 \
    ! -type f -print -quit)" ]]
  expected_sum_paths=$'release-manifest/compose-config.json\nrelease-manifest/compose-contract.env\nrelease-manifest/docker-compose.backend.yml\nrelease-manifest/images.env\nrelease-manifest/release.env\nrelease-manifest/verification.env\nrelease-manifest/verify-compose-release.sh'
  actual_sum_paths="$(awk '{print $2}' "$manifest_dir/SHA256SUMS" | \
    LC_ALL=C sort)"
  [[ "$actual_sum_paths" == "$expected_sum_paths" ]]
  (cd "$stage" && sha256sum --check --strict \
    release-manifest/SHA256SUMS)

  manifest_value() {
    local key=$1
    local file=$2
    local values=()
    mapfile -t values < <(awk -v key="$key" '
      index($0, key "=") == 1 {
        sub(/^[^=]*=/, "")
        print
      }
    ' "$file")
    ((${#values[@]} == 1))
    [[ -n "${values[0]}" ]]
    printf '%s' "${values[0]}"
  }

  validate_env_keys() {
    local file=$1
    local expected=$2
    local actual
    actual="$(awk -F= '
      /^[A-Za-z_][A-Za-z0-9_]*=/ {
        key=$1
        sub(/^[^=]*=/, "")
        if (length($0) == 0) exit 3
        print key
        next
      }
      { exit 2 }
    ' "$file" | LC_ALL=C sort)"
    [[ "$actual" == "$expected" ]]
    [[ -z "$(printf '%s\n' "$actual" | uniq --repeated)" ]]
  }

  expected_verification_keys="$(printf '%s\n' \
    amd64_gate_approved \
    amd64_gate_job \
    arm64_gate_approved \
    arm64_gate_job \
    buildkit_attestation_platforms \
    buildkit_image \
    buildkit_provenance_verified \
    buildkit_sbom_generator \
    buildkit_sbom_verified \
    buildx_binary_sha256 \
    buildx_host_platform \
    buildx_independent_rebuild_tooling_verified \
    buildx_postapproval_tooling_verified \
    buildx_repository \
    buildx_version \
    compose_config \
    compose_config_sha256 \
    compose_contract_env \
    compose_contract_env_sha256 \
    compose_contract_verified \
    compose_file \
    compose_file_sha256 \
    compose_generator_version \
    compose_verification_command \
    compose_verification_required_for_deploy \
    compose_verification_required_for_rollback \
    compose_verifier \
    compose_verifier_sha256 \
    cve_acceptance_contract_schema \
    cve_acceptance_count \
    cve_acceptance_document_schema \
    cve_acceptance_evidence_base64 \
    cve_acceptance_sha256 \
    cve_acceptance_source \
    cosign_negative_fixtures_reverified_after_approval \
    cosign_negative_fixtures_verified \
    cosign_certificate_identity \
    cosign_certificate_oidc_issuer \
    cosign_signature_job \
    cosign_signature_stage \
    cosign_signatures_reverified_after_approval \
    cosign_signatures_verified \
    cosign_signed_references \
    deployment_identity \
    external_writer_toctou_eliminated \
    github_attestations_reverified_after_approval \
    github_build_provenance_verified \
    minio_security_labels_verified_platforms \
    promotion_environment \
    promotion_environment_admin_bypass \
    promotion_environment_approval_count \
    promotion_environment_deployment_policies \
    promotion_environment_evidence_base64 \
    promotion_environment_evidence_sha256 \
    promotion_environment_id \
    promotion_environment_prevent_self_review \
    promotion_environment_required_reviewer_count \
    promotion_environment_reviewer_policy_sha256 \
    promotion_environment_updated_at \
    promotion_environment_verified \
    promotion_reverification_verified \
    registry_referrers_reverified_after_approval \
    release_signer_event \
    release_signer_workflow_ref \
    release_signer_workflow_sha \
    release_security_external_assumption \
    release_source_ref \
    reproducibility_platforms \
    reproducibility_verified \
    source_ci_run \
    source_ci_run_attempt \
    source_ci_workflow_ref \
    source_ci_workflow_sha \
    trivy_db_max_age_seconds \
    trivy_image_source \
    trivy_host_docker_config_mounted \
    trivy_postapproval_artifact_digest \
    trivy_postapproval_artifact_name \
    trivy_postapproval_database_sha256 \
    trivy_postapproval_database_updated_at \
    trivy_postapproval_evidence_sha256 \
    trivy_postapproval_identity_sha256 \
    trivy_postapproval_scan_finished_at \
    trivy_postapproval_verified \
    trivy_preapproval_amd64_artifact_name \
    trivy_preapproval_amd64_evidence_sha256 \
    trivy_preapproval_amd64_identity_sha256 \
    trivy_preapproval_amd64_scan_finished_epoch \
    trivy_preapproval_arm64_artifact_name \
    trivy_preapproval_arm64_evidence_sha256 \
    trivy_preapproval_arm64_identity_sha256 \
    trivy_preapproval_arm64_scan_finished_epoch \
    trivy_scan_platforms \
    trivy_registry_auth \
    trivy_scanner_image \
    trivy_scanner_version \
    version_tag_atomic_compare_and_swap \
    version_tag_is_deployment_identity | LC_ALL=C sort)"
  validate_env_keys "$manifest_dir/images.env" \
    $'backend\nminio\nminio_mc\npostgres'
  validate_env_keys "$manifest_dir/release.env" \
    $'source_commit\nverification_run\nverification_run_attempt\nversion'
  validate_env_keys "$manifest_dir/verification.env" \
    "$expected_verification_keys"

  backend_ref="$(manifest_value backend "$manifest_dir/images.env")"
  postgres_ref="$(manifest_value postgres "$manifest_dir/images.env")"
  minio_ref="$(manifest_value minio "$manifest_dir/images.env")"
  minio_mc_ref="$(manifest_value minio_mc "$manifest_dir/images.env")"
  source_commit="$(manifest_value source_commit "$manifest_dir/release.env")"
  release_version="$(manifest_value version "$manifest_dir/release.env")"
  verification_run="$(manifest_value verification_run \
    "$manifest_dir/release.env")"
  verification_run_attempt="$(manifest_value verification_run_attempt \
    "$manifest_dir/release.env")"
  [[ "$release_version" == "$version" ]]
  [[ "$source_commit" == "$(jq -er '.head_sha' "$stage/run.json")" ]]
  [[ "$verification_run" == "$(jq -er '.html_url' "$stage/run.json")" ]]
  [[ "$verification_run_attempt" =~ ^[1-9][0-9]*$ ]]
  [[ "$verification_run_attempt" == \
    "$(jq -er '.run_attempt | tostring' "$stage/run.json")" ]]

  expected_ci_workflow="$repository/.github/workflows/backend.yml@refs/heads/main"
  expected_release_workflow="$repository/.github/workflows/backend-release.yml@refs/heads/main"
  expected_identity="https://github.com/$expected_release_workflow"
  [[ "$(manifest_value source_ci_workflow_ref \
      "$manifest_dir/verification.env")" == "$expected_ci_workflow" ]]
  [[ "$(manifest_value source_ci_workflow_sha \
      "$manifest_dir/verification.env")" == "$source_commit" ]]
  [[ "$(manifest_value release_source_ref \
      "$manifest_dir/verification.env")" == refs/heads/main ]]
  [[ "$(manifest_value release_signer_workflow_ref \
      "$manifest_dir/verification.env")" == "$expected_release_workflow" ]]
  [[ "$(manifest_value release_signer_workflow_sha \
      "$manifest_dir/verification.env")" == "$source_commit" ]]
  [[ "$(manifest_value release_signer_event \
      "$manifest_dir/verification.env")" == workflow_dispatch ]]
  [[ "$(manifest_value cosign_certificate_identity \
      "$manifest_dir/verification.env")" == "$expected_identity" ]]
  [[ "$(manifest_value cosign_certificate_oidc_issuer \
      "$manifest_dir/verification.env")" == \
    https://token.actions.githubusercontent.com ]]
  [[ "$(manifest_value deployment_identity \
      "$manifest_dir/verification.env")" == repository@sha256 ]]
  [[ "$(manifest_value promotion_environment \
      "$manifest_dir/verification.env")" == backend-production ]]
  [[ "$(manifest_value trivy_scan_platforms \
      "$manifest_dir/verification.env")" == linux/amd64,linux/arm64 ]]
  [[ "$(manifest_value buildkit_attestation_platforms \
      "$manifest_dir/verification.env")" == linux/amd64,linux/arm64 ]]
  [[ "$(manifest_value reproducibility_platforms \
      "$manifest_dir/verification.env")" == linux/amd64,linux/arm64 ]]
  [[ "$(manifest_value source_ci_run_attempt \
      "$manifest_dir/verification.env")" =~ ^[1-9][0-9]*$ ]]
  [[ "$(manifest_value amd64_gate_job \
      "$manifest_dir/verification.env")" == verify-published ]]
  [[ "$(manifest_value arm64_gate_job \
      "$manifest_dir/verification.env")" == verify-published-arm64 ]]
  [[ "$(manifest_value trivy_image_source \
      "$manifest_dir/verification.env")" == remote ]]
  [[ "$(manifest_value trivy_registry_auth \
      "$manifest_dir/verification.env")" == \
    per-repository-pull-only-bearer ]]
  [[ "$(manifest_value trivy_db_max_age_seconds \
      "$manifest_dir/verification.env")" == 86400 ]]
  [[ "$(manifest_value trivy_scanner_image \
      "$manifest_dir/verification.env")" == \
    aquasec/trivy@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f ]]
  [[ "$(manifest_value trivy_scanner_version \
      "$manifest_dir/verification.env")" == 0.72.0 ]]
  [[ "$(manifest_value buildkit_sbom_generator \
      "$manifest_dir/verification.env")" == \
    docker/buildkit-syft-scanner:stable-1@sha256:79e7b013cbec16bbb436f312819a49a4a57752b2270c1a9332ae1a10fcc82a68 ]]
  [[ "$(manifest_value buildx_repository \
      "$manifest_dir/verification.env")" == https://github.com/docker/buildx ]]
  [[ "$(manifest_value buildx_version \
      "$manifest_dir/verification.env")" == v0.35.0 ]]
  [[ "$(manifest_value buildx_host_platform \
      "$manifest_dir/verification.env")" == linux/amd64 ]]
  [[ "$(manifest_value buildkit_image \
      "$manifest_dir/verification.env")" == \
    moby/buildkit:v0.31.0@sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1 ]]
  [[ "$(manifest_value compose_file \
      "$manifest_dir/verification.env")" == docker-compose.backend.yml ]]
  [[ "$(manifest_value compose_contract_env \
      "$manifest_dir/verification.env")" == compose-contract.env ]]
  [[ "$(manifest_value compose_config \
      "$manifest_dir/verification.env")" == compose-config.json ]]
  [[ "$(manifest_value compose_verifier \
      "$manifest_dir/verification.env")" == verify-compose-release.sh ]]
  [[ "$(manifest_value compose_verification_command \
      "$manifest_dir/verification.env")" == \
    'bash verify-compose-release.sh verify . images.env' ]]
  [[ "$(manifest_value compose_generator_version \
      "$manifest_dir/verification.env")" == "$(docker compose version --short)" ]]
  bash "$manifest_dir/verify-compose-release.sh" verify \
    "$manifest_dir" "$manifest_dir/images.env" >/dev/null
  for compose_digest_spec in \
    compose_file_sha256:docker-compose.backend.yml \
    compose_contract_env_sha256:compose-contract.env \
    compose_config_sha256:compose-config.json \
    compose_verifier_sha256:verify-compose-release.sh
  do
    compose_digest_key=${compose_digest_spec%%:*}
    compose_digest_file=${compose_digest_spec#*:}
    [[ "$(manifest_value "$compose_digest_key" \
        "$manifest_dir/verification.env")" == \
      "sha256:$(sha256sum "$manifest_dir/$compose_digest_file" | awk '{print $1}')" ]]
  done
  [[ "$(manifest_value minio_security_labels_verified_platforms \
      "$manifest_dir/verification.env")" == linux/amd64,linux/arm64 ]]
  [[ "$(manifest_value cosign_signature_job \
      "$manifest_dir/verification.env")" == sign-approved ]]
  [[ "$(manifest_value cosign_signature_stage \
      "$manifest_dir/verification.env")" == after-platform-gates ]]
  [[ "$(manifest_value cosign_signed_references \
      "$manifest_dir/verification.env")" == backend,postgres,minio,minio_mc ]]
  [[ "$(manifest_value release_security_external_assumption \
      "$manifest_dir/verification.env")" == \
    repository-and-workflow-writers-trusted-and-restricted ]]
  [[ "$(manifest_value cve_acceptance_contract_schema \
      "$manifest_dir/verification.env")" == \
    emfont.cve-acceptance-contract/v1 ]]
  [[ "$(manifest_value cve_acceptance_document_schema \
      "$manifest_dir/verification.env")" == emfont.cve-acceptance/v1 ]]
  [[ "$(manifest_value cve_acceptance_source \
      "$manifest_dir/verification.env")" == \
    github-environment-variable:backend-production/EMFONT_CVE_ACCEPTANCE_B64 ]]
  [[ "$(manifest_value promotion_environment_deployment_policies \
      "$manifest_dir/verification.env")" == branch:main ]]
  for boolean_key in \
    amd64_gate_approved \
    arm64_gate_approved \
    buildx_independent_rebuild_tooling_verified \
    buildx_postapproval_tooling_verified \
    buildkit_provenance_verified \
    buildkit_sbom_verified \
    compose_contract_verified \
    compose_verification_required_for_deploy \
    compose_verification_required_for_rollback \
    cosign_negative_fixtures_reverified_after_approval \
    cosign_negative_fixtures_verified \
    cosign_signatures_reverified_after_approval \
    cosign_signatures_verified \
    github_attestations_reverified_after_approval \
    github_build_provenance_verified \
    promotion_environment_prevent_self_review \
    promotion_environment_verified \
    promotion_reverification_verified \
    registry_referrers_reverified_after_approval \
    reproducibility_verified \
    trivy_postapproval_verified
  do
    [[ "$(manifest_value "$boolean_key" \
      "$manifest_dir/verification.env")" == true ]]
  done
  [[ "$(manifest_value version_tag_is_deployment_identity \
      "$manifest_dir/verification.env")" == false ]]
  [[ "$(manifest_value version_tag_atomic_compare_and_swap \
      "$manifest_dir/verification.env")" == false ]]
  [[ "$(manifest_value external_writer_toctou_eliminated \
      "$manifest_dir/verification.env")" == false ]]
  [[ "$(manifest_value promotion_environment_admin_bypass \
      "$manifest_dir/verification.env")" == false ]]
  [[ "$(manifest_value trivy_host_docker_config_mounted \
      "$manifest_dir/verification.env")" == false ]]

  for digest_key in \
    buildx_binary_sha256 \
    compose_config_sha256 \
    compose_contract_env_sha256 \
    compose_file_sha256 \
    compose_verifier_sha256 \
    promotion_environment_evidence_sha256 \
    promotion_environment_reviewer_policy_sha256 \
    trivy_postapproval_artifact_digest \
    trivy_postapproval_database_sha256 \
    trivy_postapproval_evidence_sha256 \
    trivy_postapproval_identity_sha256 \
    trivy_preapproval_amd64_evidence_sha256 \
    trivy_preapproval_amd64_identity_sha256 \
    trivy_preapproval_arm64_evidence_sha256 \
    trivy_preapproval_arm64_identity_sha256
  do
    [[ "$(manifest_value "$digest_key" \
      "$manifest_dir/verification.env")" =~ ^sha256:[0-9a-f]{64}$ ]]
  done

  [[ "$(manifest_value trivy_preapproval_amd64_artifact_name \
      "$manifest_dir/verification.env")" == \
    "backend-release-security-$version-$verification_run_attempt" ]]
  [[ "$(manifest_value trivy_preapproval_arm64_artifact_name \
      "$manifest_dir/verification.env")" == \
    "backend-release-security-arm64-$version-$verification_run_attempt" ]]
  [[ "$(manifest_value trivy_postapproval_artifact_name \
      "$manifest_dir/verification.env")" == \
    "backend-release-postapproval-security-$version-$verification_run_attempt" ]]
  for epoch_key in \
    trivy_preapproval_amd64_scan_finished_epoch \
    trivy_preapproval_arm64_scan_finished_epoch
  do
    [[ "$(manifest_value "$epoch_key" \
      "$manifest_dir/verification.env")" =~ ^[1-9][0-9]*$ ]]
  done
  for timestamp_key in \
    trivy_postapproval_database_updated_at \
    trivy_postapproval_scan_finished_at \
    promotion_environment_updated_at
  do
    timestamp="$(manifest_value "$timestamp_key" \
      "$manifest_dir/verification.env")"
    [[ "$timestamp" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]
    date --utc --date="$timestamp" +%s >/dev/null
  done
  for count_key in \
    promotion_environment_id \
    promotion_environment_approval_count \
    promotion_environment_required_reviewer_count
  do
    [[ "$(manifest_value "$count_key" \
      "$manifest_dir/verification.env")" =~ ^[1-9][0-9]*$ ]]
  done
  required_reviewer_count="$(manifest_value \
    promotion_environment_required_reviewer_count \
    "$manifest_dir/verification.env")"
  ((required_reviewer_count <= 6))

  run_actor="$(jq -er '
    .actor.login | select(type == "string" and
      test("^[A-Za-z0-9-]{1,39}$"))
  ' "$stage/run.json")"
  acceptance_count="$(manifest_value cve_acceptance_count \
    "$manifest_dir/verification.env")"
  [[ "$acceptance_count" =~ ^[0-9]+$ ]]
  acceptance_sha256="$(manifest_value cve_acceptance_sha256 \
    "$manifest_dir/verification.env")"
  acceptance_base64="$(manifest_value cve_acceptance_evidence_base64 \
    "$manifest_dir/verification.env")"
  if ((acceptance_count == 0)); then
    [[ "$acceptance_sha256" == none ]]
    [[ "$acceptance_base64" == none ]]
  else
    [[ "$acceptance_sha256" =~ ^sha256:[0-9a-f]{64}$ ]]
    [[ "$acceptance_base64" =~ ^[A-Za-z0-9+/]*={0,2}$ ]]
    ((${#acceptance_base64} <= 65536))
    ((${#acceptance_base64} % 4 == 0))
  fi
  approval_comment="emfont-release-approval/v1 source_sha=$source_commit run_attempt=$verification_run_attempt cve_acceptance_sha256=$acceptance_sha256"
  reviewer_policy_sha256="$(manifest_value \
    promotion_environment_reviewer_policy_sha256 \
    "$manifest_dir/verification.env")"
  [[ "$reviewer_policy_sha256" =~ ^sha256:[0-9a-f]{64}$ ]]

  environment_base64="$(manifest_value \
    promotion_environment_evidence_base64 \
    "$manifest_dir/verification.env")"
  [[ "$environment_base64" =~ ^[A-Za-z0-9+/]*={0,2}$ ]]
  ((${#environment_base64} % 4 == 0))
  environment_evidence="$stage/backend-production-environment-evidence.json"
  printf '%s' "$environment_base64" | base64 --decode \
    >"$environment_evidence"
  [[ -s "$environment_evidence" ]]
  environment_canonical="$stage/backend-production-environment-evidence.canonical.json"
  jq --sort-keys --compact-output . "$environment_evidence" \
    >"$environment_canonical"
  cmp --silent "$environment_evidence" "$environment_canonical"
  [[ "sha256:$(sha256sum "$environment_evidence" | awk '{print $1}')" == \
    "$(manifest_value promotion_environment_evidence_sha256 \
      "$manifest_dir/verification.env")" ]]
  reviewer_policy_evidence="$stage/backend-production-reviewer-policy.json"
  jq --sort-keys '.environment.reviewer_policy' \
    "$environment_evidence" >"$reviewer_policy_evidence"
  [[ "sha256:$(sha256sum "$reviewer_policy_evidence" | awk '{print $1}')" == \
    "$reviewer_policy_sha256" ]]

  environment_id="$(manifest_value promotion_environment_id \
    "$manifest_dir/verification.env")"
  environment_updated_at="$(manifest_value promotion_environment_updated_at \
    "$manifest_dir/verification.env")"
  approval_count="$(manifest_value promotion_environment_approval_count \
    "$manifest_dir/verification.env")"
  jq --exit-status \
    --arg repository "$repository" \
    --arg run_id "$run_id" \
    --arg run_attempt "$verification_run_attempt" \
    --arg source_sha "$source_commit" \
    --arg actor "$run_actor" \
    --arg approval_comment "$approval_comment" \
    --arg acceptance_sha256 "$acceptance_sha256" \
    --arg reviewer_policy_sha256 "$reviewer_policy_sha256" \
    --arg updated_at "$environment_updated_at" \
    --argjson environment_id "$environment_id" \
    --argjson reviewer_count "$required_reviewer_count" \
    --argjson approval_count "$approval_count" '
      (keys == [
        "approval_contract",
        "environment",
        "repository",
        "schema",
        "source_ref",
        "source_sha",
        "workflow_run_attempt",
        "workflow_run_id"
      ]) and
      .schema == "emfont.github-environment-policy/v1" and
      .repository == $repository and
      .workflow_run_id == $run_id and
      .workflow_run_attempt == $run_attempt and
      .source_sha == $source_sha and
      .source_ref == "refs/heads/main" and
      .approval_contract == {
        schema: "emfont-release-approval/v1",
        comment: $approval_comment,
        source_sha: $source_sha,
        run_attempt: $run_attempt,
        cve_acceptance_sha256: $acceptance_sha256,
        reviewer_policy_sha256: $reviewer_policy_sha256
      } and
      (.environment | keys == [
        "approvals",
        "can_admins_bypass",
        "created_at",
        "deployment_branch_policy",
        "deployment_policies",
        "id",
        "name",
        "protection_rules",
        "reviewer_policy",
        "updated_at"
      ]) and
      .environment.id == $environment_id and
      .environment.name == "backend-production" and
      .environment.updated_at == $updated_at and
      .environment.can_admins_bypass == false and
      .environment.reviewer_policy.schema ==
        "emfont.reviewer-policy-evidence/v1" and
      .environment.reviewer_policy.approval_comment == $approval_comment and
      (.environment.reviewer_policy.required_reviewers | type == "array" and
        length == $reviewer_count) and
      (.environment.reviewer_policy.approvals | type == "array" and
        length == $approval_count) and
      (.environment.approvals | type == "array" and
        length == $approval_count and length >= 1 and all(
          .state == "approved" and
          (.user.id | type == "number" and . > 0 and floor == .) and
          (.user.login | type == "string" and
            test("^[A-Za-z0-9-]{1,39}$")) and
          ((.user.login | ascii_downcase) != ($actor | ascii_downcase)) and
          .comment == $approval_comment and
          (.environments | type == "array" and any(
            .id == $environment_id and .name == "backend-production"
          ))
        )) and
      ([.environment.protection_rules[] |
        select(.type == "required_reviewers")] as $rules |
        ($rules | length == 1) and
        $rules[0].prevent_self_review == true and
        ($rules[0].reviewers | type == "array" and
          length == $reviewer_count and length >= 1 and length <= 6)) and
      .environment.deployment_branch_policy.protected_branches == false and
      .environment.deployment_branch_policy.custom_branch_policies == true and
      [.environment.deployment_policies[] | {name, type}] == [
        {name: "main", type: "branch"}
      ]
    ' "$environment_evidence" >/dev/null

  acceptance_evidence=
  if ((acceptance_count > 0)); then
    acceptance_evidence="$stage/cve-acceptance.canonical.json"
    printf '%s' "$acceptance_base64" | base64 --decode \
      >"$acceptance_evidence"
    acceptance_size="$(stat --format '%s' "$acceptance_evidence")"
    ((acceptance_size > 0 && acceptance_size <= 49152))
    jq --sort-keys --compact-output . "$acceptance_evidence" \
      >"$stage/cve-acceptance.recanonicalized.json"
    cmp --silent "$acceptance_evidence" \
      "$stage/cve-acceptance.recanonicalized.json"
    [[ "sha256:$(sha256sum "$acceptance_evidence" | awk '{print $1}')" == \
      "$acceptance_sha256" ]]
    jq --exit-status \
      --arg source_sha "$source_commit" \
      --arg actor "$run_actor" \
      --arg approval_comment "$approval_comment" \
      --argjson count "$acceptance_count" \
      --slurpfile environment "$environment_evidence" '
        (keys == ["acceptances", "schema", "source_sha"]) and
        .schema == "emfont.cve-acceptance/v1" and
        .source_sha == $source_sha and
        (.acceptances | type == "array" and length == $count and
          (unique_by([
            .platform,
            .component,
            .vulnerability_id,
            .target,
            .class,
            .type,
            .package,
            .installed_version,
            .fixed_version,
            .status,
            .severity,
            .image,
            .source_sha,
            .report_sha256
          ]) | length) == length and all(
            keys == [
              "class",
              "compensating_controls",
              "component",
              "expires_at",
              "fixed_version",
              "image",
              "installed_version",
              "owner",
              "package",
              "platform",
              "rationale",
              "report_sha256",
              "security_reviewer",
              "severity",
              "source_sha",
              "status",
              "target",
              "ticket",
              "type",
              "vulnerability_id"
            ] and
            (.vulnerability_id | type == "string" and
              test("^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$")) and
            (.component == "backend" or .component == "postgres" or
              .component == "minio-server" or .component == "minio-mc") and
            (.platform == "linux/amd64" or .platform == "linux/arm64") and
            (.image | type == "string" and
              test("^ghcr\\.io/yorukot/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$")) and
            (.target | type == "string" and length > 0 and length <= 1024) and
            (.class | type == "string" and length <= 128) and
            (.type | type == "string" and length <= 128) and
            (.package | type == "string" and length > 0 and length <= 512) and
            (.installed_version | type == "string" and
              length > 0 and length <= 512) and
            .fixed_version == "" and
            (.status | type == "string" and length <= 128) and
            (.severity == "HIGH" or .severity == "CRITICAL") and
            (.expires_at | type == "string" and
              test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
            (.owner | type == "string" and
              test("^github:(user/[A-Za-z0-9-]{1,39}|team/[A-Za-z0-9_.-]{1,39}/[A-Za-z0-9_.-]{1,100})$")) and
            (.rationale | type == "string" and length >= 20 and length <= 1000 and
              (test("[[:cntrl:]]") | not)) and
            (.compensating_controls | type == "array" and
              length >= 1 and length <= 8 and all(
                type == "string" and length >= 10 and length <= 500 and
                (test("[[:cntrl:]]") | not) and test("[^[:space:]]")
              ) and (unique | length) == length) and
            (.security_reviewer | type == "string" and
              test("^github:user/[A-Za-z0-9-]{1,39}$")) and
            (.ticket | type == "string" and length >= 3 and length <= 256 and
              (test("[[:space:]]") | not) and
              (test("[[:cntrl:]]") | not) and test("[A-Za-z0-9]")) and
            .source_sha == $source_sha and
            (.report_sha256 | test("^sha256:[0-9a-f]{64}$")) and
            ((.security_reviewer |
              ltrimstr("github:user/") | ascii_downcase) as $reviewer |
              $reviewer != ($actor | ascii_downcase) and
              ($environment[0].environment.approvals | any(
                ((.user.login | ascii_downcase) == $reviewer) and
                .comment == $approval_comment
              )))
          ))
      ' "$acceptance_evidence" >/dev/null
    now_epoch="$(date --utc +%s)"
    while IFS= read -r expires_at; do
      expires_epoch="$(date --utc --date="$expires_at" +%s)"
      ((expires_epoch > now_epoch))
    done < <(jq --exit-status --raw-output \
      '.acceptances[].expires_at' "$acceptance_evidence")
  fi

  expected_component_ref() {
    case "$1" in
      backend) printf '%s' "$backend_ref" ;;
      postgres) printf '%s' "$postgres_ref" ;;
      minio-server) printf '%s' "$minio_ref" ;;
      minio-mc) printf '%s' "$minio_mc_ref" ;;
      *) return 1 ;;
    esac
  }

  validate_scan_identity() {
    local identity=$1
    local platform=$2
    local reports=$3
    local layout=$4
    local component image report_sha256 expected_ref normalized platform_id
    jq --exit-status --arg platform "$platform" '
      .schema == "emfont.trivy-scan-identity/v1" and
      .platform == $platform and
      [.images[].component] ==
        ["backend", "minio-mc", "minio-server", "postgres"] and
      (.images | all(
        (.image | test("^ghcr\\.io/yorukot/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$")) and
        (.report_sha256 | test("^sha256:[0-9a-f]{64}$"))
      ))
    ' "$identity" >/dev/null
    platform_id=${platform#linux/}
    while IFS=$'\t' read -r component image report_sha256; do
      expected_ref="$(expected_component_ref "$component")"
      [[ "$image" == "$expected_ref" ]]
      if [[ "$layout" == preapproval ]]; then
        normalized="$reports/$component-high-critical.normalized.json"
      else
        normalized="$reports/$component-$platform_id-high-critical.normalized.json"
      fi
      [[ -f "$normalized" ]]
      [[ "sha256:$(sha256sum "$normalized" | awk '{print $1}')" == \
        "$report_sha256" ]]
      jq --exit-status \
        --arg component "$component" \
        --arg platform "$platform" \
        --arg image "$image" '
          .schema == "emfont.trivy-high-critical/v1" and
          .component == $component and
          .platform == $platform and
          .image == $image and
          (.findings | type == "array" and
            all(.fixed_version == ""))
        ' "$normalized" >/dev/null
    done < <(jq --exit-status --raw-output \
      '.images[] | [.component, .image, .report_sha256] | @tsv' \
      "$identity")
  }

  validate_preapproval_trivy() {
    local directory=$1
    local platform=$2
    local identity_sha256=$3
    local evidence_sha256=$4
    local finished_epoch=$5
    local identity="$directory/scan-identity.json"
    local evidence="$directory/scan-evidence.json"
    local db_updated_at scan_started_at scan_finished_at
    local db_updated_epoch scan_started_epoch actual_finished_epoch
    [[ "sha256:$(sha256sum "$identity" | awk '{print $1}')" == \
      "$identity_sha256" ]]
    [[ "sha256:$(sha256sum "$evidence" | awk '{print $1}')" == \
      "$evidence_sha256" ]]
    validate_scan_identity "$identity" "$platform" "$directory" preapproval
    jq --exit-status \
      --arg platform "$platform" \
      --arg scanner_image "$(manifest_value trivy_scanner_image \
        "$manifest_dir/verification.env")" \
      --arg scanner_version "$(manifest_value trivy_scanner_version \
        "$manifest_dir/verification.env")" \
      --arg identity_sha256 "$identity_sha256" \
      --slurpfile identity "$identity" '
        .schema == "emfont.trivy-scan-evidence/v1" and
        .platform == $platform and
        .scanner_image == $scanner_image and
        .scanner_version == $scanner_version and
        (.database_sha256 | test("^sha256:[0-9a-f]{64}$")) and
        .identity_sha256 == $identity_sha256 and
        .identity == $identity[0]
      ' "$evidence" >/dev/null
    db_updated_at="$(jq -er '.database_updated_at' "$evidence")"
    scan_started_at="$(jq -er '.scan_started_at' "$evidence")"
    scan_finished_at="$(jq -er '.scan_finished_at' "$evidence")"
    db_updated_epoch="$(date --utc --date="$db_updated_at" +%s)"
    scan_started_epoch="$(date --utc --date="$scan_started_at" +%s)"
    actual_finished_epoch="$(date --utc --date="$scan_finished_at" +%s)"
    ((db_updated_epoch <= scan_started_epoch + 300))
    ((scan_started_epoch - db_updated_epoch <= 86400))
    ((actual_finished_epoch >= scan_started_epoch))
    ((actual_finished_epoch - scan_started_epoch <= 3600))
    [[ "$actual_finished_epoch" == "$finished_epoch" ]]
  }

  trivy_dir="$stage/trivy-evidence"
  pre_amd64_dir="$trivy_dir/preapproval-amd64"
  pre_arm64_dir="$trivy_dir/preapproval-arm64"
  post_dir="$trivy_dir/postapproval"
  install -d -m 0700 \
    "$pre_amd64_dir" "$pre_arm64_dir" "$post_dir"
  pre_amd64_name="$(manifest_value \
    trivy_preapproval_amd64_artifact_name \
    "$manifest_dir/verification.env")"
  pre_arm64_name="$(manifest_value \
    trivy_preapproval_arm64_artifact_name \
    "$manifest_dir/verification.env")"
  post_name="$(manifest_value trivy_postapproval_artifact_name \
    "$manifest_dir/verification.env")"
  gh run download "$run_id" --repo "$repository" \
    --name "$pre_amd64_name" --dir "$pre_amd64_dir"
  gh run download "$run_id" --repo "$repository" \
    --name "$pre_arm64_name" --dir "$pre_arm64_dir"
  gh run download "$run_id" --repo "$repository" \
    --name "$post_name" --dir "$post_dir"

  gh api --method GET \
    "repos/$repository/actions/runs/$run_id/artifacts?per_page=100" \
    >"$stage/run-artifacts.json"
  jq --exit-status \
    --arg name "$post_name" \
    --arg digest "$(manifest_value trivy_postapproval_artifact_digest \
      "$manifest_dir/verification.env")" '
      [.artifacts[] | select(.name == $name)] as $artifacts |
      ($artifacts | length == 1) and
      $artifacts[0].expired == false and
      $artifacts[0].digest == $digest
    ' "$stage/run-artifacts.json" >/dev/null

  validate_preapproval_trivy \
    "$pre_amd64_dir" linux/amd64 \
    "$(manifest_value trivy_preapproval_amd64_identity_sha256 \
      "$manifest_dir/verification.env")" \
    "$(manifest_value trivy_preapproval_amd64_evidence_sha256 \
      "$manifest_dir/verification.env")" \
    "$(manifest_value trivy_preapproval_amd64_scan_finished_epoch \
      "$manifest_dir/verification.env")"
  validate_preapproval_trivy \
    "$pre_arm64_dir" linux/arm64 \
    "$(manifest_value trivy_preapproval_arm64_identity_sha256 \
      "$manifest_dir/verification.env")" \
    "$(manifest_value trivy_preapproval_arm64_evidence_sha256 \
      "$manifest_dir/verification.env")" \
    "$(manifest_value trivy_preapproval_arm64_scan_finished_epoch \
      "$manifest_dir/verification.env")"

  post_identity="$post_dir/scan-identities.json"
  post_evidence="$post_dir/scan-evidence.json"
  post_amd64_identity="$post_dir/scan-identity-amd64.json"
  post_arm64_identity="$post_dir/scan-identity-arm64.json"
  [[ "sha256:$(sha256sum "$post_identity" | awk '{print $1}')" == \
    "$(manifest_value trivy_postapproval_identity_sha256 \
      "$manifest_dir/verification.env")" ]]
  [[ "sha256:$(sha256sum "$post_evidence" | awk '{print $1}')" == \
    "$(manifest_value trivy_postapproval_evidence_sha256 \
      "$manifest_dir/verification.env")" ]]
  validate_scan_identity \
    "$post_amd64_identity" linux/amd64 "$post_dir" postapproval
  validate_scan_identity \
    "$post_arm64_identity" linux/arm64 "$post_dir" postapproval
  jq --exit-status \
    --slurpfile amd64 "$post_amd64_identity" \
    --slurpfile arm64 "$post_arm64_identity" '
      .schema == "emfont.trivy-combined-scan-identity/v1" and
      .identities == [$amd64[0], $arm64[0]]
    ' "$post_identity" >/dev/null

  jq --exit-status \
    --arg scanner_image "$(manifest_value trivy_scanner_image \
      "$manifest_dir/verification.env")" \
    --arg scanner_version "$(manifest_value trivy_scanner_version \
      "$manifest_dir/verification.env")" \
    --arg database_sha256 "$(manifest_value \
      trivy_postapproval_database_sha256 \
      "$manifest_dir/verification.env")" \
    --arg database_updated_at "$(manifest_value \
      trivy_postapproval_database_updated_at \
      "$manifest_dir/verification.env")" \
    --arg scan_finished_at "$(manifest_value \
      trivy_postapproval_scan_finished_at \
      "$manifest_dir/verification.env")" \
    --arg source_sha "$source_commit" \
    --arg identity_sha256 "$(manifest_value \
      trivy_postapproval_identity_sha256 \
      "$manifest_dir/verification.env")" \
    --arg acceptance_sha256 "$acceptance_sha256" \
    --arg pre_amd64_identity "$(manifest_value \
      trivy_preapproval_amd64_identity_sha256 \
      "$manifest_dir/verification.env")" \
    --arg pre_amd64_evidence "$(manifest_value \
      trivy_preapproval_amd64_evidence_sha256 \
      "$manifest_dir/verification.env")" \
    --arg pre_arm64_identity "$(manifest_value \
      trivy_preapproval_arm64_identity_sha256 \
      "$manifest_dir/verification.env")" \
    --arg pre_arm64_evidence "$(manifest_value \
      trivy_preapproval_arm64_evidence_sha256 \
      "$manifest_dir/verification.env")" \
    --argjson acceptance_count "$acceptance_count" \
    --slurpfile identities "$post_identity" '
      .schema == "emfont.trivy-postapproval-evidence/v1" and
      .scanner_image == $scanner_image and
      .scanner_version == $scanner_version and
      .database_sha256 == $database_sha256 and
      .database_updated_at == $database_updated_at and
      .scan_finished_at == $scan_finished_at and
      .source_sha == $source_sha and
      .identity_sha256 == $identity_sha256 and
      .identities == $identities[0] and
      .acceptance_sha256 == $acceptance_sha256 and
      .acceptance_count == $acceptance_count and
      .preapproval.amd64 == {
        identity_sha256: $pre_amd64_identity,
        evidence_sha256: $pre_amd64_evidence
      } and
      .preapproval.arm64 == {
        identity_sha256: $pre_arm64_identity,
        evidence_sha256: $pre_arm64_evidence
      }
    ' "$post_evidence" >/dev/null
  post_started_epoch="$(date --utc \
    --date="$(jq -er '.scan_started_at' "$post_evidence")" +%s)"
  post_finished_epoch="$(date --utc \
    --date="$(jq -er '.scan_finished_at' "$post_evidence")" +%s)"
  post_db_epoch="$(date --utc \
    --date="$(jq -er '.database_updated_at' "$post_evidence")" +%s)"
  ((post_db_epoch <= post_started_epoch + 300))
  ((post_started_epoch - post_db_epoch <= 86400))
  ((post_finished_epoch >= post_started_epoch))
  ((post_finished_epoch - post_started_epoch <= 3600))

  required_identities="$post_dir/required-cve-acceptance-identities.json"
  jq --exit-status \
    --arg source_sha "$source_commit" \
    --argjson count "$acceptance_count" '
      type == "array" and length == $count and
      (unique | length) == length and
      all(.source_sha == $source_sha and .fixed_version == "")
    ' "$required_identities" >/dev/null
  actual_identities="$stage/actual-cve-acceptance-identities.json"
  if ((acceptance_count == 0)); then
    printf '[]\n' >"$actual_identities"
  else
    jq --sort-keys '
      [.acceptances[] | del(
        .compensating_controls,
        .expires_at,
        .owner,
        .rationale,
        .security_reviewer,
        .ticket
      )] | sort_by([
        .platform,
        .component,
        .vulnerability_id,
        .target,
        .class,
        .type,
        .package,
        .installed_version,
        .fixed_version,
        .status,
        .severity,
        .image,
        .source_sha,
        .report_sha256
      ])
    ' "$acceptance_evidence" >"$actual_identities"
  fi
  cmp --silent "$required_identities" "$actual_identities"

  source_ci_run="$(manifest_value source_ci_run \
    "$manifest_dir/verification.env")"
  [[ "$source_ci_run" =~ ^https://github\.com/yorukot/emfont/actions/runs/([1-9][0-9]*)$ ]]
  source_ci_run_id=${BASH_REMATCH[1]}
  gh api --method GET \
    "repos/$repository/actions/runs/$source_ci_run_id" \
    >"$stage/source-ci-run.json"
  jq -e \
    --arg source_commit "$source_commit" \
    --arg source_ci_run "$source_ci_run" \
    --arg source_attempt "$(manifest_value source_ci_run_attempt \
      "$manifest_dir/verification.env")" '
      .name == "Backend CI" and
      .conclusion == "success" and
      .head_branch == "main" and
      .head_sha == $source_commit and
      .html_url == $source_ci_run and
      (.run_attempt | tostring) == $source_attempt
    ' "$stage/source-ci-run.json" >/dev/null

  access_mode=${EMFONT_GHCR_ACCESS_MODE:?Set public or authenticated}
  case "$access_mode" in
    public)
      anonymous_config="$stage/anonymous-docker-config"
      install -d -m 0700 "$anonymous_config"
      for ref in "$backend_ref" "$postgres_ref" "$minio_ref" "$minio_mc_ref"; do
        docker --config "$anonymous_config" buildx imagetools inspect \
          "$ref" >/dev/null
      done
      ;;
    authenticated)
      for ref in "$backend_ref" "$postgres_ref" "$minio_ref" "$minio_mc_ref"; do
        docker buildx imagetools inspect "$ref" >/dev/null
      done
      ;;
    *)
      printf 'EMFONT_GHCR_ACCESS_MODE must be public or authenticated\n' >&2
      exit 2
      ;;
  esac

  split_ref() {
    local ref=$1
    local expected_repository=$2
    local variable_prefix=$3
    local image_repository digest
    [[ "$ref" =~ ^([^@]+)@sha256:([0-9a-f]{64})$ ]]
    image_repository=${BASH_REMATCH[1]}
    digest=${BASH_REMATCH[2]}
    [[ "$image_repository" == "$expected_repository" ]]
    printf '%s_IMAGE_REPOSITORY=%s\n' "$variable_prefix" "$image_repository"
    printf '%s_IMAGE_SHA256=%s\n' "$variable_prefix" "$digest"
  }

  verification_dir="$stage/deployment-verification"
  install -d -m 0700 "$verification_dir"
  cp "$environment_evidence" \
    "$verification_dir/backend-production-environment-evidence.json"
  if ((acceptance_count > 0)); then
    cp "$acceptance_evidence" \
      "$verification_dir/cve-acceptance.canonical.json"
  fi
  cp \
    "$pre_amd64_dir/scan-identity.json" \
    "$verification_dir/trivy-preapproval-amd64-identity.json"
  cp \
    "$pre_amd64_dir/scan-evidence.json" \
    "$verification_dir/trivy-preapproval-amd64-evidence.json"
  cp \
    "$pre_arm64_dir/scan-identity.json" \
    "$verification_dir/trivy-preapproval-arm64-identity.json"
  cp \
    "$pre_arm64_dir/scan-evidence.json" \
    "$verification_dir/trivy-preapproval-arm64-evidence.json"
  cp "$post_identity" \
    "$verification_dir/trivy-postapproval-identities.json"
  cp "$post_evidence" \
    "$verification_dir/trivy-postapproval-evidence.json"
  cp "$required_identities" \
    "$verification_dir/required-cve-acceptance-identities.json"
  for image_spec in \
    "$backend_ref|backend" \
    "$postgres_ref|postgres" \
    "$minio_ref|minio" \
    "$minio_mc_ref|minio-mc"
  do
    IFS='|' read -r ref slug <<<"$image_spec"
    cosign verify \
      --certificate-identity "$expected_identity" \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      --certificate-github-workflow-name 'Backend Release' \
      --certificate-github-workflow-repository "$repository" \
      --certificate-github-workflow-ref refs/heads/main \
      --certificate-github-workflow-sha "$source_commit" \
      --certificate-github-workflow-trigger workflow_dispatch \
      "$ref" >"$verification_dir/$slug-cosign.txt" 2>&1
    gh attestation verify "oci://$ref" --repo "$repository" \
      --signer-workflow "$repository/.github/workflows/backend-release.yml" \
      --source-ref refs/heads/main \
      --source-digest "$source_commit" \
      --predicate-type https://slsa.dev/provenance/v1 \
      --deny-self-hosted-runners \
      >"$verification_dir/$slug-github-attestation.txt" 2>&1
    docker buildx imagetools inspect --raw "$ref" \
      >"$verification_dir/$slug-oci-index.json"
    jq -e '
      (.manifests | any(
        .platform.os == "linux" and .platform.architecture == "amd64")) and
      (.manifests | any(
        .platform.os == "linux" and .platform.architecture == "arm64"))
    ' "$verification_dir/$slug-oci-index.json" >/dev/null
  done
  cp "$stage/run.json" "$verification_dir/workflow-run.json"
  cp "$stage/source-ci-run.json" \
    "$verification_dir/source-ci-workflow-run.json"
  (cd "$verification_dir" && sha256sum -- * >SHA256SUMS)

  release_env="$stage/compose-release.env"
  {
    split_ref "$backend_ref" \
      ghcr.io/yorukot/emfont-backend EMFONT_BACKEND
    split_ref "$postgres_ref" \
      ghcr.io/yorukot/emfont-postgres EMFONT_POSTGRES
    split_ref "$minio_ref" \
      ghcr.io/yorukot/emfont-minio EMFONT_MINIO
    split_ref "$minio_mc_ref" \
      ghcr.io/yorukot/emfont-minio-mc EMFONT_MINIO_MC
    printf 'EMFONT_VERSION=%s\n' "$release_version"
    printf 'EMFONT_RELEASE_MANIFEST_RUN_ID=%s\n' "$run_id"
    printf 'EMFONT_RELEASE_MANIFEST_RUN_ATTEMPT=%s\n' \
      "$verification_run_attempt"
    printf 'EMFONT_RELEASE_SOURCE_COMMIT=%s\n' "$source_commit"
    printf 'EMFONT_RELEASE_MANIFEST_SHA256=%s\n' \
      "$(sha256sum "$manifest_dir/SHA256SUMS" | awk '{print $1}')"
  } >"$release_env"

  release_dir="/etc/emfont/releases/$version-run-$run_id-attempt-$verification_run_attempt"
  [[ ! -e "$release_dir" ]]
  sudo install -d -m 0700 -o root -g root \
    "$release_dir" \
    "$release_dir/release-manifest" \
    "$release_dir/deployment-verification"
  sudo install -m 0400 -o root -g root "$manifest_dir"/* \
    "$release_dir/release-manifest/"
  sudo install -m 0400 -o root -g root "$release_env" \
    "$release_dir/compose-release.env"
  sudo install -m 0400 -o root -g root "$verification_dir"/* \
    "$release_dir/deployment-verification/"
  printf 'Verified release env: %s\n' "$release_dir/compose-release.env"
)
```

The generated release file has this shape; these values are illustrative and
must never be entered manually:

```dotenv
EMFONT_BACKEND_IMAGE_REPOSITORY=ghcr.io/yorukot/emfont-backend
EMFONT_BACKEND_IMAGE_SHA256=<64-hex-manifest-digest>
EMFONT_POSTGRES_IMAGE_REPOSITORY=ghcr.io/yorukot/emfont-postgres
EMFONT_POSTGRES_IMAGE_SHA256=<64-hex-manifest-digest>
EMFONT_MINIO_IMAGE_REPOSITORY=ghcr.io/yorukot/emfont-minio
EMFONT_MINIO_IMAGE_SHA256=<64-hex-manifest-digest>
EMFONT_MINIO_MC_IMAGE_REPOSITORY=ghcr.io/yorukot/emfont-minio-mc
EMFONT_MINIO_MC_IMAGE_SHA256=<64-hex-manifest-digest>
EMFONT_VERSION=v1.2.3
EMFONT_RELEASE_MANIFEST_RUN_ID=123456789
EMFONT_RELEASE_MANIFEST_RUN_ATTEMPT=1
EMFONT_RELEASE_SOURCE_COMMIT=<40-hex-commit>
EMFONT_RELEASE_MANIFEST_SHA256=<64-hex-checksum>
```

Keep non-release topology in `/etc/emfont/backend.env`. The stable
`/etc/emfont/current-release.env` is an atomic symlink to one generated
`compose-release.env` inside an immutable run-and-attempt-specific release
directory; switch it only at the promotion step. Every Compose,
systemd, and cron invocation must load the static file first and the generated
release file second. Old full-reference variables such as
`EMFONT_BACKEND_IMAGE` are prohibited even though current Compose ignores them.
The workflow artifact has finite GitHub retention; copy the original eight-file
manifest and its run metadata to the immutable release evidence system before
deployment. A generated env file without that retained source manifest is not
an acceptable future rollback or restore input. Never source any of the three
manifest env files; parse each expected key exactly as above.
On a greenfield host, set `EMFONT_RELEASE_ENV_FILE` to the generated initial
release file while running Validate configuration; First deployment creates
the stable symlink only after that validation passes.

## Secrets and deployment configuration

Create secret files outside the repository. Values must be non-empty,
single-line files. A trailing newline is accepted, but `printf` avoids one.

```bash
sudo install -d -m 0700 /etc/emfont/secrets
sudo sh -c 'umask 077
  printf %s emfont-root > /etc/emfont/secrets/minio-root-user
  printf %s "$(openssl rand -hex 32)" > /etc/emfont/secrets/postgres-admin-password
  printf %s "$(openssl rand -hex 32)" > /etc/emfont/secrets/postgres-app-password
  printf %s "$(openssl rand -hex 32)" > /etc/emfont/secrets/minio-root-password
  printf %s emfont-controller-$(openssl rand -hex 8) > /etc/emfont/secrets/minio-app-access-key
  printf %s "$(openssl rand -hex 32)" > /etc/emfont/secrets/minio-app-secret-key
  printf %s emfont-cleanup-$(openssl rand -hex 8) > /etc/emfont/secrets/minio-cleanup-access-key
  printf %s "$(openssl rand -hex 32)" > /etc/emfont/secrets/minio-cleanup-secret-key
  printf %s "$(openssl rand -hex 32)" > /etc/emfont/secrets/metrics-bearer-token
  chmod 0600 /etc/emfont/secrets/*'

sudo find /etc/emfont/secrets -mindepth 1 -maxdepth 1 -type f \
  -exec stat -c '%u:%g:%a %n' {} +
```

Every line from the final command must start with `0:0:600`. Host secret
sources owned by the deployment user, group-readable files, symlinks, and
modes other than `0600` are hard deployment failures.

Local Docker Compose ignores long-form secret `uid`, `gid`, and `mode` for
file-backed secrets, so the stack does not rely on those fields. Each backend
service mounts its assigned root-owned source under `/run/host-secrets`, copies
it into a private `/run/secrets` tmpfs as root:root `0400`, verifies the mode,
then invokes `load-secrets.sh` and drops to UID/GID 10001. Both the source and
staged file must be unreadable by UID 10001, including the native font worker.
Failure to stage or verify any file blocks process startup.

On the deployment host, keep the release-manifest `docker-compose.backend.yml`,
the static topology env, the generated Compose release env, and all eight
verified release-manifest files owned by root and not writable by UID 10001 or
accounts outside the deployment trust boundary. Runtime scripts are
baked into the exact signed image digests and are never mounted from the host.
After every secret rotation, recreate the affected service so it receives a
fresh private tmpfs copy.

Use `/etc/emfont/backend.env` only for non-release, non-secret deployment
topology. Restrict its permissions because it identifies secret paths and
network layout. Image repositories, image SHA-256 values, release-manifest
metadata, and `EMFONT_VERSION` belong only in the generated second env file.

```dotenv
COMPOSE_PROJECT_NAME=emfont
EMFONT_BACKEND_PULL_POLICY=always
EMFONT_INFRA_PULL_POLICY=always

EMFONT_POSTGRES_ADMIN_USER=emfont_admin
EMFONT_POSTGRES_APP_USER=emfont_app
EMFONT_POSTGRES_DB=emfont
EMFONT_POSTGRES_ADMIN_PASSWORD_FILE=/etc/emfont/secrets/postgres-admin-password
EMFONT_POSTGRES_APP_PASSWORD_FILE=/etc/emfont/secrets/postgres-app-password

EMFONT_MINIO_ROOT_USER_FILE=/etc/emfont/secrets/minio-root-user
EMFONT_MINIO_ROOT_PASSWORD_FILE=/etc/emfont/secrets/minio-root-password
EMFONT_MINIO_APP_ACCESS_KEY_FILE=/etc/emfont/secrets/minio-app-access-key
EMFONT_MINIO_APP_SECRET_KEY_FILE=/etc/emfont/secrets/minio-app-secret-key
EMFONT_MINIO_CLEANUP_ACCESS_KEY_FILE=/etc/emfont/secrets/minio-cleanup-access-key
EMFONT_MINIO_CLEANUP_SECRET_KEY_FILE=/etc/emfont/secrets/minio-cleanup-secret-key
EMFONT_MINIO_BUCKET=emfont
EMFONT_MINIO_POLICY_NAME=emfont-controller
EMFONT_MINIO_NONCURRENT_EXPIRE_DAYS=7
EMFONT_MINIO_PUBLIC_BASE_URL=https://objects.example.com/emfont

EMFONT_METRICS_BEARER_TOKEN_FILE=/etc/emfont/secrets/metrics-bearer-token
EMFONT_BACKEND_BASE_URL=https://api.example.com
EMFONT_CORS_ALLOWED_ORIGINS=https://www.example.com
EMFONT_HTTP_PORT=8080
EMFONT_TRUSTED_PROXY_CIDRS=172.18.0.1/32
EMFONT_RATE_LIMIT_ENABLED=true
EMFONT_RATE_LIMIT_REQUESTS_PER_SECOND=20
EMFONT_RATE_LIMIT_BURST=40
EMFONT_GLOBAL_RATE_LIMIT_REQUESTS_PER_SECOND=200
EMFONT_GLOBAL_RATE_LIMIT_BURST=400
EMFONT_RATE_LIMIT_MAX_CLIENTS=10000
EMFONT_RATE_LIMIT_IDLE_TIMEOUT=10m

EMFONT_FONT_BUILD_CONCURRENCY=2
EMFONT_FONT_MAX_PENDING_BUILDS=16
EMFONT_FONT_MAX_ARTIFACTS=100000
EMFONT_FONT_MAX_ARTIFACT_BYTES=53687091200
EMFONT_FONT_MAX_ACCOUNTED_BYTES=53687091200
EMFONT_FONT_MAX_TERMINAL_FAILURES=10000
EMFONT_FONT_MAX_SOURCE_BYTES=134217728
EMFONT_FONT_WORKER_PATH=emfont-fontworker
EMFONT_FONT_WORKER_MAX_OUTPUT_BYTES=134217728
EMFONT_FONT_WORKER_ADDRESS_SPACE_BYTES=2147483648
EMFONT_FONT_WORKER_CPU_SECONDS=60
EMFONT_FONT_WORKER_FILE_SIZE_BYTES=134217728
EMFONT_FONT_WORKER_OPEN_FILES=32
EMFONT_FONT_WORKER_STDERR_BYTES=16384
EMFONT_CONTROLLER_MEMORY_LIMIT=5g
EMFONT_CONTROLLER_MEMORY_RESERVATION=512m

EMFONT_CLEANUP_ARTIFACT_RETENTION=720h
EMFONT_CLEANUP_RETIREMENT_GRACE=2h
EMFONT_CLEANUP_ORPHAN_GRACE=6h
EMFONT_CLEANUP_TIMEOUT=30m
```

PostgreSQL role and database names are interpolated into URLs. Restrict them to
lowercase letters, digits, and underscores; do not put a password in a URL.
The secret loader supplies passwords through `PGPASSWORD` at runtime.

`EMFONT_RATE_LIMIT_REQUESTS_PER_SECOND` and `EMFONT_RATE_LIMIT_BURST` are the
per-client bucket. The two `EMFONT_GLOBAL_RATE_LIMIT_*` values are a second,
process-wide bucket checked first; they cap aggregate generation traffic even
when callers rotate identities. Both layers are process-local, so aggregate
capacity scales with replica count and still requires a shared edge limiter.
`EMFONT_RATE_LIMIT_MAX_CLIENTS` bounds memory used by tracked client buckets,
and the idle timeout controls their opportunistic eviction.

Font work executes in `EMFONT_FONT_WORKER_PATH`, a child binary from the same
immutable backend image. Concurrency and pending-build settings bound process
count and admission; the worker settings bound source/output protocol sizes,
address space, CPU seconds, file size, open files, and captured stderr. Keep
the address-space limit at least source bytes plus output bytes plus 256 MiB,
and never below 2 GiB. Treat a worker path outside the reviewed image as a new
executable supply-chain input requiring the full release gates.

The worker-reported builder identity is also the native cache identity. It
contains the exact target OS/architecture, Go toolchain, and installed Debian
HarfBuzz/WOFF2 package revisions, followed by a SHA-256 digest over those
values, `Dockerfile`, `go.mod`, `go.sum`, and all production worker sources.
The final-image verification gate validates this structure for each platform.
Do not override or shorten the linked `workerBuildRevision`: doing so permits
incompatible native outputs to share cache keys.

Size the controller cgroup as:

```text
controller memory >= controller peak RSS
                   + build concurrency * worker peak RSS
                   + safety margin
```

The 5 GiB default uses the conservative ceiling: approximately 512 MiB for the
controller, two workers at a 2 GiB `RLIMIT_AS` each, and 512 MiB margin.
`RLIMIT_AS` is a virtual-address ceiling rather than expected RSS, but malformed
native inputs must not make the default cgroup smaller than its own worst-case
worker limits. Never raise build concurrency without raising the cgroup limit;
lowering it requires measured peak RSS and an explicit deployment review.

`EMFONT_FONT_MAX_ARTIFACTS` and `EMFONT_FONT_MAX_ACCOUNTED_BYTES` are the
enforced generated-artifact quota inputs. The defaults cap the catalog at
100,000 artifacts and 50 GiB. `EMFONT_FONT_MAX_ARTIFACT_BYTES` is the delivery
compatibility input; Compose feeds it into `MAX_ACCOUNTED_BYTES` when the
canonical variable is unset. Keep both equal in managed environment templates.
Migration 9 maintains a singleton `font_artifact_quota` ledger transactionally:
`artifact_count` counts artifact rows and `accounted_bytes` sums generated
`quota_bytes`. Pending, running, and missing artifacts charge their persisted
reservation; ready and retryable-failed artifacts charge actual `size_bytes`.
A failed retryable build releases its reservation and must pass atomic
admission again before retrying. The trigger lock order also covers family
cascade deletes.
`TRUNCATE font_artifacts` is deliberately rejected because row triggers could
not account it; use reviewed row `DELETE`s for derived-cache invalidation.
Never edit either counter manually. Alert before either quota reaches 80%, and
lower admission or increase reviewed capacity rather than disabling the quota
during an incident.

`EMFONT_FONT_MAX_TERMINAL_FAILURES` is the separate global bound for cached
unsupported-codepoint results; it must be greater than zero and defaults to
10,000. Migration 10 creates `font_terminal_failures` outside
`font_artifacts`. A fenced terminal transition evicts the oldest negative-cache
rows as needed, inserts the immutable request identity and failure, and deletes
the active artifact and its cascading job in one quota-serialized transaction.
Terminal failures therefore never consume `artifact_count` or
`accounted_bytes`, even when this cache is full. Creation checks both tables
under the same lock, so a terminal failure committed between lookup and create
cannot trigger a duplicate build. Migration 10 deliberately discards legacy
version-6 terminal rows from `font_artifacts` rather than copying an unbounded
set; expect a cold negative cache after deployment and an immediate release of
those artifact slots.

### Object download URLs

Production has one supported model: set `EMFONT_MINIO_PUBLIC_BASE_URL` to the
external HTTPS bucket base URL served by a read-only object gateway. It must be
non-empty and contain no credentials, query, or fragment; the controller will
not start in `production` when it is empty or non-HTTPS. The controller returns
unsigned URLs below that base. The MinIO application policy does not make
objects anonymous, so the gateway owns public read authorization and must allow
only `GET` and `HEAD` for `_generated/*` and `original-fonts/*`.

The gateway is a separate deployment, not a service in
`docker-compose.backend.yml`. Attach it to both its public/TLS edge network and
the Compose-created `object-store` network. The latter is intentionally
`internal: true` and `attachable: true`; configure the gateway origin as
`http://minio:9000`, never a host port. Do not attach MinIO itself to the edge
network. Record both network IDs and verify the running topology before each
promotion:

```bash
gateway_container=${EMFONT_GATEWAY_CONTAINER:?Set the deployed gateway container}
gateway_public_network=${EMFONT_GATEWAY_PUBLIC_NETWORK:?Set gateway public network}
object_store_network="$(compose config --format json | \
  jq -er '.networks["object-store"].name')"
[[ "$object_store_network" != "$gateway_public_network" ]]

docker network inspect "$object_store_network" | jq -e '
  length == 1 and .[0].Internal == true and .[0].Attachable == true
' >/dev/null
docker network inspect "$gateway_public_network" | jq -e '
  length == 1 and .[0].Internal == false
' >/dev/null
docker container inspect "$gateway_container" | jq -e \
  --arg object_store "$object_store_network" \
  --arg public_edge "$gateway_public_network" '
    length == 1 and
    (.[0].NetworkSettings.Networks | has($object_store)) and
    (.[0].NetworkSettings.Networks | has($public_edge))
  ' >/dev/null

minio_id="$(compose ps --quiet minio)"
docker container inspect "$minio_id" | jq -e \
  --arg object_store "$object_store_network" '
    length == 1 and
    ((.[0].HostConfig.PortBindings // {}) | length == 0) and
    (.[0].NetworkSettings.Networks | keys == [$object_store])
  ' >/dev/null
```

The upstream release metadata remains in the affected
CVE-2026-40344/CVE-2026-41145 range. The repository `emfont.3` build patches
all three vulnerable unsigned-streaming call sites, but defense in depth still
forbids presigned direct-origin delivery for the bundled server. CVE-2026-34204
and CVE-2026-39414 are not patched: this deployment excludes replication and
S3 Select from both the
controller workflow and application policy. Production Compose publishes no
MinIO port. The gateway must reject `POST`, `PUT`,
`PATCH`, `DELETE`, WebDAV methods, bucket operations, multipart operations,
replication APIs, S3 Select requests, and unknown methods before proxying. A WAF
alert or application policy is not equivalent to an enforcing method allowlist.

The gateway's local deny handler must produce one response that cannot be
confused with MinIO or a generic WAF response:

```text
status: 405
Allow: GET, HEAD
X-Emfont-Gateway-Policy: object-read-only-v1
body (including final LF): emfont object gateway: method not allowed\n
```

Set the marker header and exact body only in the gateway-owned method-denial
handler, strip any upstream header with the same name, and do not proxy before
the method decision. The rendered configuration must have a positive
`GET`/`HEAD` allowlist plus a default deny for every other method token; a list
of selected denied methods is not equivalent. Hash and retain that rendered
configuration.

Probe the deployed public gateway from outside its trust network. The list
below covers the registered HTTP/WebDAV methods relevant to this deployment,
common extension methods, the advisory's unsigned-streaming request, and an
unknown token that exercises the default branch. Refresh the registered list
against the IANA HTTP Method Registry for each release. `GET` and `HEAD` are
tested separately against a known object.

```bash
(
  set -Eeuo pipefail

  gateway_base=${GATEWAY_BASE_URL:-https://fonts.example.com/emfont}
  origin_bucket_path=${GATEWAY_ORIGIN_BUCKET_PATH:-/emfont}
  known_object_url=${GATEWAY_KNOWN_OBJECT_URL:?Set an immutable object URL}
  known_object_sha256=${GATEWAY_KNOWN_OBJECT_SHA256:?Set expected SHA-256}
  [[ "$known_object_sha256" =~ ^[0-9a-f]{64}$ ]]

  probe_run="$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 8)"
  evidence_dir=${GATEWAY_EVIDENCE_DIR:?Set a new evidence directory}
  [[ ! -e "$evidence_dir" ]]
  install -d -m 0700 "$evidence_dir"
  date -u +%Y-%m-%dT%H:%M:%SZ >"$evidence_dir/probe-started-at.txt"
  printf '%s\n' 'emfont object gateway: method not allowed' \
    >"$evidence_dir/expected-denial-body.txt"

  forbidden_methods=(
    ACL BASELINE-CONTROL BIND CHECKIN CHECKOUT CONNECT COPY DELETE LABEL
    LINK LOCK MERGE MKACTIVITY MKCALENDAR MKCOL MKREDIRECTREF MKWORKSPACE
    MOVE OPTIONS ORDERPATCH PATCH POST PRI PROPFIND PROPPATCH PURGE PUT QUERY
    REBIND REPORT SEARCH TRACE UNBIND UNCHECKOUT UNLINK UNLOCK UPDATE
    UPDATEREDIRECTREF VERSION-CONTROL M-SEARCH NOTIFY SUBSCRIBE UNSUBSCRIBE
    EMFONT-FORBIDDEN-PROBE
  )

  probe_denial() {
    local method=$1
    local label=$2
    shift 2
    local path="$origin_bucket_path/__gateway_method_probe__/$probe_run/$label"
    local url="${gateway_base%/}/__gateway_method_probe__/$probe_run/$label"
    local headers="$evidence_dir/$label.headers"
    local body="$evidence_dir/$label.body"
    local normalized="$evidence_dir/$label.headers.normalized"
    local status

    printf '%s\n' "$path" >>"$evidence_dir/forbidden-origin-paths.txt"
    curl --silent --show-error --http1.1 \
      --request "$method" --header 'Expect:' \
      --dump-header "$headers" --output "$body" \
      "$@" "$url"
    status="$(awk '/^HTTP\// { code=$2 } END { print code }' "$headers")"
    [[ "$status" == 405 ]]
    tr -d '\r' <"$headers" >"$normalized"
    grep -Fxi -- 'Allow: GET, HEAD' "$normalized" >/dev/null
    grep -Fxi -- \
      'X-Emfont-Gateway-Policy: object-read-only-v1' \
      "$normalized" >/dev/null
    cmp --silent "$evidence_dir/expected-denial-body.txt" "$body"
  }

  for method in "${forbidden_methods[@]}"; do
    probe_denial "$method" "$method"
  done

  probe_denial PUT PUT-STREAMING-UNSIGNED \
    --header 'X-Amz-Content-Sha256: STREAMING-UNSIGNED-PAYLOAD-TRAILER' \
    --header 'Content-Encoding: aws-chunked' \
    --header 'X-Amz-Decoded-Content-Length: 0' \
    --data-binary $'0\r\n\r\n'

  # A unique allowed miss proves the later origin-log export covers this run.
  control_path="$origin_bucket_path/__gateway_method_probe__/$probe_run/GET-CONTROL"
  control_url="${gateway_base%/}/__gateway_method_probe__/$probe_run/GET-CONTROL"
  printf '%s\n' "$control_path" >"$evidence_dir/origin-control-path.txt"
  control_status="$(curl --silent --show-error --http1.1 \
    --header 'Cache-Control: no-cache' \
    --output "$evidence_dir/GET-CONTROL.body" \
    --dump-header "$evidence_dir/GET-CONTROL.headers" \
    --write-out '%{http_code}' "$control_url")"
  [[ "$control_status" == 404 ]]
  if grep -Fi -- 'X-Emfont-Gateway-Policy:' \
      "$evidence_dir/GET-CONTROL.headers" >/dev/null; then
    printf 'allowed control response carried the gateway denial marker\n' >&2
    exit 1
  fi

  known_get_status="$(curl --fail-with-body --silent --show-error --http1.1 \
    --output "$evidence_dir/known-object.bin" \
    --write-out '%{http_code}' "$known_object_url")"
  [[ "$known_get_status" == 200 ]]
  printf '%s  %s\n' "$known_object_sha256" \
    "$evidence_dir/known-object.bin" |
    sha256sum --check --strict
  known_head_status="$(curl --fail --silent --show-error --http1.1 --head \
    --dump-header "$evidence_dir/known-object-HEAD.headers" \
    --output /dev/null --write-out '%{http_code}' "$known_object_url")"
  [[ "$known_head_status" == 200 ]]

  date -u +%Y-%m-%dT%H:%M:%SZ >"$evidence_dir/probe-finished-at.txt"
)
```

Export authoritative MinIO/provider origin audit logs for the recorded UTC
window, with clock-skew margin, before log retention expires. Disable cache for
the random `__gateway_method_probe__` prefix so `GET-CONTROL` reaches origin.
The export is valid only if request paths are logged, log delivery is healthy,
and it contains `origin-control-path.txt` at least once. It must contain
none of `forbidden-origin-paths.txt`:

```bash
origin_audit=${GATEWAY_ORIGIN_AUDIT_EXPORT:?Set the origin log export}
evidence_dir=${GATEWAY_EVIDENCE_DIR:?Set the gateway evidence directory}
[[ -s "$origin_audit" ]]
grep -F -- "$(cat "$evidence_dir/origin-control-path.txt")" \
  "$origin_audit" >/dev/null
while IFS= read -r forbidden_path; do
  if grep -F -- "$forbidden_path" "$origin_audit" >/dev/null; then
    printf 'forbidden request reached origin: %s\n' "$forbidden_path" >&2
    exit 1
  fi
done <"$evidence_dir/forbidden-origin-paths.txt"
```

Retain the gateway access-log export, origin export, probe directory, and a
checksum manifest over all of them. A MinIO-generated 400/401, a missing or
generic denial marker, a body mismatch, a forbidden origin-log match, or an
origin export without the allowed control request fails the gate. Status codes
alone are not evidence that the request stopped at the gateway.

The second prefix is required because `/css/{font}` without `words` links
directly to an original font. Publishing only `_generated/*` produces valid CSS
whose font request fails with 403. Generated URLs carry a `versionId` query
parameter. The CDN must forward that parameter to S3 and include it in its cache
key; dropping or normalizing it can serve the wrong object version. Deny object
listing and all methods other than `GET` and `HEAD` at the public gateway.

The default `minio:9000` endpoint is intentionally private between containers.
Do not make it client-reachable to repair browser access; configure the
GET/HEAD-only public base URL instead.

The bucket is dedicated infrastructure. `minio-init` enables versioning, then
runs the root-credential `objectversionbackfill` helper before creating the app
user/policy or changing lifecycle rules. The helper verifies versioning before,
during, and after the run. For each current `null` version it pins the source
version and ETag, snapshots the version set, reads and SHA-256 hashes the exact
bytes, then reopens that pinned source and streams it through SHA-256 into one
non-multipart same-key PUT guarded by the destination's current ETag. The new
version carries durable content and metadata/tag digest markers. A rerun that
finds a marked current version re-verifies the pinned version, marker digests,
bytes, metadata, tags, and original null source before treating it as complete.
The helper requires exactly one new real version and verifies current/pinned
identity, size, metadata, tags, byte count, and SHA-256 equality. A concurrent
object or tag change, listing/read/PUT error, object over the guarded 5 GiB
single-PUT limit, null result version, versioning change, bucket or object
encryption, or object-lock state fails the one-shot. The maintenance window
must still stop external writers because S3 has no tag CAS. The helper leaves
the old null version as noncurrent history; it does not delete it.

After the backfill, `minio-init` replaces the entire lifecycle configuration
with the Emfont generated-object rule. Never run it against a shared bucket or
a bucket with independently managed lifecycle rules. The backfill helper emits
only key-free counters (`scanned`, `null_versions`, `rewritten`, and
`already_versioned`); a successful exit is the evidence that every rewritten
object passed the byte-hash and metadata checks. Its final structured read-back
also fails closed unless the lifecycle configuration is exactly the one
intended rule and the app principal is enabled with only the intended policy
and no group membership, and the named policy's structured read-back exactly
matches the least-privilege action/resource document. The mutation steps alone
are not proof of those final invariants; do not substitute unreviewed operator
commands for the required script behavior. Treat any failure to enable or
verify versioning, complete the backfill, or prove the final dedicated-bucket
and principal state as a hard deployment failure.

Compose passes the same `EMFONT_MINIO_ENDPOINT`, `EMFONT_MINIO_REGION`, and
`EMFONT_MINIO_SECURE` values to the controller and bundled `minio-init`; a
bootstrap against one MinIO instance with runtime traffic sent to another is
invalid. `EMFONT_MINIO_ENDPOINT` is only `host[:port]`, never a URL; TLS is
selected exclusively with `EMFONT_MINIO_SECURE`. The shipped topology supports
its private `minio:9000` endpoint. An
external or managed S3 deployment must instead use a separately reviewed,
orchestrator-specific manifest or override that omits `minio` and `minio-init`,
removes their dependency edges, targets the pre-provisioned bucket and IAM
principal, and provides network reachability to that endpoint for the controller
and separately reviewed migration/verification tooling. The current Compose
manifest does not directly support that architecture.

## Reverse proxy and client identity

The edge must overwrite, not append to, an inbound `X-Forwarded-For`. The
following Nginx example keeps the controller on loopback, leaves `/metrics`
private, and routes only Go-owned paths. Put UI and admin traffic on a separate
virtual host or replace the final 404 with the separately managed frontend.

```nginx
# /etc/nginx/snippets/emfont-controller-proxy.conf
proxy_http_version 1.1;
proxy_set_header Connection "";
proxy_set_header Host $host;
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-Host $host;
proxy_set_header X-Forwarded-For $remote_addr;
proxy_connect_timeout 5s;
proxy_send_timeout 115s;
proxy_read_timeout 115s;
```

Load that snippet from the API virtual host. The following directives belong in
Nginx's `http` context, not in the snippet above:

```nginx
# In the http context. This zone is shared by one Nginx host's workers only.
limit_req_zone $binary_remote_addr zone=emfont_generation:10m rate=20r/s;
limit_req_zone $binary_remote_addr zone=emfont_health:1m rate=5r/s;

upstream emfont_controller {
    server 127.0.0.1:8080;
    keepalive 32;
}

server {
    listen 443 ssl;
    server_name api.example.com;
    ssl_certificate /etc/letsencrypt/live/api.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.example.com/privkey.pem;

    # Optional public process liveness. It does not query dependencies.
    location = /api/v1/livez {
        limit_req zone=emfont_health burst=10 nodelay;
        add_header Cache-Control "no-store" always;
        include /etc/nginx/snippets/emfont-controller-proxy.conf;
        proxy_pass http://emfont_controller;
    }

    # Readiness carries service version/state and is for private orchestration.
    location = /api/v1/readyz { return 404; }
    location = /api/v1/healthz { return 404; }

    location ~ ^/(?:api/v1/)?(?:g|css)/ {
        limit_req zone=emfont_generation burst=40 nodelay;
        include /etc/nginx/snippets/emfont-controller-proxy.conf;
        proxy_pass http://emfont_controller;
    }

    location /api/v1/ {
        include /etc/nginx/snippets/emfont-controller-proxy.conf;
        proxy_pass http://emfont_controller;
    }

    location = /list {
        include /etc/nginx/snippets/emfont-controller-proxy.conf;
        proxy_pass http://emfont_controller;
    }

    location ^~ /info/ {
        include /etc/nginx/snippets/emfont-controller-proxy.conf;
        proxy_pass http://emfont_controller;
    }

    location / {
        return 404;
    }
}
```

Expose `/api/v1/livez` publicly only when an external monitor requires it; it
returns process status, service name, version, and time, so keep it uncached
and rate-limit it at the edge. `/api/v1/readyz` and its `/healthz` alias query
PostgreSQL and object storage and must remain on loopback or a private
orchestration listener. Public clients must receive 404 for both. The Compose
container healthcheck uses readiness internally and does not require public
exposure.

With this topology, set `EMFONT_TRUST_PROXY_HEADERS=true` as in the production
environment example and replace the example
`EMFONT_TRUSTED_PROXY_CIDRS=172.18.0.1/32` with the controller's observed
immediate Nginx peer address or addresses, using `/32` for IPv4 and `/128` for
IPv6 whenever possible. Leave proxy-header trust disabled for direct access.
If another load balancer sits in front of Nginx, configure Nginx real-IP
handling only for that balancer's fixed CIDRs; never trust forwarding headers
from arbitrary peers.

The application limiter is process-local. The Nginx zone above is host-local.
For multiple controller pods or multiple edge nodes, enforce the public quota
at an API gateway or edge limiter with shared state. Keep the application
limiter as a final bound, but do not count it as the distributed abuse-control
layer.

## Build bundled hardened images

Production Compose has no image defaults. It cannot render until all four
repository/SHA-256 pairs are supplied, and it always joins them with the
literal `@sha256:` separator. The local tags below are developer-only inputs
to standalone build and smoke validation; they are never valid production
Compose inputs:

```bash
EMFONT_BUILDX_BUILDER=emfont-production-buildkit-v0-31-0
docker buildx create \
  --name "$EMFONT_BUILDX_BUILDER" \
  --driver docker-container \
  --driver-opt \
    image=moby/buildkit:v0.31.0@sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1 \
  --bootstrap

printf '%s  %s\n' \
  fc62af55857f1c41fe262b4fe7f2dc2e9ff4e89b12a85a903f32ee180da57848 \
  backend/docker/minio/minio-cve-2026-40344-41145.patch | \
  sha256sum --check --strict

docker buildx build --builder "$EMFONT_BUILDX_BUILDER" \
  --pull --platform linux/amd64 \
  --output type=docker,rewrite-timestamp=true \
  --file backend/docker/postgres/Dockerfile \
  --tag emfont-postgres:16.14-hardened \
  backend

docker buildx build --builder "$EMFONT_BUILDX_BUILDER" \
  --pull --platform linux/amd64 \
  --output type=docker,rewrite-timestamp=true \
  --file backend/docker/minio/Dockerfile.server \
  --tag emfont/minio-server:hardened \
  backend

docker buildx build --builder "$EMFONT_BUILDX_BUILDER" \
  --pull --platform linux/amd64 \
  --output type=docker,rewrite-timestamp=true \
  --file backend/docker/minio/Dockerfile.mc \
  --tag emfont/minio-mc:hardened \
  backend

docker buildx build --builder "$EMFONT_BUILDX_BUILDER" \
  --pull --platform linux/amd64 --target test \
  --file backend/docker/minio/Dockerfile.server \
  backend
docker buildx build --builder "$EMFONT_BUILDX_BUILDER" \
  --pull --platform linux/amd64 --target test \
  --file backend/docker/minio/Dockerfile.mc \
  backend

rm -rf /tmp/emfont-minio-arm64
docker buildx build --builder "$EMFONT_BUILDX_BUILDER" \
  --pull --platform linux/arm64 --target binary \
  --file backend/docker/minio/Dockerfile.server \
  --output type=local,dest=/tmp/emfont-minio-arm64/server \
  backend
docker buildx build --builder "$EMFONT_BUILDX_BUILDER" \
  --pull --platform linux/arm64 --target binary \
  --file backend/docker/minio/Dockerfile.mc \
  --output type=local,dest=/tmp/emfont-minio-arm64/mc \
  backend
readelf --file-header /tmp/emfont-minio-arm64/server/minio | \
  grep 'Machine:.*AArch64'
readelf --file-header /tmp/emfont-minio-arm64/mc/mc | \
  grep 'Machine:.*AArch64'

BUILDX_BUILDER="$EMFONT_BUILDX_BUILDER" \
  backend/docker/postgres/verify.sh emfont-postgres:16.14-hardened
SERVER_IMAGE=emfont/minio-server:hardened \
MC_IMAGE=emfont/minio-mc:hardened \
  backend/docker/minio/smoke.sh
```

The PostgreSQL verifier rebuilds the image, checks the `setpriv` handoff,
capabilities, SQL persistence, and its blocking/full Trivy views. The MinIO
server build verifies the security patch digest, exact 2+1 patched call-site
counts, and absence of the vulnerable dynamic authorization argument before it
compiles any target. The MinIO smoke exercises both hardened images,
secret-file startup, versioning,
lifecycle configuration, and the scoped application policy. CI independently
creates complete JSON reports for all three images and passes each report
through `workflow-trivy-gate.sh`; any fixable HIGH/CRITICAL finding fails while
unfixed findings remain present for exact-digest/platform acceptance review.
It also executes both MinIO Dockerfile `test` stages, cross-compiles the scratch
`binary` targets for arm64, verifies AArch64 ELF headers, and retains
govulncheck plus full/blocking Trivy rootfs reports for both arm64 binaries.

Local tags are developer inputs only. Production publication and verification
must run through `backend-release.yml`; only its final verified release
manifest may populate the eight `*_IMAGE_REPOSITORY`/`*_IMAGE_SHA256`
variables. Never convert a local tag, candidate tag, or promoted version tag
into deployment input. The scheduled backend workflow rebuilds against current
package and vulnerability data; a scheduled failure freezes promotion until
triaged.

The host integration font test compiles against the Ubuntu runner libraries.
The separate final-image Compose smoke sends the official checksum-pinned Noto
Sans TC source through the running built controller and bundled
`emfont-fontworker`, then checks the 2,868-byte production artifact and
`3e365346851cf540ccbef2b61ca7c05c51ff93833c8a928c5a816884373819e2`
SHA-256. The verifier also runs the image's same-snapshot `hb-subset` and
`woff2_compress` reference stage and requires byte-for-byte equality before it
checks the retained baseline. The runtime image retains the canonical audit
inputs at `/usr/local/share/emfont/fontworker-build-packages.tsv`,
`fontworker-runtime-packages.tsv`, and `fontworker-package-manifest.sha256`;
retain all three with release evidence. Staging and production reject legacy or
malformed worker identities. When HarfBuzz, WOFF2, the toolchain, or any linked
runtime package changes, update the baseline only with this same-snapshot
official CLI evidence; an Ubuntu host hash is not interchangeable.

## Validate configuration

Run these commands from the repository root so the checked-in Compose path and
release evidence paths are unambiguous. Runtime helper scripts are baked into
the verified images; production Compose has no host bind mount for them.

```bash
static_env=/etc/emfont/backend.env
release_env=${EMFONT_RELEASE_ENV_FILE:-/etc/emfont/current-release.env}
release_env="$(readlink -f "$release_env")"
release_dir="$(dirname "$release_env")"
[[ -f "$static_env" && -f "$release_env" ]]
[[ "$(stat -c '%u:%g:%a' "$release_env")" == "0:0:400" ]]
if grep -Eq \
  '^[[:space:]]*(EMFONT_(BACKEND|POSTGRES|MINIO|MINIO_MC)_IMAGE(_REPOSITORY|_SHA256)?|EMFONT_VERSION|EMFONT_RELEASE_(MANIFEST_RUN_ID|MANIFEST_RUN_ATTEMPT|SOURCE_COMMIT|MANIFEST_SHA256))[[:space:]]*=' \
  "$static_env"; then
  printf 'static env contains prohibited release input\n' >&2
  exit 1
fi
if grep -Eq \
  '^[[:space:]]*(EMFONT_(HTTP|MINIO)_BIND_ADDRESS|EMFONT_MINIO_PORT|EMFONT_TRUST_PROXY_HEADERS)[[:space:]]*=' \
  "$static_env"; then
  printf 'static env contains a retired or production-pinned override\n' >&2
  exit 1
fi
(cd "$release_dir" && sha256sum --check --strict \
  release-manifest/SHA256SUMS)
expected_manifest_files=$'SHA256SUMS\ncompose-config.json\ncompose-contract.env\ndocker-compose.backend.yml\nimages.env\nrelease.env\nverification.env\nverify-compose-release.sh'
actual_manifest_files="$(find "$release_dir/release-manifest" \
  -mindepth 1 -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort)"
[[ "$actual_manifest_files" == "$expected_manifest_files" ]]
[[ -z "$(find "$release_dir/release-manifest" \
  -mindepth 1 -maxdepth 1 ! -type f -print -quit)" ]]
expected_manifest_sums=$'release-manifest/compose-config.json\nrelease-manifest/compose-contract.env\nrelease-manifest/docker-compose.backend.yml\nrelease-manifest/images.env\nrelease-manifest/release.env\nrelease-manifest/verification.env\nrelease-manifest/verify-compose-release.sh'
actual_manifest_sums="$(awk '{print $2}' \
  "$release_dir/release-manifest/SHA256SUMS" | LC_ALL=C sort)"
[[ "$actual_manifest_sums" == "$expected_manifest_sums" ]]
(cd "$release_dir/deployment-verification" && \
  sha256sum --check --strict SHA256SUMS)
bash "$release_dir/release-manifest/verify-compose-release.sh" verify \
  "$release_dir/release-manifest" \
  "$release_dir/release-manifest/images.env" >/dev/null

clean_compose_environment() {
  env \
    -u COMPOSE_PROJECT_NAME \
    -u EMFONT_VERSION \
    -u EMFONT_BACKEND_IMAGE_REPOSITORY \
    -u EMFONT_BACKEND_IMAGE_SHA256 \
    -u EMFONT_POSTGRES_IMAGE_REPOSITORY \
    -u EMFONT_POSTGRES_IMAGE_SHA256 \
    -u EMFONT_MINIO_IMAGE_REPOSITORY \
    -u EMFONT_MINIO_IMAGE_SHA256 \
    -u EMFONT_MINIO_MC_IMAGE_REPOSITORY \
    -u EMFONT_MINIO_MC_IMAGE_SHA256 \
    "$@"
}

compose() {
  clean_compose_environment docker compose \
    --env-file "$static_env" \
    --env-file "$release_env" \
    -f "$release_dir/release-manifest/docker-compose.backend.yml" "$@"
}

(
  set -Eeuo pipefail
  rendered_config="$(mktemp)"
  trap 'rm -f "$rendered_config"' EXIT

  compose config --quiet
  compose config --images
  compose config --format json >"$rendered_config"
  cmp --silent \
    <(compose config --images | LC_ALL=C sort -u) \
    <(cut -d= -f2- "$release_dir/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  [[ "$(jq -er '.services.controller.environment.EMFONT_VERSION' \
      "$rendered_config")" == \
    "$(sed -n 's/^version=//p' \
      "$release_dir/release-manifest/release.env")" ]]
  [[ "$(sed -n 's/^EMFONT_RELEASE_MANIFEST_RUN_ATTEMPT=//p' \
      "$release_env")" == \
    "$(sed -n 's/^verification_run_attempt=//p' \
      "$release_dir/release-manifest/release.env")" ]]
  [[ "$(sed -n 's/^EMFONT_RELEASE_MANIFEST_RUN_ID=//p' \
      "$release_env")" == \
    "$(sed -n 's#^verification_run=.*/##p' \
      "$release_dir/release-manifest/release.env")" ]]
  [[ "$(sed -n 's/^EMFONT_RELEASE_SOURCE_COMMIT=//p' \
      "$release_env")" == \
    "$(sed -n 's/^source_commit=//p' \
      "$release_dir/release-manifest/release.env")" ]]
  [[ "$(sed -n 's/^EMFONT_RELEASE_MANIFEST_SHA256=//p' \
      "$release_env")" == \
    "$(sha256sum "$release_dir/release-manifest/SHA256SUMS" | \
      awk '{print $1}')" ]]

  jq -e '
    def loopback_port($service; $target):
      ([.services[$service].ports[]? |
        select((.target | tonumber) == $target)]) as $ports |
      ($ports | length) == 1 and
      $ports[0].host_ip == "127.0.0.1";
    def no_published_ports($service):
      ((.services[$service].ports // []) | length) == 0;

    loopback_port("controller"; 8080) and
    no_published_ports("postgres") and
    no_published_ports("minio") and
    .networks.database.internal == true and
    .networks["object-store"].internal == true and
    .networks["object-store"].attachable == true and
    (.services.postgres.networks | keys == ["database"]) and
    (.services.minio.networks | keys == ["object-store"]) and
    (.services["minio-init"].networks | keys == ["object-store"]) and
    (.services.controller.networks | has("database")) and
    (.services.controller.networks | has("edge")) and
    (.services.controller.networks | has("object-store")) and
    .services.controller.environment.EMFONT_ENV == "production" and
    .services.controller.environment.EMFONT_RATE_LIMIT_ENABLED == "true" and
    (.services.controller.environment.EMFONT_MINIO_PUBLIC_BASE_URL |
      type == "string" and startswith("https://")) and
    .services.controller.environment.EMFONT_TRUST_PROXY_HEADERS == "true" and
    (.services.controller.environment.EMFONT_CORS_ALLOWED_ORIGINS |
      type == "string" and length > 0) and
    (.services.controller.environment.EMFONT_TRUSTED_PROXY_CIDRS |
      type == "string" and length > 0) and
    .services.controller.read_only == true and
    (.services.controller.cap_add | sort) == ["KILL", "SETGID", "SETUID"] and
    (.services.controller.cap_drop | index("ALL")) != null and
    .services.postgres.read_only == true and
    (.services.postgres.cap_add | sort) ==
      ["CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"] and
    (.services.postgres.cap_drop | index("ALL")) != null and
    .services.minio.read_only == true and
    (.services.minio.cap_add | sort) == ["SETGID", "SETUID"] and
    (.services.minio.cap_drop | index("ALL")) != null and
    .services["postgres-permissions"].user == "0:0" and
    .services["postgres-permissions"].environment.EMFONT_RUN_AS_UID == "10001" and
    .services["postgres-permissions"].environment.EMFONT_RUN_AS_GID == "10001" and
    (.services["postgres-permissions"].cap_add | sort) == ["SETGID", "SETUID"] and
    (.services["postgres-permissions"].cap_drop | index("ALL")) != null and
    (.services["postgres-permissions"].entrypoint |
      any(type == "string" and contains("load-secrets.sh"))) and
    (.services["postgres-permissions"].command |
      any(type == "string" and contains("10001:10001")))
  ' "$rendered_config" >/dev/null

  python3 - "$rendered_config" <<'PY'
import ipaddress
import json
import sys
import urllib.parse

with open(sys.argv[1], encoding="utf-8") as stream:
    config = json.load(stream)

environment = config["services"]["controller"]["environment"]
origins = [item.strip() for item in
           environment["EMFONT_CORS_ALLOWED_ORIGINS"].split(",")]
if not origins or any(not item or item == "*" for item in origins):
    raise SystemExit("production CORS requires non-wildcard explicit origins")
for origin in origins:
    parsed = urllib.parse.urlsplit(origin)
    if (parsed.scheme not in {"http", "https"} or not parsed.netloc or
            parsed.path or parsed.query or parsed.fragment):
        raise SystemExit(f"invalid CORS origin: {origin!r}")

cidrs = [item.strip() for item in
         environment["EMFONT_TRUSTED_PROXY_CIDRS"].split(",")]
if not cidrs or any(not item for item in cidrs):
    raise SystemExit("trusted proxy CIDR allowlist is empty")
for value in cidrs:
    try:
        network = ipaddress.ip_network(value, strict=True)
    except ValueError as error:
        raise SystemExit(f"invalid trusted proxy CIDR {value!r}: {error}") from error
    if network.prefixlen == 0:
        raise SystemExit(f"trusted proxy CIDR is overly broad: {value!r}")
    if (isinstance(network, ipaddress.IPv6Network) and
            network.network_address.ipv4_mapped is not None):
        raise SystemExit(f"IPv4-mapped trusted proxy CIDR is forbidden: {value!r}")

for key in ("EMFONT_BACKEND_BASE_URL", "EMFONT_MINIO_PUBLIC_BASE_URL"):
    value = environment.get(key, "").strip()
    if key == "EMFONT_MINIO_PUBLIC_BASE_URL" and not value:
        raise SystemExit("production object gateway base URL is required")
    if value:
        parsed = urllib.parse.urlsplit(value)
        if parsed.scheme != "https":
            raise SystemExit(f"{key} must use HTTPS in production")
        if (not parsed.netloc or parsed.username is not None or
                parsed.password is not None or "?" in value or "#" in value):
            raise SystemExit(f"{key} has forbidden URL components")
PY
)
```

The comparison makes the verified manifest, rather than a tag or manually
assembled reference, authoritative for every rendered service. Fail the
release if it differs, if any rendered image is not an exact digest, or if an
image has a fixable HIGH/CRITICAL Trivy finding. Scan the deployed architecture;
add every other architecture that the release supports to
`EMFONT_SCAN_PLATFORMS`. The same run must also retain a complete report,
including unfixed findings, for review.

The static gate deliberately rejects retired bind-address variables and the
retired `EMFONT_MINIO_PORT` variable and production-pinned proxy-trust variable
instead of silently ignoring them. The application repeats semantic validation
at startup: production rejects CORS `*`, a disabled rate limiter, and an empty
or non-HTTPS object gateway URL; trusted proxy parsing rejects malformed or
non-canonical prefixes, host bits, IPv4-mapped IPv6, and both `0.0.0.0/0` and
`::/0`. A config-render pass is not permission to weaken any runtime rule.

Use `compose`/`clean_compose_environment` for every interactive operation; do
not invoke raw `docker compose`. Shell variables otherwise have higher
interpolation precedence than `--env-file` and could replace a manifest-owned
repository, digest, version, or project name. Scheduled units must apply the
same environment clearing shown below.

```bash
(
  set -Eeuo pipefail
  mc_config_dir=
  trap '
    rc=$?
    if [[ -n "$mc_config_dir" ]]; then
      rm -rf -- "$mc_config_dir"
    fi
    if ((rc != 0)); then
      printf "image gate failed; do not deploy\n" >&2
    fi
    exit "$rc"
  ' EXIT

  read -r -a platforms <<<"${EMFONT_SCAN_PLATFORMS:-linux/amd64}"
  mapfile -t images < <(compose config --images | sort -u)
  ((${#images[@]} > 0))
  report_dir=${EMFONT_SCAN_REPORT_DIR:?Set a protected release report directory}
  install -d -m 0700 "$report_dir"
  existing_report="$(find "$report_dir" -mindepth 1 -maxdepth 1 -print -quit)"
  [[ -z "$existing_report" ]]

  trivy image --download-db-only
  trivy --version >"$report_dir/trivy-version.txt"
  for image in "${images[@]}"; do
    [[ "$image" =~ @sha256:[0-9a-f]{64}$ ]]
    image_id="$(printf '%s' "$image" | sha256sum | cut -d ' ' -f 1)"
    for platform in "${platforms[@]}"; do
      platform_id=${platform//\//-}
      printf '%s\t%s\t%s\n' \
        "$image_id" "$platform" "$image" \
        >>"$report_dir/images.tsv"
      trivy image \
        --image-src remote \
        --platform "$platform" \
        --skip-db-update \
        --exit-code 0 \
        --scanners vuln \
        --format json \
        --output "$report_dir/$image_id-$platform_id-all.json" \
        "$image"
      backend/scripts/workflow-trivy-gate.sh \
        "$report_dir/$image_id-$platform_id-all.json" \
        "$report_dir/$image_id-$platform_id-fixable.json"

      jq -r '
        .Results[]?.Vulnerabilities[]? |
        [
          .VulnerabilityID,
          .PkgName,
          .InstalledVersion,
          (.FixedVersion // ""),
          .Severity
        ] | @tsv
      ' "$report_dir/$image_id-$platform_id-all.json" |
        sort -u >"$report_dir/$image_id-$platform_id-findings.tsv"
    done
  done

  (cd "$report_dir" && sha256sum -- * >SHA256SUMS)
)
```

The `*-fixable.json` files are the automated blocking gate. The `*-all.json`
and normalized `*-findings.tsv` files are the review input. Compare each TSV to
the prior accepted digest, review every added or changed row, and attach a
strict `EMFONT_CVE_ACCEPTANCE_B64` entry to this release. Its canonical digest
must be authenticated by the exact GitHub environment approval comment defined
above. Fixable HIGH/CRITICAL findings are not eligible for acceptance. Unfixed
findings are not silently inherited from an older digest; missing review,
expired acceptance, or an unexplained delta keeps the release closed.

General acceptance of an unfixed finding does not override a topology blocker.
CVE-2026-40344, CVE-2026-41145, CVE-2026-34204, and CVE-2026-39414 require the
bundled MinIO listener to remain unreachable from public and untrusted networks
and require an independently enforced `GET`/`HEAD`-only gateway. The latter two
also require retained configuration and code-review evidence that neither the
controller/app policy nor an operational workflow invokes replication or S3
Select. If those controls cannot be proved in the rendered deployment, policy
review, and external method probe, deployment is blocked; use a fixed managed
object store through the separately orchestrated architecture described above
instead.

Trivy may continue to report all four CVEs. Only CVE-2026-40344 and
CVE-2026-41145 have repository source changes; CVE-2026-34204 and
CVE-2026-39414 remain unpatched. A narrowly scoped risk exception is valid only
when the release record proves all of the following:

- The server label is `RELEASE.2025-10-15T17-29-55Z-emfont.3`.
  `io.emfont.security-patches` names only the source-patched CVE-2026-40344 and
  CVE-2026-41145. `io.emfont.security-topology-controls` names the four unfixed
  topology findings CVE-2026-33322, CVE-2026-33419, CVE-2026-34204, and
  CVE-2026-39414. Label presence is inventory evidence, not proof that any of
  those four findings was patched.
- `backend/docker/minio/minio-cve-2026-40344-41145.patch` has SHA-256
  `fc62af55857f1c41fe262b4fe7f2dc2e9ff4e89b12a85a903f32ee180da57848`.
- The Dockerfile build assertions find exactly two patched calls in
  `cmd/object-handlers.go`, one in `cmd/object-multipart-handlers.go`, and no
  remaining authorization-header-controlled call.
- The patched server `test` target, runtime smoke, amd64 image scans, and arm64
  binary govulncheck/Trivy gates pass for the exact release inputs.
- MinIO remains private with no host port and only the internal `object-store`
  attachment; replication and S3 Select remain unused and
  unavailable to the app workflow, and the public gateway passes the method and
  streaming-header rejection probes below.
- A separate acceptance entry exists for every exact scanner identity for all
  four CVEs on each platform where it is reported. The patched-call-site
  evidence may support only the first two; the latter two acceptances must name
  the topology and feature-exclusion controls explicitly. Every named security
  reviewer supplies the hash-bound GitHub environment approval.

Any patch, assertion, label, upstream revision, image digest, or gateway
change invalidates the exception. It does not cover another CVE or any fixable
finding.

A separately orchestrated managed service must have a documented provider patch
policy, supported engine version, encryption, backups, and restore procedure,
in addition to the pre-provisioned versioning, IAM, lifecycle, service omission,
and network-reachability requirements above. Keep the environment file,
complete and blocking scan reports, risk review, applicable image provenance,
and the alternate deployment's exact rendered configuration with the release
record, but never archive secret contents with it.

### Canonical service reconciliation and identity

Define these helpers in the same Bash operator shell as `compose`. First
deployment, routine promotion, rollback, and backup all use this exact identity
gate. `docker compose run --rm` is useful for probes, but it cannot establish
the canonical one-shot container identity and is not a deployment substitute.

```bash
release_manifest_ref() {
  local release_path=$1
  local key=$2
  local -a values=()
  mapfile -t values < <(awk -v key="$key" '
    index($0, key "=") == 1 {
      sub(/^[^=]*=/, "")
      print
    }
  ' "$release_path/release-manifest/images.env")
  ((${#values[@]} == 1))
  [[ "${values[0]}" =~ ^[^@]+@sha256:[0-9a-f]{64}$ ]]
  printf '%s' "${values[0]}"
}

canonical_container_ids() {
  local compose_command=$1
  local service=$2
  local container_id oneoff

  while IFS= read -r container_id; do
    [[ -n "$container_id" ]] || continue
    oneoff="$(docker container inspect --format \
      '{{ index .Config.Labels "com.docker.compose.oneoff" }}' \
      "$container_id")"
    if [[ "$oneoff" == False ]]; then
      printf '%s\n' "$container_id"
    fi
  done < <("$compose_command" ps --all --quiet "$service")
}

one_shot_failure_evidence() {
  local compose_command=$1
  local service=$2
  local container_id=$3

  printf 'one-shot service=%s container=%s state=%s exit_status=%s\n' \
    "$service" "$container_id" \
    "$(docker container inspect --format '{{.State.Status}}' \
      "$container_id" 2>/dev/null || printf unknown)" \
    "$(docker container inspect --format '{{.State.ExitCode}}' \
      "$container_id" 2>/dev/null || printf unknown)" >&2
  if [[ "$service" == minio-init ]]; then
    docker container logs "$container_id" 2>&1 | sed -nE \
      '/^object-version-backfill: scanned=[0-9]+ null_versions=[0-9]+ rewritten=[0-9]+ already_versioned=[0-9]+$/p' >&2 || true
  else
    "$compose_command" logs --no-log-prefix "$service" >&2 || true
  fi
}

run_release_one_shot() (
  local compose_command=$1
  local service=$2
  local timeout_seconds=${3:-${EMFONT_ONE_SHOT_TIMEOUT_SECONDS:-3600}}
  local evidence_label=${4:-}
  local evidence_file=${5:-}
  local stop_grace_seconds=${EMFONT_ONE_SHOT_STOP_GRACE_SECONDS:-30}
  local lock_file=${EMFONT_ONE_SHOT_LOCK_FILE:-/run/lock/emfont-release-one-shot.lock}
  local container_id exit_code existing_id existing_state wait_status
  local -a container_ids=()

  [[ "$service" =~ ^[a-z0-9][a-z0-9-]*$ ]]
  [[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]]
  [[ "$stop_grace_seconds" =~ ^[1-9][0-9]*$ ]]
  if [[ -n "$evidence_label" || -n "$evidence_file" ]]; then
    [[ "$service" == minio-init ]]
    [[ "$evidence_label" =~ ^[a-z0-9][a-z0-9-]*$ ]]
    if [[ -n "$evidence_file" ]]; then
      [[ "$evidence_file" == /* && ! -e "$evidence_file" ]]
    fi
  fi
  [[ "$lock_file" == /* && ! -L "$lock_file" ]]
  (umask 077 && : >>"$lock_file")
  [[ -f "$lock_file" && ! -L "$lock_file" ]]
  exec 9>>"$lock_file"
  if ! flock --exclusive --nonblock 9; then
    printf 'another release one-shot is active; refusing to overlap %s\n' \
      "$service" >&2
    return 1
  fi

  # Never let --force-recreate stop or remove a live one-shot. The active
  # owner must finish, or its timeout path must stop, kill, and wait for it.
  mapfile -t container_ids < <(
    "$compose_command" ps --all --quiet "$service"
  )
  for existing_id in "${container_ids[@]}"; do
    [[ -n "$existing_id" ]] || continue
    existing_state="$(docker container inspect --format \
      '{{.State.Status}}' "$existing_id")"
    case "$existing_state" in
      created|exited) ;;
      *)
        printf 'refusing to recreate live %s container %s in state %s\n' \
          "$service" "$existing_id" "$existing_state" >&2
        return 1
        ;;
    esac
  done

  "$compose_command" up --detach --no-deps --force-recreate "$service"
  container_ids=()
  mapfile -t container_ids < <(
    canonical_container_ids "$compose_command" "$service"
  )
  ((${#container_ids[@]} == 1))
  container_id=${container_ids[0]}
  [[ "$container_id" =~ ^[0-9a-f]{64}$ ]]

  if exit_code="$(timeout --foreground "$timeout_seconds" \
      docker container wait "$container_id")"; then
    wait_status=0
  else
    wait_status=$?
    printf '%s wait failed or timed out after %s seconds (wait status %s)\n' \
      "$service" "$timeout_seconds" "$wait_status" >&2
    docker container stop --time "$stop_grace_seconds" \
      "$container_id" >/dev/null 2>&1 || true
    docker container kill "$container_id" >/dev/null 2>&1 || true
    docker container wait "$container_id" >/dev/null 2>&1 || true
    one_shot_failure_evidence "$compose_command" "$service" "$container_id"
    return 1
  fi
  if [[ "$exit_code" != 0 ]] ||
      [[ "$(docker container inspect --format '{{.State.Status}}' \
        "$container_id")" != exited ]]; then
    one_shot_failure_evidence "$compose_command" "$service" "$container_id"
    return 1
  fi
  if [[ -n "$evidence_label" ]]; then
    if [[ -n "$evidence_file" ]]; then
      (umask 077 && sanitized_minio_init_evidence \
        "$compose_command" "$evidence_label" >"$evidence_file")
    else
      sanitized_minio_init_evidence "$compose_command" "$evidence_label"
    fi
  fi
)

sanitized_minio_init_evidence() {
  local compose_command=$1
  local run_label=$2
  local container_id state exit_status sanitized_counts
  local scanned null_versions rewritten already_versioned
  local -a container_ids=()
  local -a matches=()

  [[ "$run_label" =~ ^[a-z0-9][a-z0-9-]*$ ]]
  mapfile -t container_ids < <(
    canonical_container_ids "$compose_command" minio-init
  )
  ((${#container_ids[@]} == 1))
  container_id=${container_ids[0]}
  state="$(docker container inspect --format '{{.State.Status}}' \
    "$container_id")"
  exit_status="$(docker container inspect --format '{{.State.ExitCode}}' \
    "$container_id")"
  [[ "$state:$exit_status" == exited:0 ]]

  sanitized_counts="$(docker container logs "$container_id" 2>&1 | sed -nE \
    's/^object-version-backfill: scanned=([0-9]+) null_versions=([0-9]+) rewritten=([0-9]+) already_versioned=([0-9]+)$/\1 \2 \3 \4/p')"
  mapfile -t matches < <(printf '%s' "$sanitized_counts")
  ((${#matches[@]} == 1))
  read -r scanned null_versions rewritten already_versioned \
    <<<"${matches[0]}"
  printf 'run=%s\nstate=%s\nexit_status=%s\n' \
    "$run_label" "$state" "$exit_status"
  printf 'scanned=%s\nnull_versions=%s\nrewritten=%s\nalready_versioned=%s\n' \
    "$scanned" "$null_versions" "$rewritten" "$already_versioned"
}

reconcile_release_state() {
  local compose_command=$1

  "$compose_command" up --detach --no-deps --force-recreate \
    --wait --wait-timeout 180 postgres minio
  run_release_one_shot "$compose_command" migrate \
    "${EMFONT_MIGRATE_TIMEOUT_SECONDS:-600}"
  run_release_one_shot "$compose_command" postgres-permissions \
    "${EMFONT_POSTGRES_PERMISSIONS_TIMEOUT_SECONDS:-300}"
  run_release_one_shot "$compose_command" minio-init \
    "${EMFONT_MINIO_INIT_TIMEOUT_SECONDS:-3600}" reconciliation
}

verify_release_containers() {
  local compose_command=$1
  local release_path=$2
  local evidence_file=${3:-}
  local service key expected_ref rendered_ref container_id configured_ref
  local image_id expected_image_id repo_digests state health exit_code label
  local port_bindings runtime_networks object_store_network database_network
  local edge_network
  local -a services=(
    postgres minio migrate postgres-permissions minio-init controller
  )
  local -a container_ids=()
  local -A manifest_key=()
  local -A seen_container=()

  manifest_key[postgres]=postgres
  manifest_key[minio]=minio
  manifest_key[migrate]=backend
  manifest_key[postgres-permissions]=postgres
  manifest_key[minio-init]=minio_mc
  manifest_key[controller]=backend

  object_store_network="$("$compose_command" config --format json | \
    jq -er '.networks["object-store"].name')"
  database_network="$("$compose_command" config --format json | \
    jq -er '.networks.database.name')"
  edge_network="$("$compose_command" config --format json | \
    jq -er '.networks.edge.name')"

  if [[ -n "$evidence_file" ]]; then
    (umask 077 && : >"$evidence_file")
    printf '%s\n' \
      $'service\tcontainer_id\texact_ref\timage_id\trepo_digests\tstate\thealth\texit_code' \
      >"$evidence_file"
  fi

  for service in "${services[@]}"; do
    key=${manifest_key[$service]}
    expected_ref="$(release_manifest_ref "$release_path" "$key")"
    rendered_ref="$("$compose_command" config --format json | jq -er \
      --arg service "$service" '.services[$service].image')"
    [[ "$rendered_ref" == "$expected_ref" ]]

    container_ids=()
    mapfile -t container_ids < <(
      canonical_container_ids "$compose_command" "$service"
    )
    ((${#container_ids[@]} == 1))
    container_id=${container_ids[0]}
    [[ "$container_id" =~ ^[0-9a-f]{64}$ ]]
    [[ -z "${seen_container[$container_id]+set}" ]]
    seen_container[$container_id]=1

    label="$(docker container inspect --format \
      '{{ index .Config.Labels "com.docker.compose.service" }}' \
      "$container_id")"
    [[ "$label" == "$service" ]]
    [[ "$(docker container inspect --format \
      '{{ index .Config.Labels "com.docker.compose.oneoff" }}' \
      "$container_id")" == False ]]

    configured_ref="$(docker container inspect --format '{{.Config.Image}}' \
      "$container_id")"
    image_id="$(docker container inspect --format '{{.Image}}' "$container_id")"
    expected_image_id="$(docker image inspect --format '{{.Id}}' \
      "$expected_ref")"
    [[ "$configured_ref" == "$expected_ref" ]]
    [[ "$image_id" == "$expected_image_id" ]]
    [[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]]

    repo_digests="$(docker image inspect --format '{{json .RepoDigests}}' \
      "$image_id")"
    jq -e --arg expected "$expected_ref" \
      'type == "array" and index($expected) != null' \
      <<<"$repo_digests" >/dev/null

    port_bindings="$(docker container inspect --format \
      '{{json .HostConfig.PortBindings}}' "$container_id")"
    runtime_networks="$(docker container inspect --format \
      '{{json .NetworkSettings.Networks}}' "$container_id")"
    case "$service" in
      controller)
        jq -e '
          (."8080/tcp" // []) as $bindings |
          ($bindings | length) == 1 and
          $bindings[0].HostIp == "127.0.0.1" and
          ($bindings[0].HostPort | test("^[0-9]+$"))
        ' <<<"$port_bindings" >/dev/null
        jq -e \
          --arg object_store "$object_store_network" \
          --arg database "$database_network" \
          --arg edge "$edge_network" '
            (keys | length) == 3 and
            has($object_store) and has($database) and has($edge)
          ' <<<"$runtime_networks" >/dev/null
        ;;
      postgres|minio)
        jq -e '. == null or (type == "object" and length == 0)' \
          <<<"$port_bindings" >/dev/null
        if [[ "$service" == postgres ]]; then
          jq -e --arg database "$database_network" \
            'keys == [$database]' <<<"$runtime_networks" >/dev/null
        else
          jq -e --arg object_store "$object_store_network" \
            'keys == [$object_store]' <<<"$runtime_networks" >/dev/null
        fi
        ;;
    esac

    state="$(docker container inspect --format '{{.State.Status}}' \
      "$container_id")"
    health="$(docker container inspect --format \
      '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
      "$container_id")"
    exit_code="$(docker container inspect --format '{{.State.ExitCode}}' \
      "$container_id")"
    case "$service" in
      postgres|minio|controller)
        [[ "$state:$health" == running:healthy ]]
        ;;
      migrate|postgres-permissions|minio-init)
        [[ "$state:$exit_code" == exited:0 ]]
        ;;
    esac

    if [[ -n "$evidence_file" ]]; then
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$service" "$container_id" "$expected_ref" "$image_id" \
        "$repo_digests" "$state" "$health" "$exit_code" \
        >>"$evidence_file"
    fi
  done
}
```

The nonblocking host lock prevents concurrent helper invocations, including an
initializer and its idempotency rerun. The helper also refuses to call
`--force-recreate` while any container for that one-shot service is live. Set
`EMFONT_ONE_SHOT_LOCK_FILE` to a protected absolute path when `/run/lock` is not
appropriate, and use that same deployment-wide path for every operator process.
`EMFONT_MINIO_INIT_TIMEOUT_SECONDS` defaults to 3600 seconds; set it higher than
the measured worst-case backfill duration for the bucket.
`EMFONT_ONE_SHOT_STOP_GRACE_SECONDS` controls the graceful-stop interval. A wait
failure or timeout always performs an explicit stop, kill, and wait and leaves
the exited container available for inspection before a later helper invocation
may recreate it. Do not bypass this serialization with a direct initializer
`docker compose run`. When the optional evidence label and absolute output path
are supplied, the helper writes sanitized evidence before releasing the lock,
so an idempotency rerun cannot replace the inspected container first.

Raw `minio-init` output can contain the application access-key identifier.
Never print, tee, upload, or archive that output. The failure helper allowlists
only the key-free backfill counter line, and
`sanitized_minio_init_evidence` emits only run state, exit status, and those
counters. Those sanitized fields are the only initializer output permitted in
the release evidence system.

The manifest reference is an OCI index digest. The container's `.Image` is the
platform image-config ID, so the gate checks both facts: `.Config.Image` and an
entry in local `RepoDigests` must equal the exact manifest reference, while
`.Image` must equal the image ID resolved locally from that reference. Recording
only the rendered reference, a tag, or a shortened container ID is insufficient.
The same inspect pass checks the controller's loopback binding, the absence of
PostgreSQL and MinIO host ports, and the stateful services' exact internal
network attachments; rendered topology alone is not runtime evidence. Verify
the separately deployed gateway's dual attachment with the Object download URLs
gate.

## First deployment

Terminate TLS at the read-only object gateway before starting the controller.
The bundled MinIO endpoint stays private. This greenfield procedure keeps the
writer stopped until every stateful one-shot job succeeds and migration 10 is
confirmed. A greenfield target has no stable release, so select its initial
verified manifest before rendering Compose:

```bash
initial_release_env="$(readlink -f \
  "${INITIAL_RELEASE_ENV_FILE:?Set the initial verified release env}")"
initial_release_dir="$(dirname "$initial_release_env")"
(cd "$initial_release_dir" && sha256sum --check --strict \
  release-manifest/SHA256SUMS)
(cd "$initial_release_dir/deployment-verification" && \
  sha256sum --check --strict SHA256SUMS)
temporary_link="/etc/emfont/.current-release.env.$$"
sudo ln -s "$initial_release_env" "$temporary_link"
sudo mv -Tf "$temporary_link" /etc/emfont/current-release.env
release_env="$(readlink -f /etc/emfont/current-release.env)"
release_dir="$(dirname "$release_env")"

(
  set -Eeuo pipefail
  trap '
    rc=$?
    if ((rc != 0)); then
      compose stop controller >/dev/null 2>&1 || true
      compose logs --no-log-prefix \
        postgres minio migrate postgres-permissions >&2 || true
      printf "bootstrap failed; keep every writer stopped and preserve state\n" >&2
    fi
    exit "$rc"
  ' EXIT

  compose config --quiet
  cmp --silent \
    <(compose config --images | LC_ALL=C sort -u) \
    <(cut -d= -f2- "$release_dir/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  [[ "$(compose config --format json | \
      jq -er '.services.controller.environment.EMFONT_VERSION')" == \
    "$(sed -n 's/^version=//p' \
      "$release_dir/release-manifest/release.env")" ]]
  compose pull postgres minio migrate postgres-permissions minio-init controller
  reconcile_release_state compose
  compose run --rm --no-deps migrate \
    /usr/local/bin/emfont-migrate -command status

  applied_migrations="$(compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --tuples-only --no-align \
      --command="
        WITH latest AS (
          SELECT DISTINCT ON (version_id) version_id, is_applied
          FROM goose_db_version
          WHERE version_id > 0
          ORDER BY version_id, id DESC
        )
        SELECT string_agg(
          version_id::text || ':' || is_applied::text,
          ',' ORDER BY version_id
        )
        FROM latest
      "
  ')"
  [[ "$applied_migrations" == \
    "1:true,2:true,3:true,4:true,5:true,6:true,7:true,8:true,9:true,10:true" ]]
)
```

That block creates the attachable `object-store` network. Now deploy or attach
the HTTPS gateway, run the dual-network/no-MinIO-port checks from Object
download URLs, and retain their output. Set the confirmation only after those
checks pass. Only then may the controller start:

```bash
export EMFONT_GATEWAY_TOPOLOGY_VERIFIED=confirmed
(
  set -Eeuo pipefail
  trap '
    rc=$?
    if ((rc != 0)); then
      compose stop controller >/dev/null 2>&1 || true
      compose logs --no-log-prefix controller >&2 || true
      printf "controller gate failed; keep every writer stopped\n" >&2
    fi
    exit "$rc"
  ' EXIT

  [[ "${EMFONT_GATEWAY_TOPOLOGY_VERIFIED:-}" == confirmed ]]
  compose up --detach --no-deps --force-recreate controller
  timeout 120 bash -c \
    'until curl -fsS http://127.0.0.1:8080/api/v1/readyz >/dev/null; do sleep 2; done'
  verify_release_containers compose "$release_dir"
  compose ps --all

  for secret_name in \
    postgres_app_password \
    minio_app_access_key \
    minio_app_secret_key \
    metrics_bearer_token
  do
    [[ "$(compose exec -T controller \
      stat -c '%u:%g:%a' "/run/secrets/$secret_name")" == "0:0:400" ]]
    [[ "$(compose exec -T controller \
      stat -c '%u:%g:%a' "/run/host-secrets/$secret_name")" == "0:0:600" ]]
  done
  controller_pid="$(compose exec -T controller sh -ec '
    controller_pid=
    for candidate in $(cat /proc/1/task/1/children); do
      case "$(cat "/proc/$candidate/comm")" in
        emfont-controll*)
          [ -z "$controller_pid" ]
          controller_pid=$candidate
          ;;
      esac
    done
    [ -n "$controller_pid" ]
    printf "%s" "$controller_pid"
  ')"
  compose exec -T --user 10001:10001 \
    -e CONTROLLER_PID="$controller_pid" controller sh -ec '
    for directory in /run/secrets /run/host-secrets; do
      for secret in "$directory"/*; do
        if [ -r "$secret" ]; then
          printf "runtime UID can read protected secret %s\n" "$secret" >&2
          exit 1
        fi
      done
    done
    if cat "/proc/$CONTROLLER_PID/environ" >/dev/null 2>&1; then
      printf "same-UID process can read controller environment\n" >&2
      exit 1
    fi
  '

  curl --fail --show-error http://127.0.0.1:8080/api/v1/livez
  curl --fail --show-error http://127.0.0.1:8080/api/v1/readyz
)
```

`postgres-permissions` cannot reach its SQL helper unless `load-secrets.sh`
successfully stages both files and drops to `10001:10001`; its Compose command
checks that runtime identity before executing reconciliation. On any greenfield
failure, leave the initial manifest link in place for diagnosis and rerun only
after fixing the cause. Do not select a different manifest against partially
initialized volumes without a reviewed recovery decision.

Do not route public traffic yet. Inspect the non-MinIO one-shot logs, retain the
sanitized initializer evidence, and independently read back the object-store
configuration:

```bash
set -Eeuo pipefail
compose logs --no-log-prefix migrate postgres-permissions
object_config="$(compose run --rm --no-deps minio-init /bin/sh -ec '
  printf "%s\n%s\n" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" | \
    mc alias set verify http://minio:9000 >/dev/null
  mc version info --json "verify/$EMFONT_MINIO_BUCKET"
  mc ilm rule ls --json "verify/$EMFONT_MINIO_BUCKET"
')"
printf '%s\n' "$object_config" | jq -s -e '
  length == 2 and
  .[0].status == "success" and
  .[0].versioning.status == "Enabled" and
  .[1].status == "success" and
  (.[1].config.Rules | length == 1) and
  .[1].config.Rules[0].Status == "Enabled" and
  .[1].config.Rules[0].Filter.Prefix == "_generated/" and
  .[1].config.Rules[0].Expiration.ExpiredObjectDeleteMarker == true and
  .[1].config.Rules[0].NoncurrentVersionExpiration.NoncurrentDays > 0
' >/dev/null

database_security="$(
  compose run --rm --no-deps -T \
    postgres-permissions /bin/sh -s <<'SCRIPT'
set -eu
export PGPASSWORD="$EMFONT_POSTGRES_APP_PASSWORD"
psql --no-psqlrc --host="$EMFONT_POSTGRES_HOST" \
  --username="$EMFONT_POSTGRES_APP_USER" \
  --dbname="$EMFONT_POSTGRES_DB" --quiet --tuples-only --no-align <<'SQL'
SELECT
  NOT has_database_privilege(current_user, current_database(), 'TEMPORARY')
  AND NOT has_table_privilege(
    current_user, 'public.font_artifact_quota', 'UPDATE'
  )
  AND has_column_privilege(
    current_user, 'public.font_artifact_quota', 'singleton', 'UPDATE'
  )
  AND NOT has_column_privilege(
    current_user, 'public.font_artifact_quota', 'artifact_count', 'UPDATE'
  )
  AND NOT has_column_privilege(
    current_user, 'public.font_artifact_quota', 'accounted_bytes', 'UPDATE'
  )
  AND quota.artifact_count = actual.artifact_count
  AND quota.accounted_bytes = actual.accounted_bytes
FROM font_artifact_quota AS quota
CROSS JOIN (
  SELECT count(*)::BIGINT AS artifact_count,
         COALESCE(sum(quota_bytes), 0)::BIGINT AS accounted_bytes
  FROM font_artifacts
) AS actual
WHERE quota.singleton;
SQL
SCRIPT
)"
[[ "$database_security" == t ]]
```

The object-store probe independently confirms versioning and the lifecycle
shape, but it is not a substitute for the initializer's required final
app-principal and lifecycle read-back. Do not add an unreviewed `mc admin`
recipe here and treat it as equivalent. Any release whose `minio-init` script
only performs mutations remains blocked; the fixed production implementation
must enforce both final invariants with fail-closed checks.

After readiness succeeds, verify one real font generation and download through
the public HTTPS `GET`/`HEAD` gateway. A readiness response alone does not prove
that the external object-delivery path is routable or correctly restricted.

## First Go rollout on existing data

Migrations 3 through 9 set a 5-second PostgreSQL `lock_timeout` and a 5-minute
`statement_timeout`. Those bounds prevent indefinite waiting; they do not make
the first Go rollout online-safe. Migration 3 changes a column type and creates
an index, migration 4 updates existing rows and creates an index, migration 5
adds the global fence and object-version identity, and migration 6 adds terminal
failure classification and constraints. Migration 7 locks and reconciles the
legacy font tables, backfills nullable values, converts legacy JSON/timestamps,
replaces constraints/defaults, and advances sequences. Migration 8 rewrites
every existing artifact with a persisted reservation and installs the positive
reservation and size-within-reservation invariants. Migration 9 adds the stored
`quota_bytes` expression, initializes the singleton count/byte ledger, and
installs ledger-first locking, row accounting, cascade-delete locking, and
`TRUNCATE` rejection triggers. Run this transition in a maintenance window.

Before the coordinated backup and migration:

1. Remove generation and admin traffic from the edge.
2. Stop every Go controller replica and canary.
3. Stop the legacy Node/admin writer and every importer or background writer.
4. Disable the cleanup scheduler and wait for any active cleanup run to exit.
5. Verify the writer inventory is empty, then take the coordinated backup in
   the Backup section while all writers remain stopped.

The repository cannot know how the external Node/admin deployment is managed.
Use its documented stop command, verify it independently, and set the following
confirmation only after all non-Compose writers are stopped. Also set
`EMFONT_OBJECT_VERSION_BACKFILL_EVIDENCE_DIR` to a new absolute staging
directory whose contents will be copied to the immutable release evidence
system. The two `minio-init` runs below must occur before the first upgraded
controller starts: the first performs and verifies any required rewrites; the
second proves the operation is idempotent with zero remaining null versions and
zero additional rewrites.

```bash
export EMFONT_EXTERNAL_WRITERS_STOPPED=confirmed
candidate_release_env="$(readlink -f \
  "${CANDIDATE_RELEASE_ENV_FILE:?Set the verified candidate release env}")"
candidate_release_dir="$(dirname "$candidate_release_env")"
backfill_evidence_dir=${EMFONT_OBJECT_VERSION_BACKFILL_EVIDENCE_DIR:?Set a new absolute object-version-backfill evidence directory}
[[ "$backfill_evidence_dir" == /* ]]
[[ ! -e "$backfill_evidence_dir" ]]
install -d -m 0700 "$backfill_evidence_dir"
(cd "$candidate_release_dir" && sha256sum --check --strict \
  release-manifest/SHA256SUMS)
(cd "$candidate_release_dir/deployment-verification" && \
  sha256sum --check --strict SHA256SUMS)
candidate_compose() {
  clean_compose_environment docker compose \
    --env-file /etc/emfont/backend.env \
    --env-file "$candidate_release_env" \
    -f "$candidate_release_dir/release-manifest/docker-compose.backend.yml" "$@"
}

(
  set -Eeuo pipefail
  umask 077
  trap '
    rc=$?
    if ((rc != 0)); then
      printf "migration failed; keep every writer stopped\n" >&2
    fi
    exit "$rc"
  ' EXIT

  [[ "${EMFONT_EXTERNAL_WRITERS_STOPPED:-}" == "confirmed" ]]
  compose stop controller
  if systemctl is-active --quiet emfont-fontcleanup.timer; then
    sudo systemctl stop emfont-fontcleanup.timer
  fi
  if systemctl is-active --quiet emfont-fontcleanup.service; then
    sudo systemctl stop emfont-fontcleanup.service
  fi
  [[ -z "$(compose ps --status running --quiet controller)" ]]
  running_cleanup="$(
    compose --profile maintenance ps \
      --status running --quiet fontcleanup
  )"
  [[ -z "$running_cleanup" ]]

  candidate_compose config --quiet
  cmp --silent \
    <(candidate_compose config --images | LC_ALL=C sort -u) \
    <(cut -d= -f2- \
      "$candidate_release_dir/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  [[ "$(candidate_compose config --format json | \
      jq -er '.services.controller.environment.EMFONT_VERSION')" == \
    "$(sed -n 's/^version=//p' \
      "$candidate_release_dir/release-manifest/release.env")" ]]
  candidate_compose pull \
    postgres minio migrate postgres-permissions minio-init controller

  backfill_counts() {
    local evidence_file=$1
    awk -F= '
      $1 == "scanned" { scanned = $2; scanned_count++ }
      $1 == "null_versions" { null_versions = $2; null_count++ }
      $1 == "rewritten" { rewritten = $2; rewritten_count++ }
      $1 == "already_versioned" {
        already_versioned = $2
        versioned_count++
      }
      END {
        if (scanned_count != 1 || null_count != 1 ||
            rewritten_count != 1 || versioned_count != 1 ||
            scanned !~ /^[0-9]+$/ || null_versions !~ /^[0-9]+$/ ||
            rewritten !~ /^[0-9]+$/ || already_versioned !~ /^[0-9]+$/) {
          exit 1
        }
        print scanned, null_versions, rewritten, already_versioned
      }
    ' "$evidence_file"
  }

  run_release_one_shot candidate_compose minio-init \
    "${EMFONT_MINIO_INIT_TIMEOUT_SECONDS:-3600}" first-run \
    "$backfill_evidence_dir/first-run.env"
  first_counts="$(backfill_counts "$backfill_evidence_dir/first-run.env")"
  read -r first_scanned first_null first_rewritten first_versioned \
    <<<"$first_counts"
  ((first_scanned == first_null + first_versioned))
  ((first_rewritten == first_null))

  run_release_one_shot candidate_compose minio-init \
    "${EMFONT_MINIO_INIT_TIMEOUT_SECONDS:-3600}" idempotency-rerun \
    "$backfill_evidence_dir/idempotency-rerun.env"
  second_counts="$(backfill_counts \
    "$backfill_evidence_dir/idempotency-rerun.env")"
  read -r second_scanned second_null second_rewritten second_versioned \
    <<<"$second_counts"
  ((second_scanned == first_scanned))
  ((second_null == 0))
  ((second_rewritten == 0))
  ((second_versioned == second_scanned))
  {
    printf 'first_scanned=%s\n' "$first_scanned"
    printf 'first_null_versions=%s\n' "$first_null"
    printf 'first_rewritten=%s\n' "$first_rewritten"
    printf 'first_already_versioned=%s\n' "$first_versioned"
    printf 'rerun_scanned=%s\n' "$second_scanned"
    printf 'rerun_null_versions=%s\n' "$second_null"
    printf 'rerun_rewritten=%s\n' "$second_rewritten"
    printf 'rerun_already_versioned=%s\n' "$second_versioned"
  } >"$backfill_evidence_dir/counts.env"
  (cd "$backfill_evidence_dir" && sha256sum -- \
    first-run.env idempotency-rerun.env counts.env >SHA256SUMS)
  chmod 0400 "$backfill_evidence_dir"/*

  candidate_compose run --rm --no-deps migrate
  candidate_compose run --rm --no-deps postgres-permissions
  candidate_compose run --rm --no-deps migrate \
    /usr/local/bin/emfont-migrate -command status

  applied_migrations="$(candidate_compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --tuples-only --no-align \
      --command="
        WITH latest AS (
          SELECT DISTINCT ON (version_id) version_id, is_applied
          FROM goose_db_version
          WHERE version_id > 0
          ORDER BY version_id, id DESC
        )
        SELECT string_agg(
          version_id::text || ':' || is_applied::text,
          ',' ORDER BY version_id
        )
        FROM latest
      "
  ')"
  [[ "$applied_migrations" == \
    "1:true,2:true,3:true,4:true,5:true,6:true,7:true,8:true,9:true,10:true" ]]
)
```

Copy the checksummed backfill evidence directory to the immutable release
evidence system and record its evidence ID before proceeding. On a successful
first run, `scanned == null_versions + already_versioned` and every null
version was rewritten. With writers still stopped, the second run must scan the
same count, report `null_versions=0` and `rewritten=0`, and report every scanned
object as already versioned. An empty bucket validly reports zero for all four
counters on both runs. The successful helper exit also proves each rewrite's
old/new byte SHA-256, metadata, tag, and version-set checks. The archived
`.env` files contain only exit state and allowlisted key-free counters; never
add the raw container log because it can contain the application access-key
identifier.

If the block fails, do not start any writer and do not retry blindly. A partial
object backfill is safe to inspect and rerun only after the cause is understood,
because completed objects now have real current versions and remain no-ops, but
the failed run is not deployment authority. Preserve only its exit status,
allowlisted counter evidence, and PostgreSQL lock evidence; do not export or
archive raw initializer output. Determine whether any database transaction was
rolled back, compare Goose status to the backup, and choose a reviewed forward
fix or recovery. Never run `down` as an automatic failure handler. Do not bypass
the helper or weaken the controller: it must continue rejecting a source whose
current object version is empty or `null`.

`ArtifactProtocolVersion v4` intentionally changes cache identity. Existing
v1-v3 artifacts do not warm the v4 cache, so the rollout is a cold-cache event.
Start the candidate on loopback, pre-warm the manifest below, and then follow
the canary stages. Do not reopen all traffic immediately after migration.

## Cache warm-up manifest

Maintain a reviewed NDJSON manifest of hot font IDs, weights, modes, and
character sets. Build it from privacy-safe production aggregates and include
both dynamic (`min: true`) and actual static/fallback (`min: false`) workloads.
Keep it with release evidence. Each line is one complete request:

```json
{"font":"noto-sans-tc","words":"\u4f60\u597d\u4e16\u754c","min":true,"weight":"400","format":"woff2"}
{"font":"noto-sans-tc","words":"\u5b57\u9ad4\u6e2c\u8a66","min":false,"weight":"700","format":"woff2"}
```

Replay it through the loopback candidate with concurrency no greater than the
configured build concurrency. This sequential form is deliberately bounded and
retries transient 429/503 responses:

```bash
(
  set -Eeuo pipefail
  trap '
    rc=$?
    if ((rc != 0)); then
      printf "cache warm-up failed; block promotion\n" >&2
    fi
    exit "$rc"
  ' EXIT

  manifest=/etc/emfont/prewarm.ndjson
  base_url=${PREWARM_BASE_URL:-http://127.0.0.1:18080}
  [[ -s "$manifest" ]]
  jq -e -s '
    length > 0 and all(.[];
      type == "object" and
      (.font | type == "string" and test("^[A-Za-z0-9._-]{1,128}$")) and
      (.words | type == "string" and length > 0) and
      (.min | type == "boolean") and
      (.format == "woff2")
    )
  ' "$manifest" >/dev/null

  while IFS= read -r row; do
    font="$(jq -er '.font | select(test("^[A-Za-z0-9._-]{1,128}$"))' <<<"$row")"
    body="$(jq -ce '
      select(.words | type == "string" and length > 0) |
      {words, min, weight, format}
    ' <<<"$row")"
    curl --fail-with-body --silent --show-error \
      --retry 8 --retry-all-errors --retry-delay 2 --max-time 120 \
      --header 'Content-Type: application/json' \
      --data-binary "$body" \
      "$base_url/api/v1/g/$font" |
      jq -e '.status == "success" and (.location | length > 0)' >/dev/null
  done < <(jq -c . "$manifest")
)
```

Replay the manifest a second time and require cache hits without new builds.
Also download representative returned URLs through the real CDN/public gateway
and verify their content hashes. Warm-up is incomplete until both the API and
public object-delivery path pass.

## Routine deployment

This procedure applies only after the first migration-9 rollout. Future schema
changes must use expand/contract migrations and be reviewed for online safety.
Add compatible schema first, deploy readers and writers, backfill separately,
and remove old schema only after the previous release can no longer run.
Serialize migration jobs; never launch more than one.

1. Record the current verified release-manifest run ID, manifest checksum,
   backend repository/SHA-256, and version.
2. Complete and verify a coordinated backup before any schema migration.
3. Acquire the candidate only through the Verified release input procedure.
   Verify its provenance, exact-digest reports, signed full-report risk review,
   and CI. Review migration SQL for lock duration and backward compatibility.
4. Keep `/etc/emfont/current-release.env` on the stable manifest. Set
   `CANDIDATE_RELEASE_ENV_FILE` to the candidate's generated
   `compose-release.env`; never derive it from a candidate or version tag.
5. Repeat the complete Validate configuration procedure with
   `EMFONT_RELEASE_ENV_FILE` set to that candidate file. Prove its rendered
   image set equals the candidate manifest, then pull the images.
6. Run `migrate` and `postgres-permissions` as explicit one-shot preparation
   jobs with the candidate manifest file. Run `minio-init` only through
   `run_release_one_shot` with its configured backfill timeout and sanitized
   evidence.
7. Only after every one-shot exits zero, launch a loopback canary, warm it, and
   progress through the canary gates.
8. Before promotion, quiesce every writer. Switch the stable link to the whole
   candidate manifest, force-recreate PostgreSQL and MinIO, recreate all three
   canonical one-shot services, then recreate the controller. Promotion is not
   complete until every canonical container passes the Docker inspect identity
   gate against all four manifest references.

```bash
(
  set -Eeuo pipefail
  trap '
    rc=$?
    if ((rc != 0)); then
      printf "release preparation failed; do not start the candidate\n" >&2
    fi
    exit "$rc"
  ' EXIT

  candidate_release_env="$(readlink -f \
    "${CANDIDATE_RELEASE_ENV_FILE:?Set the verified candidate release env}")"
  candidate_release_dir="$(dirname "$candidate_release_env")"
  [[ "$(stat -c '%u:%g:%a' "$candidate_release_env")" == "0:0:400" ]]
  (cd "$candidate_release_dir" && sha256sum --check --strict \
    release-manifest/SHA256SUMS)
  (cd "$candidate_release_dir/deployment-verification" && \
    sha256sum --check --strict SHA256SUMS)
  bash "$candidate_release_dir/release-manifest/verify-compose-release.sh" \
    verify "$candidate_release_dir/release-manifest" \
    "$candidate_release_dir/release-manifest/images.env" >/dev/null

  candidate_compose() {
    clean_compose_environment docker compose \
      --env-file /etc/emfont/backend.env \
      --env-file "$candidate_release_env" \
      -f "$candidate_release_dir/release-manifest/docker-compose.backend.yml" "$@"
  }

  candidate_compose config --quiet
  cmp --silent \
    <(candidate_compose config --images | LC_ALL=C sort -u) \
    <(cut -d= -f2- \
      "$candidate_release_dir/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  [[ "$(candidate_compose config --format json | \
      jq -er '.services.controller.environment.EMFONT_VERSION')" == \
    "$(sed -n 's/^version=//p' \
      "$candidate_release_dir/release-manifest/release.env")" ]]
  candidate_compose pull \
    postgres minio migrate postgres-permissions minio-init controller
  candidate_compose run --rm --no-deps migrate
  candidate_compose run --rm --no-deps postgres-permissions
  run_release_one_shot candidate_compose minio-init \
    "${EMFONT_MINIO_INIT_TIMEOUT_SECONDS:-3600}" candidate-reconciliation
  candidate_compose run --rm --no-deps migrate \
    /usr/local/bin/emfont-migrate -command status
)
```

This block intentionally contains no controller start. If migration or any
reconciliation job fails, do not create or restart a candidate. Keep the old
release only when the migration review proves its schema remains compatible;
otherwise remove it from traffic and enter the incident procedure.

## Canary

A canary shares the production database and bucket. Its code and migrations
must therefore remain compatible with the current controller. Include canary
connections in PostgreSQL pool sizing.

After backup and any backward-compatible migration, start the candidate on a
separate loopback port without changing the stable service. Prove the exact
applied migration set first; readiness is not a substitute because it checks
the schema shape rather than the complete Goose history:

```bash
candidate_release_env="$(readlink -f \
  "${CANDIDATE_RELEASE_ENV_FILE:?Set the verified candidate release env}")"
candidate_release_dir="$(dirname "$candidate_release_env")"
(cd "$candidate_release_dir" && sha256sum --check --strict \
  release-manifest/SHA256SUMS)
(cd "$candidate_release_dir/deployment-verification" && \
  sha256sum --check --strict SHA256SUMS)

applied_versions="$(compose exec -T postgres sh -ec '
  export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
  psql --host=127.0.0.1 --username="$POSTGRES_USER" \
    --dbname="$POSTGRES_DB" --tuples-only --no-align \
    --command="
      WITH latest AS (
        SELECT DISTINCT ON (version_id) version_id, is_applied
        FROM goose_db_version
        WHERE version_id > 0
        ORDER BY version_id, id DESC
      )
      SELECT string_agg(
        version_id::text || ':' || is_applied::text,
        ',' ORDER BY version_id
      )
      FROM latest
    "
')"
[[ "$applied_versions" == \
  "1:true,2:true,3:true,4:true,5:true,6:true,7:true,8:true,9:true,10:true" ]]

clean_compose_environment docker compose \
    --env-file /etc/emfont/backend.env \
    --env-file "$candidate_release_env" \
    -f "$candidate_release_dir/release-manifest/docker-compose.backend.yml" \
    run --detach --no-deps \
    --name emfont-controller-canary \
    --publish 127.0.0.1:18080:8080 \
    controller

canary_id="$(docker container inspect --format '{{.Id}}' \
  emfont-controller-canary)"
canary_ref="$(release_manifest_ref "$candidate_release_dir" backend)"
[[ "$(docker container inspect --format '{{.Config.Image}}' \
  "$canary_id")" == "$canary_ref" ]]
canary_image_id="$(docker container inspect --format '{{.Image}}' \
  "$canary_id")"
[[ "$canary_image_id" == \
  "$(docker image inspect --format '{{.Id}}' "$canary_ref")" ]]
docker image inspect --format '{{json .RepoDigests}}' "$canary_image_id" |
  jq -e --arg expected "$canary_ref" \
    'type == "array" and index($expected) != null' >/dev/null
docker container inspect --format '{{json .HostConfig.PortBindings}}' \
  "$canary_id" | jq -e '
    (."8080/tcp" // []) as $bindings |
    ($bindings | length) == 1 and
    $bindings[0].HostIp == "127.0.0.1" and
    $bindings[0].HostPort == "18080"
  ' >/dev/null

wait_for_readiness() {
  local url=$1
  local attempt
  for ((attempt = 1; attempt <= 30; attempt++)); do
    if curl --fail --silent --show-error \
      --connect-timeout 1 --max-time 2 "$url" >/dev/null; then
      return 0
    fi
    if ((attempt < 30)); then
      sleep 2
    fi
  done
  printf 'readiness did not pass after 30 bounded attempts: %s\n' \
    "$url" >&2
  return 1
}

if ! wait_for_readiness \
  http://127.0.0.1:18080/api/v1/readyz; then
  docker logs --tail 200 emfont-controller-canary >&2
  exit 1
fi
```

Pre-warm v4 before sending user traffic. Then use sticky stages of 1%, 5%, 25%,
50%, and 100%. Hold each stage for at least one representative traffic window
and longer than the alert evaluation window. Freeze or reduce traffic on any
gate failure.

Record the minimum cache-hit threshold before deployment. For the manifest
replay, the default promotion gate is at least 0.90 for 30 minutes with no new
builds on the second replay; use a higher threshold when the prior production
baseline is higher. For live traffic, compare like-for-like workload windows.
Useful PromQL expressions are:

```promql
sum(rate(emfont_font_cache_lookups_total{result="hit"}[10m]))
/
sum(rate(emfont_font_cache_lookups_total{result=~"hit|miss"}[10m]))

sum(rate(emfont_http_requests_total{status=~"429|503"}[5m]))

max(emfont_font_builds_queued)

sum(rate(emfont_font_build_admissions_total{result="rejected"}[5m]))
```

Do not promote while the hit ratio is below the recorded gate, the queue stays
above zero after warm-up, admissions are rejected, 429/503 exceeds the existing
error budget, readiness flaps, or build latency/failures, PostgreSQL pressure,
and object-store errors regress. Increasing concurrency during the rollout is
not a substitute for meeting the gate.

After the 100% candidate stage passes, remove write-capable traffic and stop all
external writers. Promotion restarts the bundled data services even when a
particular digest did not change; this is what makes the stable manifest an
honest description of every canonical container. Set the confirmation only
after external writers are stopped and traffic is detached from both Compose
controllers. The script inspects, then stops, those two remaining processes
before stateful recreation:

```bash
export EMFONT_EXTERNAL_WRITERS_STOPPED=confirmed

(
  set -Eeuo pipefail
  candidate_release_env="$(readlink -f \
    "${CANDIDATE_RELEASE_ENV_FILE:?Set the verified candidate release env}")"
  candidate_release_dir="$(dirname "$candidate_release_env")"
  previous_release_env="$(readlink -f /etc/emfont/current-release.env)"
  previous_release_dir="$(dirname "$previous_release_env")"
  link_switched=false
  stateful_recreate_started=false

  trap '
    rc=$?
    if ((rc != 0)); then
      set +e
      compose stop controller >/dev/null 2>&1
      docker rm --force emfont-controller-canary >/dev/null 2>&1
      compose logs --no-log-prefix \
        postgres minio migrate postgres-permissions controller >&2
      if [[ "$link_switched" != true ]]; then
        printf "promotion failed before link switch; prior link unchanged; keep writers stopped\n" >&2
      elif [[ "$stateful_recreate_started" == false ]]; then
        recovery_link="/etc/emfont/.current-release.env.recovery.$$"
        sudo ln -s "$previous_release_env" "$recovery_link"
        sudo mv -Tf "$recovery_link" /etc/emfont/current-release.env
        release_env="$previous_release_env"
        release_dir="$previous_release_dir"
        printf "promotion failed before stateful recreation; prior link restored; verify it before restarting writers\n" >&2
      else
        printf "promotion failed after stateful recreation began; candidate link retained; keep writers stopped and use forward repair or verified recovery\n" >&2
      fi
    fi
    exit "$rc"
  ' EXIT

  [[ "${EMFONT_EXTERNAL_WRITERS_STOPPED:-}" == confirmed ]]
  [[ "$candidate_release_env" != "$previous_release_env" ]]
  [[ "$(stat -c '%u:%g:%a' "$candidate_release_env")" == "0:0:400" ]]
  (cd "$candidate_release_dir" && sha256sum --check --strict \
    release-manifest/SHA256SUMS)
  (cd "$candidate_release_dir/deployment-verification" && \
    sha256sum --check --strict SHA256SUMS)
  verify_release_containers compose "$previous_release_dir"

  candidate_compose() {
    clean_compose_environment docker compose \
      --env-file /etc/emfont/backend.env \
      --env-file "$candidate_release_env" \
      -f "$candidate_release_dir/release-manifest/docker-compose.backend.yml" "$@"
  }
  candidate_compose config --quiet
  cmp --silent \
    <(candidate_compose config --images | LC_ALL=C sort -u) \
    <(cut -d= -f2- \
      "$candidate_release_dir/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  [[ "$(candidate_compose config --format json | \
      jq -er '.services.controller.environment.EMFONT_VERSION')" == \
    "$(sed -n 's/^version=//p' \
      "$candidate_release_dir/release-manifest/release.env")" ]]
  candidate_compose pull \
    postgres minio migrate postgres-permissions minio-init controller

  compose stop controller
  if systemctl is-active --quiet emfont-fontcleanup.timer; then
    sudo systemctl stop emfont-fontcleanup.timer
  fi
  if systemctl is-active --quiet emfont-fontcleanup.service; then
    sudo systemctl stop emfont-fontcleanup.service
  fi
  running_cleanup="$(compose --profile maintenance ps \
    --status running --quiet fontcleanup)"
  [[ -z "$running_cleanup" ]]
  [[ -z "$(compose ps --status running --quiet controller)" ]]
  docker rm --force emfont-controller-canary

  printf 'previous=%s\ncandidate=%s\n' \
    "$previous_release_env" "$candidate_release_env"
  temporary_link="/etc/emfont/.current-release.env.$$"
  sudo ln -s "$candidate_release_env" "$temporary_link"
  sudo mv -Tf "$temporary_link" /etc/emfont/current-release.env
  link_switched=true
  release_env="$(readlink -f /etc/emfont/current-release.env)"
  release_dir="$(dirname "$release_env")"
  [[ "$release_env" == "$candidate_release_env" ]]
  cmp --silent \
    <(compose config --images | LC_ALL=C sort -u) \
    <(cut -d= -f2- "$release_dir/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  [[ "$(compose config --format json | \
      jq -er '.services.controller.environment.EMFONT_VERSION')" == \
    "$(sed -n 's/^version=//p' \
      "$release_dir/release-manifest/release.env")" ]]

  stateful_recreate_started=true
  reconcile_release_state compose
  compose run --rm --no-deps migrate \
    /usr/local/bin/emfont-migrate -command status
  compose up --detach --no-deps --force-recreate controller

  wait_for_readiness() {
    local url=$1
    local attempt
    for ((attempt = 1; attempt <= 60; attempt++)); do
      if curl --fail --silent --show-error \
        --connect-timeout 1 --max-time 2 "$url" >/dev/null; then
        return 0
      fi
      if ((attempt < 60)); then
        sleep 2
      fi
    done
    return 1
  }
  wait_for_readiness http://127.0.0.1:8080/api/v1/readyz
  verify_release_containers compose "$release_dir"
  curl --fail --show-error http://127.0.0.1:8080/api/v1/livez
)
```

If the trap restores the prior link, the prior controller is still stopped:
rerun configuration and `verify_release_containers` before an explicitly
approved restart. Once stateful recreation starts, never automatically switch
the link back or start an old PostgreSQL/MinIO image on the volumes. Correct a
transient failure by completing the candidate reconciliation; for an image or
data incompatibility, keep writers stopped and use the Rollback compatibility
gate or restore the coordinated pre-deploy backup onto fresh volumes.

## Rollback

Rollback selects an entire retained verified release manifest, never only an
older backend digest. A mixed deployment in which the stable manifest names old
PostgreSQL/MinIO references while those containers still run newer images is
forbidden: it cannot pass backup evidence or the canonical identity gate.

Before rollback, take and verify a coordinated backup of the current state,
stop every external writer, remove controller traffic, and obtain explicit
review that the prior backend accepts the current forward schema and that both
prior stateful images may open the current volumes. The script inspects and then
stops the remaining Compose controller. Test any PostgreSQL or MinIO image
downgrade against a restored copy first. If stateful compatibility is unknown
or unsupported, do not use this procedure; publish a forward release with the
currently compatible infrastructure digests, or restore the coordinated
pre-deploy recovery point onto fresh volumes under its matching manifest.
Repeat the complete Validate configuration procedure with
`EMFONT_RELEASE_ENV_FILE` set to `ROLLBACK_RELEASE_ENV_FILE` before running the
block below.

```bash
export EMFONT_EXTERNAL_WRITERS_STOPPED=confirmed
export EMFONT_ROLLBACK_BACKUP_VERIFIED=confirmed
export EMFONT_ROLLBACK_SCHEMA_COMPATIBILITY_APPROVED=confirmed
export EMFONT_ROLLBACK_STATEFUL_COMPATIBILITY_APPROVED=confirmed

(
  set -Eeuo pipefail
  rollback_release_env="$(readlink -f \
    "${ROLLBACK_RELEASE_ENV_FILE:?Set the previous verified release env}")"
  rollback_release_dir="$(dirname "$rollback_release_env")"
  current_release_env="$(readlink -f /etc/emfont/current-release.env)"
  current_release_dir="$(dirname "$current_release_env")"
  link_switched=false
  stateful_recreate_started=false

  trap '
    rc=$?
    if ((rc != 0)); then
      set +e
      compose stop controller >/dev/null 2>&1
      compose logs --no-log-prefix \
        postgres minio migrate postgres-permissions controller >&2
      if [[ "$link_switched" != true ]]; then
        printf "rollback failed before link switch; current link unchanged; keep writers stopped\n" >&2
      elif [[ "$stateful_recreate_started" == false ]]; then
        recovery_link="/etc/emfont/.current-release.env.recovery.$$"
        sudo ln -s "$current_release_env" "$recovery_link"
        sudo mv -Tf "$recovery_link" /etc/emfont/current-release.env
        release_env="$current_release_env"
        release_dir="$current_release_dir"
        printf "rollback failed before stateful recreation; current link restored; verify it before restarting writers\n" >&2
      else
        printf "rollback failed after stateful recreation began; rollback link retained; keep writers stopped and use forward repair or verified recovery\n" >&2
      fi
    fi
    exit "$rc"
  ' EXIT

  [[ "${EMFONT_EXTERNAL_WRITERS_STOPPED:-}" == confirmed ]]
  [[ "${EMFONT_ROLLBACK_BACKUP_VERIFIED:-}" == confirmed ]]
  [[ "${EMFONT_ROLLBACK_SCHEMA_COMPATIBILITY_APPROVED:-}" == confirmed ]]
  [[ "${EMFONT_ROLLBACK_STATEFUL_COMPATIBILITY_APPROVED:-}" == confirmed ]]
  [[ "$rollback_release_env" != "$current_release_env" ]]
  [[ "$(stat -c '%u:%g:%a' "$rollback_release_env")" == "0:0:400" ]]
  (cd "$rollback_release_dir" && sha256sum --check --strict \
    release-manifest/SHA256SUMS)
  (cd "$rollback_release_dir/deployment-verification" && \
    sha256sum --check --strict SHA256SUMS)
  verify_release_containers compose "$current_release_dir"

  rollback_compose() {
    clean_compose_environment docker compose \
      --env-file /etc/emfont/backend.env \
      --env-file "$rollback_release_env" \
      -f "$rollback_release_dir/release-manifest/docker-compose.backend.yml" "$@"
  }
  rollback_compose config --quiet
  cmp --silent \
    <(rollback_compose config --images | LC_ALL=C sort -u) \
    <(cut -d= -f2- \
      "$rollback_release_dir/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  [[ "$(rollback_compose config --format json | \
      jq -er '.services.controller.environment.EMFONT_VERSION')" == \
    "$(sed -n 's/^version=//p' \
      "$rollback_release_dir/release-manifest/release.env")" ]]
  rollback_compose pull \
    postgres minio migrate postgres-permissions minio-init controller

  compose stop controller
  docker rm --force emfont-controller-canary >/dev/null 2>&1 || true
  if systemctl is-active --quiet emfont-fontcleanup.timer; then
    sudo systemctl stop emfont-fontcleanup.timer
  fi
  if systemctl is-active --quiet emfont-fontcleanup.service; then
    sudo systemctl stop emfont-fontcleanup.service
  fi
  [[ -z "$(compose --profile maintenance ps \
    --status running --quiet fontcleanup)" ]]
  [[ -z "$(compose ps --status running --quiet controller)" ]]

  printf 'current=%s\nrollback=%s\n' \
    "$current_release_env" "$rollback_release_env"
  temporary_link="/etc/emfont/.current-release.env.$$"
  sudo ln -s "$rollback_release_env" "$temporary_link"
  sudo mv -Tf "$temporary_link" /etc/emfont/current-release.env
  link_switched=true
  release_env="$(readlink -f /etc/emfont/current-release.env)"
  release_dir="$(dirname "$release_env")"
  [[ "$release_env" == "$rollback_release_env" ]]
  cmp --silent \
    <(compose config --images | LC_ALL=C sort -u) \
    <(cut -d= -f2- "$release_dir/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  [[ "$(compose config --format json | \
      jq -er '.services.controller.environment.EMFONT_VERSION')" == \
    "$(sed -n 's/^version=//p' \
      "$release_dir/release-manifest/release.env")" ]]

  stateful_recreate_started=true
  reconcile_release_state compose
  compose run --rm --no-deps migrate \
    /usr/local/bin/emfont-migrate -command status
  compose up --detach --no-deps --force-recreate controller
  timeout 120 bash -c \
    'until curl -fsS http://127.0.0.1:8080/api/v1/readyz >/dev/null; do sleep 2; done'
  verify_release_containers compose "$release_dir"
  curl --fail --show-error http://127.0.0.1:8080/api/v1/livez
)
```

The canonical `migrate` service still executes only its configured `up` command;
it never runs `down`. Rollback approval must prove that this is a no-op for the
current forward schema and that the prior controller remains compatible with
it. Down migrations 2 through 6, 8, 9, and 10 discard persisted data or schema
state; migration 7's down is a non-reverting no-op. The exact scope through the
actual latest migration 10 is:

- Version 10 drops the bounded terminal-failure cache and every cached
  unsupported-codepoint result. It does not recreate the legacy terminal rows
  removed from `font_artifacts` during the up migration.

- Version 9 drops the transactional quota ledger, generated `quota_bytes`
  column, all accounting/locking triggers, and their security-definer
  functions. This removes atomic count/byte admission and re-enables an
  unaccounted `TRUNCATE`; it is not a production rollback mechanism.

- Version 8 drops `font_artifacts.reservation_bytes`, losing every persisted
  per-artifact reservation, and removes the positive-reservation and
  size-within-reservation constraints used by admission accounting.
- Version 7 executes only `SELECT TRUE` on down. It leaves all up changes in
  place and cannot recover the distinction between legacy NULL values and the
  empty arrays/default `ttf` used to replace them. It also does not restore the
  pre-conversion JSON representation, timestamp-without-time-zone type, old
  defaults/nullability/constraints, or earlier sequence positions. JSON to
  JSONB normalization and interpreting legacy timestamps as UTC are therefore
  intentionally forward-only.
- Version 6 drops terminal-failure `failure_code`/`retryable` classification
  and its constraints.
- Version 5 drops stored object version IDs and per-job fence values, then
  removes the fence sequence. It preserves only the sequence high-water value
  in `system_metadata` to prevent token reuse on a later re-upgrade; that does
  not preserve either dropped column.
- Version 4 drops every artifact retirement timestamp, including timestamps
  backfilled for already-stale artifacts.
- Version 3 drops retry scheduling, artifact generation, and protocol-version
  state, and irreversibly clamps BIGINT attempt counts above 2,147,483,647 when
  converting them back to the version-2 INT type.
- Version 2 drops `font_build_jobs`, `font_artifacts`, and `font_sources` and
  their data. Its down SQL is teardown, not a complete reversal of all schema
  objects created by its up SQL.
- Version 1 drops `system_metadata`, including any persisted fence high-water
  record left by a version-5 down rehearsal.

No down migration through version 10 may run on a production recovery set.
Versions 3 through 10 may be exercised only on an isolated restored copy as a
separately approved compatibility rehearsal, with a verified coordinated
backup and all target controllers, canaries, cleanup jobs, importers, and
legacy writers stopped. Versions 1 and 2 are destructive teardown for
disposable targets. If an old application cannot run against the migrated
schema, prefer a forward fix. For a destructive incompatibility, keep writers
stopped and restore the coordinated pre-deploy backup onto fresh volumes.

Never downgrade PostgreSQL data files across major versions or assume a MinIO
downgrade is supported.

## Backup

PostgreSQL rows reference MinIO objects, so back them up as one recovery point.
Stopping only the Compose controller is insufficient: `/css` GET requests can
build artifacts, and the legacy Node/admin service, importers, canaries, and
cleanup command can all mutate the same recovery set. Remove write-capable
traffic and stop every item in the writer inventory before the first database
dump or object export. Keep all writers stopped until both stores are captured.

The example below creates a logical recovery point. It is fail-closed and never
restarts a writer, even on success. Set the confirmation only after external
writers and schedulers are stopped and any in-flight cleanup has exited.

```bash
export EMFONT_EXTERNAL_WRITERS_STOPPED=confirmed

(
  set -Eeuo pipefail
  mc_extract_container=
  mc_config_dir=
  trap '
    rc=$?
    if [[ -n "$mc_extract_container" ]]; then
      docker rm -f "$mc_extract_container" >/dev/null 2>&1 || true
    fi
    if [[ -n "$mc_config_dir" ]]; then
      rm -rf -- "$mc_config_dir"
    fi
    if ((rc != 0)); then
      printf "backup failed; keep writers stopped; preserve output\n" >&2
    fi
    exit "$rc"
  ' EXIT

  [[ "${EMFONT_EXTERNAL_WRITERS_STOPPED:-}" == "confirmed" ]]
  backup_started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  backup_id="$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 8)"
  backup_root="/srv/backups/emfont/$backup_id"
  operator_uid=$(id -u)
  operator_gid=$(id -g)
  [[ ! -e "$backup_root" ]]
  sudo install -d -m 0700 -o "$operator_uid" -g "$operator_gid" \
    "$backup_root"

  active_release_env="$(readlink -f /etc/emfont/current-release.env)"
  active_release_dir="$(dirname "$active_release_env")"
  sudo sh -ec '
    cd "$1"
    sha256sum --check --strict release-manifest/SHA256SUMS
  ' backup-release-check "$active_release_dir"

  # Capture actual identities while all three long-running services are still
  # healthy. The helper also checks the retained canonical one-shot containers.
  verify_release_containers compose "$active_release_dir" \
    "$backup_root/running-images.tsv"

  compose stop controller
  if systemctl is-active --quiet emfont-fontcleanup.timer; then
    sudo systemctl stop emfont-fontcleanup.timer
  fi
  if systemctl is-active --quiet emfont-fontcleanup.service; then
    sudo systemctl stop emfont-fontcleanup.service
  fi
  [[ -z "$(compose ps --status running --quiet controller)" ]]
  running_cleanup="$(
    compose --profile maintenance ps \
      --status running --quiet fontcleanup
  )"
  [[ -z "$running_cleanup" ]]
  sudo install -d -m 0700 -o "$operator_uid" -g "$operator_gid" \
    "$backup_root/verified-release" \
    "$backup_root/verified-release/release-manifest" \
    "$backup_root/verified-release/deployment-verification"
  sudo install -m 0600 -o "$operator_uid" -g "$operator_gid" \
    "$active_release_env" \
    "$backup_root/verified-release/compose-release.env"
  sudo cp -a -- "$active_release_dir/release-manifest/." \
    "$backup_root/verified-release/release-manifest/"
  sudo chown -R "$operator_uid:$operator_gid" \
    "$backup_root/verified-release/release-manifest"
  sudo find "$backup_root/verified-release/release-manifest" \
    -type d -exec chmod 0700 {} +
  sudo find "$backup_root/verified-release/release-manifest" \
    -type f -exec chmod 0600 {} +
  sudo cp -a -- \
    "$active_release_dir/deployment-verification/." \
    "$backup_root/verified-release/deployment-verification/"
  sudo chown -R "$operator_uid:$operator_gid" \
    "$backup_root/verified-release/deployment-verification"
  sudo find "$backup_root/verified-release/deployment-verification" \
    -type d -exec chmod 0700 {} +
  sudo find "$backup_root/verified-release/deployment-verification" \
    -type f -exec chmod 0600 {} +
  (cd "$backup_root/verified-release" && \
    sha256sum --check --strict release-manifest/SHA256SUMS)
  (cd "$backup_root/verified-release/deployment-verification" && \
    sha256sum --check --strict SHA256SUMS)

  # The logical object producer is source-bound and retained with the backup.
  install -d -m 0700 "$backup_root/object-tools"
  source_commit="$(sed -n 's/^source_commit=//p' \
    "$active_release_dir/release-manifest/release.env")"
  [[ "$source_commit" =~ ^[0-9a-f]{40}$ ]]
  for tool in \
    backend/scripts/minio-object-manifest-lib.sh \
    backend/scripts/minio-object-export.sh \
    backend/scripts/minio-object-restore.sh
  do
    git -C /opt/emfont cat-file -e "$source_commit:$tool"
    cmp --silent \
      <(git -C /opt/emfont show "$source_commit:$tool") \
      "/opt/emfont/$tool"
    install -m 0500 "/opt/emfont/$tool" "$backup_root/object-tools/"
  done

  compose config >"$backup_root/rendered-compose.yaml"
  compose config --images | LC_ALL=C sort -u \
    >"$backup_root/image-digests.txt"
  [[ -s "$backup_root/image-digests.txt" ]]
  while IFS= read -r image; do
    [[ "$image" =~ @sha256:[0-9a-f]{64}$ ]]
  done <"$backup_root/image-digests.txt"
  cmp --silent "$backup_root/image-digests.txt" \
    <(cut -d= -f2- \
      "$backup_root/verified-release/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  [[ "$(compose config --format json | \
      jq -er '.services.controller.environment.EMFONT_VERSION')" == \
    "$(sed -n 's/^version=//p' \
      "$backup_root/verified-release/release-manifest/release.env")" ]]

  expected_running_header=$'service\tcontainer_id\texact_ref\timage_id\trepo_digests\tstate\thealth\texit_code'
  [[ "$(head -n 1 "$backup_root/running-images.tsv")" == \
    "$expected_running_header" ]]
  [[ "$(wc -l <"$backup_root/running-images.tsv")" == 7 ]]
  cmp --silent \
    <(tail -n +2 "$backup_root/running-images.tsv" | cut -f3 | \
      LC_ALL=C sort -u) \
    <(cut -d= -f2- \
      "$backup_root/verified-release/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  while IFS=$'\t' read -r service container_id exact_ref image_id \
      repo_digests state health exit_code; do
    [[ -n "$service" && "$container_id" =~ ^[0-9a-f]{64}$ ]]
    [[ "$exact_ref" =~ @sha256:[0-9a-f]{64}$ ]]
    [[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]]
    jq -e --arg expected "$exact_ref" \
      'type == "array" and index($expected) != null' \
      <<<"$repo_digests" >/dev/null
    [[ -n "$state" && -n "$health" && "$exit_code" =~ ^[0-9]+$ ]]
  done < <(tail -n +2 "$backup_root/running-images.tsv")

  bucket="$(compose config --format json | jq -er '
    .services["minio-init"].environment.EMFONT_MINIO_BUCKET |
    select(type == "string" and length > 0)
  ')"
  compose run --rm --no-deps migrate \
    /usr/local/bin/emfont-migrate -command status \
    >"$backup_root/migration-status.txt"
  migration_state="$(compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --tuples-only --no-align \
      --command="
        WITH latest AS (
          SELECT DISTINCT ON (version_id) version_id, is_applied
          FROM goose_db_version
          WHERE version_id > 0
          ORDER BY version_id, id DESC
        )
        SELECT string_agg(
          version_id::text || ':' || is_applied::text,
          ',' ORDER BY version_id
        )
        FROM latest
      "
  ')"
  [[ "$migration_state" == \
    "1:true,2:true,3:true,4:true,5:true,6:true,7:true,8:true,9:true,10:true" ]]
  printf '%s\n' "$migration_state" >"$backup_root/migration-state.txt"

  compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --tuples-only --no-align \
      --field-separator="$(printf "\\t")" \
      --command="
        SELECT
          quota.artifact_count,
          actual.artifact_count,
          quota.accounted_bytes,
          actual.accounted_bytes,
          quota.artifact_count = actual.artifact_count
            AND quota.accounted_bytes = actual.accounted_bytes
        FROM font_artifact_quota AS quota
        CROSS JOIN (
          SELECT count(*)::BIGINT AS artifact_count,
                 COALESCE(sum(quota_bytes), 0)::BIGINT AS accounted_bytes
          FROM font_artifacts
        ) AS actual
        WHERE quota.singleton
      "
  ' >"$backup_root/quota-ledger.tsv"
  awk -F '\t' '
    NR != 1 || NF != 5 || $1 !~ /^[0-9]+$/ || $2 !~ /^[0-9]+$/ ||
      $3 !~ /^[0-9]+$/ || $4 !~ /^[0-9]+$/ || $5 != "t" { exit 1 }
    END { if (NR != 1) exit 1 }
  ' "$backup_root/quota-ledger.tsv"

  compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    exec pg_dump \
      --host=127.0.0.1 \
      --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" \
      --format=custom \
      --compress=9 \
      --no-owner \
      --no-acl
  ' >"$backup_root/postgres.dump"
  [[ -s "$backup_root/postgres.dump" ]]

  # Exact row counts are the database inventory used by the restore drill.
  compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    exec psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --no-psqlrc --set=ON_ERROR_STOP=1
  ' >"$backup_root/database-counts.tsv" <<'SQL'
\pset tuples_only on
\pset format unaligned
SELECT format(
  'SELECT %L || chr(9) || count(*)::text FROM %I.%I;',
  schemaname || '.' || tablename,
  schemaname,
  tablename
)
FROM pg_catalog.pg_tables
WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
  AND schemaname !~ '^pg_toast'
ORDER BY schemaname, tablename
\gexec
SQL
  [[ -s "$backup_root/database-counts.tsv" ]]
  awk -F '\t' '
    NF != 2 || $1 == "" || $2 !~ /^[0-9]+$/ { exit 1 }
    END { if (NR == 0) exit 1 }
  ' "$backup_root/database-counts.tsv"

  mapfile -t minio_mc_refs < <(awk -F= '$1 == "minio_mc" {
    sub(/^[^=]*=/, "")
    print
  }' "$active_release_dir/release-manifest/images.env")
  ((${#minio_mc_refs[@]} == 1))
  minio_mc_ref=${minio_mc_refs[0]}
  [[ "$minio_mc_ref" =~ @sha256:[0-9a-f]{64}$ ]]
  docker image inspect "$minio_mc_ref" >/dev/null
  mc_extract_container="$(docker create --entrypoint /bin/true \
    "$minio_mc_ref")"
  docker cp "$mc_extract_container:/usr/local/bin/mc" \
    "$backup_root/object-tools/mc"
  docker rm "$mc_extract_container" >/dev/null
  mc_extract_container=
  chmod 0500 "$backup_root/object-tools/mc"

  minio_container="$(compose ps --quiet minio)"
  [[ "$minio_container" =~ ^[0-9a-f]{64}$ ]]
  minio_ip="$(docker inspect "$minio_container" | jq -er '
    .[0].NetworkSettings.Networks |
    [.[] | .IPAddress | select(type == "string" and length > 0)] |
    select(length == 1) | .[0] |
    select(test("^[0-9]+(\\.[0-9]+){3}$"))
  ')"
  mc_config_dir="$(mktemp -d /tmp/emfont-backup-mc.XXXXXX)"
  [[ "$(stat -c '%u:%a' "$mc_config_dir")" == "$operator_uid:700" ]]
  sudo /bin/sh -eu -c '
    printf "%s\n%s\n" "$(cat -- "$1")" "$(cat -- "$2")"
  ' minio-backup-credentials \
    /etc/emfont/secrets/minio-root-user \
    /etc/emfont/secrets/minio-root-password | \
    MC_CONFIG_DIR="$mc_config_dir" "$backup_root/object-tools/mc" \
      alias set backup "http://$minio_ip:9000" \
      --api S3v4 --path on >/dev/null 2>&1

  EMFONT_OBJECT_WRITERS_QUIESCED=confirmed \
  MC_BIN="$backup_root/object-tools/mc" \
  MC_CONFIG_DIR="$mc_config_dir" \
    "$backup_root/object-tools/minio-object-export.sh" \
      "backup/$bucket" "$backup_root/minio-export"
  rm -rf -- "$mc_config_dir"
  mc_config_dir=
  compose exec -T postgres pg_restore --list \
    <"$backup_root/postgres.dump" >/dev/null

  sha256_of() {
    sha256sum -- "$1" | awk '{print $1}'
  }
  object_count="$(jq -er '.object_count' \
    "$backup_root/minio-export/export-manifest.json")"
  object_bytes="$(jq -er '.total_size_bytes' \
    "$backup_root/minio-export/export-manifest.json")"
  relation_count="$(wc -l <"$backup_root/database-counts.tsv")"
  backup_completed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  jq -n \
    --arg backup_id "$backup_id" \
    --arg started_at "$backup_started_at" \
    --arg completed_at "$backup_completed_at" \
    --arg bucket "$bucket" \
    --arg operator "$(id -un)" \
    --argjson object_count "$object_count" \
    --argjson object_bytes "$object_bytes" \
    --argjson relation_count "$relation_count" \
    --arg postgres_sha256 "$(sha256_of "$backup_root/postgres.dump")" \
    --arg postgres_bytes "$(stat -c '%s' "$backup_root/postgres.dump")" \
    --arg object_export_manifest_sha256 \
      "$(sha256_of "$backup_root/minio-export/export-manifest.json")" \
    --arg object_manifest_sha256 \
      "$(sha256_of "$backup_root/minio-export/object-manifest.ndjson")" \
    --arg object_mc_sha256 \
      "$(sha256_of "$backup_root/object-tools/mc")" \
    --arg object_export_helper_sha256 \
      "$(sha256_of "$backup_root/object-tools/minio-object-export.sh")" \
    --arg object_restore_helper_sha256 \
      "$(sha256_of "$backup_root/object-tools/minio-object-restore.sh")" \
    --arg object_library_sha256 \
      "$(sha256_of "$backup_root/object-tools/minio-object-manifest-lib.sh")" \
    --arg database_counts_sha256 \
      "$(sha256_of "$backup_root/database-counts.tsv")" \
    --arg image_digests_sha256 \
      "$(sha256_of "$backup_root/image-digests.txt")" \
    --arg running_images_sha256 \
      "$(sha256_of "$backup_root/running-images.tsv")" \
    --arg release_manifest_sha256 \
      "$(sha256_of \
        "$backup_root/verified-release/release-manifest/SHA256SUMS")" \
    --arg release_verification_sha256 \
      "$(sha256_of \
        "$backup_root/verified-release/deployment-verification/SHA256SUMS")" \
    '{
      format: "emfont-logical-backup/v2",
      backup_id: $backup_id,
      backup_type: "logical-current-object-versions-with-metadata",
      consistency: "all-writers-quiesced",
      started_at: $started_at,
      completed_at: $completed_at,
      operator: $operator,
      latest_schema_migration: 10,
      migration_state: "1:true,2:true,3:true,4:true,5:true,6:true,7:true,8:true,9:true,10:true",
      bucket: $bucket,
      postgres: {
        file: "postgres.dump",
        size_bytes: ($postgres_bytes | tonumber),
        sha256: $postgres_sha256,
        relation_count: $relation_count,
        counts_file: "database-counts.tsv",
        counts_sha256: $database_counts_sha256
      },
      objects: {
        directory: "minio-export",
        export_manifest: "minio-export/export-manifest.json",
        export_manifest_sha256: $object_export_manifest_sha256,
        object_manifest: "minio-export/object-manifest.ndjson",
        object_manifest_sha256: $object_manifest_sha256,
        object_count: $object_count,
        total_size_bytes: $object_bytes,
        version_scope: "current-only-pinned-version-ids",
        metadata_contract: "emfont-minio-metadata/v1",
        checksum_contract: "server-and-payload-sha256"
      },
      object_tooling: {
        directory: "object-tools",
        mc_sha256: $object_mc_sha256,
        export_helper_sha256: $object_export_helper_sha256,
        restore_helper_sha256: $object_restore_helper_sha256,
        manifest_library_sha256: $object_library_sha256
      },
      images: {
        file: "image-digests.txt",
        sha256: $image_digests_sha256,
        running_containers_file: "running-images.tsv",
        running_containers_sha256: $running_images_sha256
      },
      verified_release: {
        directory: "verified-release",
        manifest_checksum_sha256: $release_manifest_sha256,
        deployment_verification_checksum_sha256: $release_verification_sha256
      }
    }' >"$backup_root/backup-manifest.json"

  (
    cd "$backup_root"
    sha256sum -- \
      backup-manifest.json \
      database-counts.tsv \
      image-digests.txt \
      running-images.tsv \
      migration-state.txt \
      migration-status.txt \
      postgres.dump \
      quota-ledger.tsv \
      rendered-compose.yaml >SHA256SUMS
    [[ -z "$(find minio-export object-tools verified-release \
      ! -type d ! -type f -print -quit)" ]]
    find minio-export object-tools verified-release \
      -type f -print0 | LC_ALL=C sort -z | \
      xargs -0 -r sha256sum -- >>SHA256SUMS
    sha256sum --check --strict SHA256SUMS
  )

  printf 'Logical backup verified at %s; writers remain stopped.\n' \
    "$backup_root"
)
```

The backup is not complete until `SHA256SUMS` and every file it names are
encrypted, copied to an independent off-host account or failure domain,
retrieved into a new empty directory, decrypted, and verified there with
`sha256sum --check --strict SHA256SUMS`. Verify
`backup-manifest.json`, the exact table counts, object count/bytes, every
payload checksum, metadata/tag manifest, and source version identity during the
isolated drill. Record the exact off-host URI,
retention/immutability, verified release-manifest run ID and checksum, backend
and infrastructure digests, full canonical container IDs, running image-config
IDs and `RepoDigests`, UTC start/end, operator, writer-stop evidence, upload
verification, and retrieval verification. `running-images.tsv` is valid only
because its exact references were compared with the retained four-entry release
manifest before the controller stopped. Only then may the deployment procedure
or a separately approved writer restart continue. Never put a restart command
in the backup script or its EXIT trap.

Filesystem copies of a live named volume are not valid PostgreSQL backups. The
reviewed export helper pins each current source version, downloads that exact
version, preserves its content type, supported standard headers, complete user
metadata, tags, storage class, ETag, version ID, and server SHA-256 checksum,
then rechecks the current version and tag set before publishing its manifest.
It rejects missing SHA-256 checksums, unsupported metadata, concurrent changes,
or producer/parser schema drift. This is still a current-version logical
backup, not MinIO's complete version history, and every restored object receives
a new version ID. Because migration 5 stores generated-object version IDs in
PostgreSQL, a logical restore must invalidate `font_artifacts` and
`font_build_jobs` as shown below. Those tables are derived cache state; source
metadata and original objects remain authoritative.

An alternative is a provider-supported, coordinated PostgreSQL/object-storage
snapshot that preserves the complete bucket version namespace and every
version ID. Use that path only after a restore drill proves that PostgreSQL
references resolve to the exact restored object versions. Document the
consistency protocol and do not mix its assumptions with the logical procedure.
For lower RPO, use PostgreSQL WAL archiving and provider-native versioned object
replication under a tested consistency protocol; this Compose file does not
configure PITR.

## Recovery

Rehearse recovery from a retrieved off-host copy at least quarterly and after
any storage, schema, image, or backup-tool change. The drill must run in a
dedicated host/VM or provider account with separate credentials, fresh volumes,
no route to production PostgreSQL or MinIO, and no production gateway DNS.
Using another Compose project on the production host is not sufficient
isolation. Measure RTO/RPO and prove font generation and object download, not
just process startup.

1. Retrieve the sealed backup into a new directory and verify every checksum,
   manifest field, image digest, table count, and object export summary.
2. Verify the restore project, credentials, database, bucket, ports, and named
   volumes are isolated and empty. Keep every restore-target writer stopped.
3. Restore PostgreSQL and current object data while no writer is running.
4. Before changing derived state, compare all restored table counts with
   `database-counts.tsv`. Require the restore helper to verify every payload,
   checksum, metadata field, tag, and destination version, and retain its
   source-to-destination `version-map.ndjson`.
5. For a logical restore, clear only `font_build_jobs` and `font_artifacts`
   because destination object version IDs changed. Do not clear source or
   family metadata. Run migration 10 and permission reconciliation. Use row
   `DELETE`, never `TRUNCATE`, so the quota ledger remains transactional.
6. Start only an isolated loopback controller, replay the v4 warm-up twice,
   generate a new font, and download source and generated objects through an
   isolated `GET`/`HEAD` gateway. Seal logs, timings, counts, and checksums as
   the restore-drill record.

The following example restores the logical backup into a new isolated Compose
project. `/etc/emfont/restore/backend.env` contains drill-only topology and
secrets but no image fields. `EMFONT_RESTORE_RELEASE_ENV_FILE` must name an
installed copy of the backup's `verified-release/compose-release.env`; values
may not be reconstructed from tags or typed from `image-digests.txt`. The
static file must set an isolated HTTPS object-gateway base URL, never a
production hostname. The block intentionally starts no controller, scheduler,
cleanup job, gateway, or legacy writer:

```bash
export EMFONT_RESTORE_ISOLATED=confirmed
export EMFONT_RESTORE_SOURCE_OFF_HOST=confirmed
export EMFONT_RESTORE_TARGET_EMPTY=confirmed
export EMFONT_RESTORE_ROOT=/srv/restore/emfont/20260710T120000Z
export EMFONT_RESTORE_ENV_FILE=/etc/emfont/restore/backend.env
export EMFONT_RESTORE_RELEASE_ENV_FILE=/etc/emfont/restore/compose-release.env
export EMFONT_RESTORE_PROJECT=emfont-restore-20260710t120000z
export EMFONT_PRODUCTION_PROJECT=emfont
export EMFONT_HTTP_PORT=18081

(
  set -Eeuo pipefail
  trap '
    rc=$?
    if ((rc != 0)); then
      printf "restore drill failed; do not start any writer\n" >&2
    fi
    exit "$rc"
  ' EXIT

  [[ "${EMFONT_RESTORE_ISOLATED:-}" == "confirmed" ]]
  [[ "${EMFONT_RESTORE_SOURCE_OFF_HOST:-}" == "confirmed" ]]
  [[ "${EMFONT_RESTORE_TARGET_EMPTY:-}" == "confirmed" ]]
  restore_root=${EMFONT_RESTORE_ROOT:?Set the retrieved backup directory}
  restore_env=${EMFONT_RESTORE_ENV_FILE:?Set the drill-only environment file}
  restore_release_env=${EMFONT_RESTORE_RELEASE_ENV_FILE:?Set restore release env}
  restore_project=${EMFONT_RESTORE_PROJECT:?Set a unique restore project}
  production_project=${EMFONT_PRODUCTION_PROJECT:?Set production project name}
  [[ "$restore_project" =~ ^emfont-restore-[a-z0-9][a-z0-9-]*$ ]]
  [[ "$restore_project" != "$production_project" ]]
  [[ -d "$restore_root" && -f "$restore_env" \
    && -f "$restore_release_env" ]]
  cmp --silent "$restore_release_env" \
    "$restore_root/verified-release/compose-release.env"

  clean_compose_environment() {
    env \
      -u COMPOSE_PROJECT_NAME \
      -u EMFONT_VERSION \
      -u EMFONT_BACKEND_IMAGE_REPOSITORY \
      -u EMFONT_BACKEND_IMAGE_SHA256 \
      -u EMFONT_POSTGRES_IMAGE_REPOSITORY \
      -u EMFONT_POSTGRES_IMAGE_SHA256 \
      -u EMFONT_MINIO_IMAGE_REPOSITORY \
      -u EMFONT_MINIO_IMAGE_SHA256 \
      -u EMFONT_MINIO_MC_IMAGE_REPOSITORY \
      -u EMFONT_MINIO_MC_IMAGE_SHA256 \
      "$@"
  }

  restore_compose() {
    clean_compose_environment docker compose \
      --project-name "$restore_project" \
      --env-file "$restore_env" \
      --env-file "$restore_release_env" \
      -f "$restore_root/verified-release/release-manifest/docker-compose.backend.yml" "$@"
  }
  bash "$restore_root/verified-release/release-manifest/verify-compose-release.sh" \
    verify "$restore_root/verified-release/release-manifest" \
    "$restore_root/verified-release/release-manifest/images.env" >/dev/null

  # Existing resources mean this is not a fresh target; investigate, do not
  # delete them from inside the restore procedure.
  [[ -z "$(docker ps -aq \
    --filter "label=com.docker.compose.project=$restore_project")" ]]
  [[ -z "$(docker volume ls -q \
    --filter "label=com.docker.compose.project=$restore_project")" ]]
  [[ -z "$(docker network ls -q \
    --filter "label=com.docker.compose.project=$restore_project")" ]]

  drill_root="/srv/restore-drills/$restore_project"
  operator_uid=$(id -u)
  operator_gid=$(id -g)
  [[ ! -e "$drill_root" ]]
  sudo install -d -m 0700 -o "$operator_uid" -g "$operator_gid" \
    "$drill_root"
  date -u +%Y-%m-%dT%H:%M:%SZ >"$drill_root/drill-started-at.txt"

  (
    cd "$restore_root"
    sha256sum --check --strict SHA256SUMS
    jq -e '
      .format == "emfont-logical-backup/v2" and
      .backup_type == "logical-current-object-versions-with-metadata" and
      .latest_schema_migration == 10 and
      .migration_state ==
        "1:true,2:true,3:true,4:true,5:true,6:true,7:true,8:true,9:true,10:true" and
      .objects.version_scope == "current-only-pinned-version-ids" and
      .objects.metadata_contract == "emfont-minio-metadata/v1" and
      .objects.checksum_contract == "server-and-payload-sha256"
    ' backup-manifest.json >/dev/null
    jq -e '
      .format == "emfont-object-export/v1" and
      .version_scope == "current-only-pinned-version-ids" and
      .metadata_contract == "emfont-minio-metadata/v1" and
      .checksum_contract == "server-and-payload-sha256"
    ' minio-export/export-manifest.json >/dev/null
    (cd verified-release && sha256sum --check --strict \
      release-manifest/SHA256SUMS)
    (cd verified-release/deployment-verification && \
      sha256sum --check --strict SHA256SUMS)
  )

  sha256_of() {
    sha256sum -- "$1" | awk '{print $1}'
  }
  expected_object_count="$(jq -er '.object_count' \
    "$restore_root/minio-export/export-manifest.json")"
  expected_object_bytes="$(jq -er '.total_size_bytes' \
    "$restore_root/minio-export/export-manifest.json")"
  [[ "$(wc -l <"$restore_root/minio-export/object-manifest.ndjson")" \
    == "$expected_object_count" ]]
  [[ "$(wc -l <"$restore_root/database-counts.tsv")" \
    == "$(jq -er '.postgres.relation_count' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$expected_object_count" == \
    "$(jq -er '.objects.object_count' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$expected_object_bytes" == \
    "$(jq -er '.objects.total_size_bytes' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(sha256_of "$restore_root/postgres.dump")" == \
    "$(jq -er '.postgres.sha256' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(stat -c '%s' "$restore_root/postgres.dump")" == \
    "$(jq -er '.postgres.size_bytes' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(sha256_of "$restore_root/database-counts.tsv")" == \
    "$(jq -er '.postgres.counts_sha256' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(sha256_of "$restore_root/minio-export/export-manifest.json")" == \
    "$(jq -er '.objects.export_manifest_sha256' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(sha256_of "$restore_root/minio-export/object-manifest.ndjson")" == \
    "$(jq -er '.objects.object_manifest_sha256' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(sha256_of "$restore_root/object-tools/mc")" == \
    "$(jq -er '.object_tooling.mc_sha256' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(sha256_of "$restore_root/image-digests.txt")" == \
    "$(jq -er '.images.sha256' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(sha256_of "$restore_root/running-images.tsv")" == \
    "$(jq -er '.images.running_containers_sha256' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(sha256_of \
      "$restore_root/verified-release/release-manifest/SHA256SUMS")" == \
    "$(jq -er '.verified_release.manifest_checksum_sha256' \
      "$restore_root/backup-manifest.json")" ]]
  [[ "$(sha256_of \
      "$restore_root/verified-release/deployment-verification/SHA256SUMS")" == \
    "$(jq -er \
      '.verified_release.deployment_verification_checksum_sha256' \
      "$restore_root/backup-manifest.json")" ]]
  cmp --silent "$restore_root/image-digests.txt" \
    <(cut -d= -f2- \
      "$restore_root/verified-release/release-manifest/images.env" | \
      LC_ALL=C sort -u)
  cmp --silent \
    <(tail -n +2 "$restore_root/running-images.tsv" | cut -f3 | \
      LC_ALL=C sort -u) \
    <(cut -d= -f2- \
      "$restore_root/verified-release/release-manifest/images.env" | \
      LC_ALL=C sort -u)

  restore_compose config --quiet
  restore_compose config --images | LC_ALL=C sort -u \
    >"$drill_root/restored-image-digests.txt"
  cmp --silent "$restore_root/image-digests.txt" \
    "$drill_root/restored-image-digests.txt"
  [[ "$(restore_compose config --format json | \
      jq -er '.services.controller.environment.EMFONT_VERSION')" == \
    "$(sed -n 's/^version=//p' \
      "$restore_root/verified-release/release-manifest/release.env")" ]]
  restore_bucket="$(restore_compose config --format json | jq -er '
    .services["minio-init"].environment.EMFONT_MINIO_BUCKET |
    select(type == "string" and length > 0)
  ')"
  [[ "$restore_bucket" == \
    "$(jq -er '.bucket' "$restore_root/backup-manifest.json")" ]]

  restore_compose up -d --wait postgres minio
  run_release_one_shot restore_compose minio-init \
    "${EMFONT_MINIO_INIT_TIMEOUT_SECONDS:-3600}" restore-bootstrap \
    "$drill_root/minio-init.env"

  restore_compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    dropdb --host=127.0.0.1 --username="$POSTGRES_USER" \
      --maintenance-db=postgres --if-exists --force "$POSTGRES_DB"
    createdb --host=127.0.0.1 --username="$POSTGRES_USER" \
      --maintenance-db=postgres "$POSTGRES_DB"
  '

  restore_compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    exec pg_restore --host=127.0.0.1 \
      --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" \
      --exit-on-error --no-owner --no-acl
  ' <"$restore_root/postgres.dump"

  # Compare every restored table before intentionally clearing derived caches.
  restore_compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    exec psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --no-psqlrc --set=ON_ERROR_STOP=1
  ' >"$drill_root/restored-database-counts.tsv" <<'SQL'
\pset tuples_only on
\pset format unaligned
SELECT format(
  'SELECT %L || chr(9) || count(*)::text FROM %I.%I;',
  schemaname || '.' || tablename,
  schemaname,
  tablename
)
FROM pg_catalog.pg_tables
WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
  AND schemaname !~ '^pg_toast'
ORDER BY schemaname, tablename
\gexec
SQL
  cmp --silent "$restore_root/database-counts.tsv" \
    "$drill_root/restored-database-counts.tsv"

  restore_compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --tuples-only --no-align \
      --field-separator="$(printf "\\t")" \
      --command="
        SELECT
          quota.artifact_count,
          actual.artifact_count,
          quota.accounted_bytes,
          actual.accounted_bytes,
          quota.artifact_count = actual.artifact_count
            AND quota.accounted_bytes = actual.accounted_bytes
        FROM font_artifact_quota AS quota
        CROSS JOIN (
          SELECT count(*)::BIGINT AS artifact_count,
                 COALESCE(sum(quota_bytes), 0)::BIGINT AS accounted_bytes
          FROM font_artifacts
        ) AS actual
        WHERE quota.singleton
      "
  ' >"$drill_root/restored-quota-ledger.tsv"
  cmp --silent "$restore_root/quota-ledger.tsv" \
    "$drill_root/restored-quota-ledger.tsv"

  restore_minio_container="$(restore_compose ps --quiet minio)"
  [[ "$restore_minio_container" =~ ^[0-9a-f]{64}$ ]]
  restore_minio_ip="$(docker inspect "$restore_minio_container" | jq -er '
    .[0].NetworkSettings.Networks |
    [.[] | .IPAddress | select(type == "string" and length > 0)] |
    select(length == 1) | .[0] |
    select(test("^[0-9]+(\\.[0-9]+){3}$"))
  ')"
  mc_config_dir="$(mktemp -d /tmp/emfont-restore-mc.XXXXXX)"
  [[ "$(stat -c '%u:%a' "$mc_config_dir")" == "$operator_uid:700" ]]
  restore_compose run --rm --no-deps minio-init /bin/sh -ec '
    printf "%s\n%s\n" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"
  ' 2>/dev/null | \
    MC_CONFIG_DIR="$mc_config_dir" "$restore_root/object-tools/mc" \
      alias set restore "http://$restore_minio_ip:9000" \
      --api S3v4 --path on >/dev/null 2>&1
  EMFONT_RESTORE_TARGET_EMPTY=confirmed \
  MC_BIN="$restore_root/object-tools/mc" \
  MC_CONFIG_DIR="$mc_config_dir" \
    "$restore_root/object-tools/minio-object-restore.sh" \
      "$restore_root/minio-export" "restore/$restore_bucket" \
      "$drill_root/object-restore-result"
  rm -rf -- "$mc_config_dir"
  mc_config_dir=
  jq -e \
    --argjson count "$expected_object_count" \
    --argjson bytes "$expected_object_bytes" '
    .format == "emfont-object-restore/v1" and
    .object_count == $count and .total_size_bytes == $bytes and
    .verification == "payload-checksum-metadata-tags-and-current-version"
  ' "$drill_root/object-restore-result/restore-manifest.json" >/dev/null
  [[ "$(wc -l <"$drill_root/object-restore-result/version-map.ndjson")" \
    == "$expected_object_count" ]]

  restore_compose run --rm --no-deps migrate
  restore_compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --set=ON_ERROR_STOP=1 \
      --command="BEGIN; DELETE FROM font_build_jobs; DELETE FROM font_artifacts; COMMIT;"
  '
  restore_compose run --rm --no-deps postgres-permissions
  restore_compose run --rm --no-deps migrate \
    /usr/local/bin/emfont-migrate -command status \
    >"$drill_root/restored-migration-status.txt"
  migration_state="$(restore_compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --tuples-only --no-align \
      --command="
        WITH latest AS (
          SELECT DISTINCT ON (version_id) version_id, is_applied
          FROM goose_db_version
          WHERE version_id > 0
          ORDER BY version_id, id DESC
        )
        SELECT string_agg(
          version_id::text || ':' || is_applied::text,
          ',' ORDER BY version_id
        )
        FROM latest
      "
  ')"
  [[ "$migration_state" == \
    "1:true,2:true,3:true,4:true,5:true,6:true,7:true,8:true,9:true,10:true" ]]
  printf '%s\n' "$migration_state" \
    >"$drill_root/restored-migration-state.txt"
  derived_counts="$(restore_compose exec -T postgres sh -ec '
    export PGPASSWORD="$(cat "$POSTGRES_PASSWORD_FILE")"
    psql --host=127.0.0.1 --username="$POSTGRES_USER" \
      --dbname="$POSTGRES_DB" --tuples-only --no-align \
      --command="
        SELECT (SELECT count(*) FROM font_artifacts)::text || ':' ||
               (SELECT count(*) FROM font_build_jobs)::text || ':' ||
               artifact_count::text || ':' || accounted_bytes::text
        FROM font_artifact_quota
        WHERE singleton
      "
  ')"
  [[ "$derived_counts" == "0:0:0:0" ]]
)
```

For a physical, version-preserving object-store restore, omit derived-cache
invalidation only when an isolated drill has proved that every stored
`object_version_id` still resolves to the same bytes and the quota ledger
matches the artifact table. A logical restore must always run the row deletes.

After the integrity block exits zero, deploy a drill-only gateway with no
production DNS, credentials, or network route. Attach it to the restore
project's internal attachable `object-store` network and a separate isolated
TLS test network; prove both attachments and the absence of a MinIO host port
with the Object download URLs gate, substituting `restore_compose`. Then start
the controller only in the isolated project. Use the bounded readiness loop
from the Canary section against port 18081, replay the warm-up manifest twice,
perform a new generation, and verify the returned source and generated URLs
through that drill-only `GET`/`HEAD` gateway with `versionId` intact. The drill
record must include the `backup-manifest.json` and `SHA256SUMS` hashes,
project/host identity, exact image digests and
provenance IDs, UTC start/end, RPO/RTO, restored table counts, pre-invalidation
comparison, quota-ledger comparison, object manifest and version-map checksums,
readiness/generation/download output, and checksums over all drill evidence.
Preserve the isolated volumes until a reviewer accepts the record; then destroy
only that explicitly named restore project.

A production incident restore uses fresh production volumes or managed
instances and repeats the same checksum, metadata/tag, version-map, and
migration-10 checks under an approved recovery change. Keep all production controllers,
canaries, cleanup jobs, importers, and legacy writers stopped throughout.
Start only a loopback canary after integrity validation, then complete staged
traffic promotion. Resume cleanup and external writers only after recovery is
accepted and schema compatibility is confirmed. Any checksum, count, probe,
warm-up, migration, or restore error leaves all writers stopped and preserves
the failed and old state for investigation. Do not rerun the logical restore
against its partially populated target; provision another isolated project
with fresh volumes and an empty version namespace, then restart the verified
procedure from the beginning.

## Secret rotation

Write replacement files with `0600` mode and rename them atomically. A running
container does not reload secrets; recreate the affected service.

- **Metrics token:** replace the file, recreate `controller`, update the
  scraper, and verify authenticated `/metrics`. Coordinate overlap externally
  because the backend accepts one token.
- **PostgreSQL app password:** replace `postgres-app-password`, run
  `postgres-permissions` to update the role, then recreate `controller`.
- **PostgreSQL admin password:** alter the admin role using the current
  credential, replace `postgres-admin-password`, recreate `postgres` so its
  mounted secret points at the replacement file, and verify both `migrate` and
  `postgres-permissions`. The upstream-compatible image password file only
  initializes a new database; changing the file alone does not alter an
  existing role.
- **MinIO app credential:** generate a new access key and secret, update both
  app secret paths, run the serialized `run_release_one_shot` helper for
  `minio-init`, retain only `sanitized_minio_init_evidence`, recreate the
  controller, verify readiness, then remove the old MinIO user. Changing the
  access key creates a second principal and permits overlap.
- **MinIO root credential:** update both root files and recreate `minio`, then
  rerun `minio-init` through `run_release_one_shot` and retain only sanitized
  evidence. Schedule this as a storage maintenance event and retain the app
  credential throughout.

Never print secrets in tickets or shell tracing. Disable command history for
manual credential commands, and inspect process arguments when designing
automation. Rotate immediately after suspected exposure.

## Monitoring and routine operations

The controller emits JSON logs. Docker limits each service to five 10 MiB log
files; forward logs off-host before local rotation loses them.

```bash
compose ps
compose logs --since 15m controller
curl --fail http://127.0.0.1:8080/api/v1/livez
curl --fail http://127.0.0.1:8080/api/v1/readyz
(
  set -Eeuo pipefail
  case $- in *x*) printf 'disable shell tracing before reading metrics credentials\n' >&2; exit 1 ;; esac
  metrics_header="$(mktemp)"
  trap 'rm -f -- "$metrics_header"' EXIT HUP INT TERM
  chmod 0600 "$metrics_header"
  sudo /bin/sh -eu -c '
    token=$(cat -- "$1")
    [ -n "$token" ]
    [ "$(printf %s "$token" | wc -l)" -eq 0 ]
    printf "Authorization: Bearer %s\n" "$token"
  ' metrics-header /etc/emfont/secrets/metrics-bearer-token >"$metrics_header"
  curl --fail --silent --show-error \
    --header "@$metrics_header" \
    http://127.0.0.1:8080/metrics
)
compose run --rm --no-deps migrate \
  /usr/local/bin/emfont-migrate -command status
```

Alert on sustained readiness failure, restart loops, HTTP 5xx/429/503 rate,
request and font-build latency, build failures, build admission rejection,
nonzero queued builds, PostgreSQL connection and lock pressure, object-store
errors, host/volume capacity, inode exhaustion, certificate expiry, a new
fixable HIGH/CRITICAL finding, an unreviewed full-report delta, and backup
age/failure. Capacity alerts must account for all MinIO object versions and
PostgreSQL growth. The migration status for this release must show exactly
migrations 1 through 10 applied, with no later or unapplied version. Also alert
when the quota ledger differs from `count(*)` and `sum(quota_bytes)` in
`font_artifacts`; any mismatch is a write stop and restore/repair event, not a
counter to edit manually.

Readiness returning 503 usually means database connectivity/schema or object
bucket access. Check `controller`, `migrate`, and `postgres-permissions` logs,
then the `minio-init` container state, exit status, and allowlisted sanitized
evidence. Do not print or export its raw logs. A live but unready instance must
remain out of load-balancer rotation. Treat build-path 429/503 and a growing
queue as capacity or dependency signals; investigate traffic identity, cache
hit ratio, PostgreSQL, and object storage before changing limits.

### Artifact cleanup

Run the bounded cleanup command at least daily after the controller stack is
healthy. It retires inactive rows, keeps them referenced through the
retirement grace, then removes expired rows and old unreferenced generated
objects. Running builds and recently touched ready artifacts are not cleanup
candidates.

```bash
compose --profile maintenance run --rm --no-deps fontcleanup
```

The command writes a JSON report and exits nonzero on partial deletion or
dependency failures. Alert on a nonzero exit, `objectDeletionFailures > 0`, or
any `*LimitReached` field. A reached object-page limit requires increasing
`EMFONT_CLEANUP_MAX_OBJECT_PAGES`; otherwise later lexicographic pages will not
be scanned. Keep `EMFONT_CLEANUP_RETIREMENT_GRACE` at least as long as the
longest read-gateway/CDN and client cache lifetime, and keep
`EMFONT_CLEANUP_ORPHAN_GRACE` longer than both the build timeout and lease.
That strict age invariant, the live-lease check on publication, and fenced
generated-object keys ensure that an object old enough for orphan deletion
cannot still be committed by a valid build claim after the database reference
check. Reducing the orphan grace to the lease or timeout reopens that race and
is rejected by controller and cleanup configuration validation.

Schedule exactly one cleanup job per database/bucket pair. Do not run it inside
every controller replica. The following systemd unit and timer run it daily
through the same release-verifying wrapper used by cron. Install the root-owned
wrapper shown below before enabling either scheduler.

```systemd
# /etc/systemd/system/emfont-fontcleanup.service
[Unit]
Description=Emfont artifact cleanup
Requires=docker.service
After=docker.service network-online.target

[Service]
Type=oneshot
WorkingDirectory=/
UMask=0077
UnsetEnvironment=COMPOSE_PROJECT_NAME EMFONT_VERSION
UnsetEnvironment=EMFONT_BACKEND_IMAGE_REPOSITORY EMFONT_BACKEND_IMAGE_SHA256
UnsetEnvironment=EMFONT_POSTGRES_IMAGE_REPOSITORY EMFONT_POSTGRES_IMAGE_SHA256
UnsetEnvironment=EMFONT_MINIO_IMAGE_REPOSITORY EMFONT_MINIO_IMAGE_SHA256
UnsetEnvironment=EMFONT_MINIO_MC_IMAGE_REPOSITORY EMFONT_MINIO_MC_IMAGE_SHA256
ExecStart=/usr/local/sbin/emfont-fontcleanup
TimeoutStartSec=35min
StandardOutput=journal
StandardError=journal
```

```systemd
# /etc/systemd/system/emfont-fontcleanup.timer
[Unit]
Description=Run Emfont artifact cleanup daily

[Timer]
OnCalendar=*-*-* 03:17:00 UTC
RandomizedDelaySec=15m
Persistent=true
Unit=emfont-fontcleanup.service

[Install]
WantedBy=timers.target
```

Install this root-owned executable as `/usr/local/sbin/emfont-fontcleanup` for
both systemd and cron:

```sh
#!/bin/sh
set -eu
exec 9>/run/lock/emfont-fontcleanup.lock
/usr/bin/flock -n 9 || exit 0
release_env=$(/usr/bin/readlink -f /etc/emfont/current-release.env)
release_dir=$(/usr/bin/dirname "$release_env")
manifest_dir=$release_dir/release-manifest
compose_file=$manifest_dir/docker-compose.backend.yml
[ -f "$release_env" ]
[ "$(/usr/bin/stat -c '%u:%g:%a' "$release_env")" = 0:0:400 ]
(cd "$manifest_dir" && /usr/bin/sha256sum --check --strict SHA256SUMS)
/usr/bin/bash "$manifest_dir/verify-compose-release.sh" verify \
  "$manifest_dir" "$manifest_dir/images.env" >/dev/null
exec /usr/bin/env \
  -u COMPOSE_PROJECT_NAME \
  -u EMFONT_VERSION \
  -u EMFONT_BACKEND_IMAGE_REPOSITORY \
  -u EMFONT_BACKEND_IMAGE_SHA256 \
  -u EMFONT_POSTGRES_IMAGE_REPOSITORY \
  -u EMFONT_POSTGRES_IMAGE_SHA256 \
  -u EMFONT_MINIO_IMAGE_REPOSITORY \
  -u EMFONT_MINIO_IMAGE_SHA256 \
  -u EMFONT_MINIO_MC_IMAGE_REPOSITORY \
  -u EMFONT_MINIO_MC_IMAGE_SHA256 \
  /usr/bin/docker compose \
  --env-file /etc/emfont/backend.env \
  --env-file "$release_env" \
  -f "$compose_file" \
  --profile maintenance run --rm --no-deps fontcleanup
```

Then enable and test the systemd schedule:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now emfont-fontcleanup.timer
systemctl list-timers emfont-fontcleanup.timer
sudo systemctl start emfont-fontcleanup.service
sudo journalctl -u emfont-fontcleanup.service -n 20 --no-pager
```

Then use this `/etc/cron.d/emfont-fontcleanup` entry as an alternative, not a
second scheduler. Cron mail or an equivalent log shipper must deliver failures
to operations:

```cron
MAILTO=ops@example.com
17 3 * * * root /usr/local/sbin/emfont-fontcleanup
```

Capture the JSON report centrally. Alert on all of the following:

- The command exits nonzero, the systemd unit fails, or no successful report is
  received for 26 hours.
- `objectDeletionFailures` is greater than zero.
- `retirementLimitReached`, `rowDeletionLimitReached`, or
  `objectPageLimitReached` is true.
- Runtime approaches `EMFONT_CLEANUP_TIMEOUT`, or row/object counts grow across
  repeated successful runs.
- `objectsChanged` remains elevated across runs, or noncurrent-version capacity
  does not decline after `EMFONT_MINIO_NONCURRENT_EXPIRE_DAYS`.

Pause the timer before backup, restore, migration-9 maintenance, or storage
repair; wait for an active service to exit before declaring writers quiesced.
Resume it only after the deployment or recovery is accepted.

For incidents:

1. Stop routing to unready instances; preserve logs and timestamps.
2. Stop `controller` if writes could worsen corruption or disk exhaustion.
3. Snapshot or back up current state before repair when storage permits.
4. Prefer restoring service by a compatible full-release rollback or forward repair. Avoid
   ad hoc schema and object mutations.
5. Validate row/object consistency and public downloads before reopening.
6. Record the recovery point, commands, image digests, and follow-up actions.

## Dependency and capacity upgrades

Treat every digest update as a release. Review upstream security notes and
update pinned Dockerfile inputs when applicable, then publish only through
`backend-release.yml`. Acquire its final verified manifest and let the strict
parser populate all repository/SHA-256 pairs; never edit image values directly.
Run the backup and canary procedure and retain the prior verified manifest for
a compatibility-reviewed full-release rollback.

Rescan every deployed digest whenever the vulnerability database changes and
at least daily. A newly fixable HIGH/CRITICAL finding requires remediation; a
new or changed unfixed finding requires a fresh documented review. Until that
review is complete, freeze new deployments and follow the security incident
policy for the running release.

PostgreSQL patch updates require a restart and normal backup. Major updates
require a rehearsed `pg_dump`/`pg_restore` or `pg_upgrade` plan; never attach a
new major image directly to the old volume. MinIO upgrades and any rollback
must follow that release's documented compatibility path and be tested against
a restored copy of production data. Removing the public-exposure blocker for
CVE-2026-40344, CVE-2026-41145, CVE-2026-34204, or CVE-2026-39414 requires
upstream advisory closure or equivalent reviewed research, a new server build,
and a new full scan. The `emfont.3` source patch applies only to the first two
and permits the private deployment exception above; it does not patch the
latter two or permit direct public exposure.

CVE-2026-33322 and CVE-2026-33419 are separate unfixed identity-provider
findings. The release record must retain their full Trivy entries and prove the
`minio-init` OIDC/LDAP disabled check passed against the deployed volume. They
cannot be accepted for a deployment that configures either identity provider.

Revisit memory, CPU, PostgreSQL connection limits, build concurrency, pending
build limit, artifact count/byte quotas, request timeout, and reverse-proxy
timeout together. The default controller memory limit is 5 GiB for concurrency
2 and satisfies the conservative sizing formula above. The default controller
request timeout is 100 seconds and write timeout is 110 seconds to cover the
90-second font build timeout. Keep the write timeout greater than the request
timeout and the build lease greater than the build timeout.
