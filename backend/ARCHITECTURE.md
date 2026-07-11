# Backend Architecture

This backend is a controller-only Go modular monolith modeled after the Netstamp backend pattern.

## Target Shape

- `cmd/controller`: thin process entrypoint. It creates the shutdown context, calls `app.New`, runs the app, and syncs the logger.
- `cmd/migrate`: Goose migration CLI. Migrations are run as a separate operational step, not silently by the controller.
- `internal/controller/app`: composition root. It wires config, logging, metrics, tracing, PostgreSQL, application services, and HTTP.
- `internal/controller/transport/http`: HTTP routing, middleware, request binding, response mapping, OpenAPI/docs, health, metrics, and problem+json responses.
- `internal/controller/application/*`: use-case orchestration, DTOs, ports, validation, sentinel errors, and tracing interfaces.
- `internal/domain/*`: stable domain models and validation/normalization. Domain packages must not import controller packages.
- `internal/controller/infrastructure/postgres`: pgx/sqlc adapters, pool/readiness helpers, transaction support, durable build leases, and database error translation.
- `internal/controller/infrastructure/objectstore`: MinIO artifact/source adapter and an unavailable adapter for metadata-only deployments.
- `internal/controller/infrastructure/fontbuild`: framed subprocess adapter for the isolated HarfBuzz + WOFF2 worker. Only the worker links native libraries through cgo; the controller is built with cgo disabled.
- `internal/platform/observability`: shared metrics and tracing helpers.

The dependency direction is:

```text
HTTP transport -> application service/ports -> PostgreSQL adapter -> sqlc/pgx -> PostgreSQL
                         |                  -> MinIO adapter ----------------> MinIO
                         |                  -> worker subprocess protocol ----> cgo HarfBuzz/WOFF2
                      domain
```

Only `internal/controller/app` wires concrete packages together.

## Vertical Slices

The scaffold includes a small `system` slice:

- `domain/system`: validates and normalizes system metadata.
- `application/system`: owns `Get` and `Upsert` use cases and defines the `Store` port.
- `infrastructure/postgres`: implements the store port with `system_metadata` through sqlc.
- `transport/http/handler/system`: exposes read-only `GET /api/v1/system`. Mutation stays disabled until an authenticated admin slice exists.

The `font` slice is the first product slice:

- `domain/font`: font identity, weight resolution, normalized character sets, legacy-compatible hashes, and versioned artifact/object keys.
- `application/font`: `/g`, `/css`, `/list`, and `/info` use cases; cache validation; bounded builds; singleflight; and durable lease orchestration.
- `infrastructure/postgres/font_repository`: font/source metadata, artifacts, static packs, and fenced build jobs.
- `infrastructure/objectstore/minio`: source reads and artifact HEAD/PUT/GET/public URLs with checksum metadata.
- `infrastructure/fontbuild/harfbuzz`: bounded worker subprocess lifecycle and protocol; `harfbuzznative` performs native Unicode subsetting and WOFF2 compression inside `cmd/fontworker`.
- `transport/http/handler/font`: versioned routes and legacy root aliases.

## Artifact State

PostgreSQL is authoritative for metadata and build state. MinIO is authoritative for durable bytes. Local files are not used to decide whether a font is built.

```text
request
  -> deterministic key (font + weight + text/pack + source fingerprint + builder version)
  -> PostgreSQL artifact lookup
  -> MinIO HEAD validates size + ETag + SHA-256
  -> cache hit: return URL
  -> cache miss: acquire fenced PostgreSQL lease
  -> bounded HarfBuzz/WOFF2 build
  -> MinIO PUT
  -> fenced artifact ready + job complete
```

Object keys include a short source/builder revision. The production native-builder identity exposes its exact OS/architecture, Go toolchain, and Debian HarfBuzz/WOFF2 package revisions. It also contains separate SHA-256 identities for the production sources and a canonical native package manifest. That manifest records the resolved path, owning Debian binary package, package revision, and architecture for the actual C/C++ compiler, compiler backends, assembler, linker, archiver, `pkg-config`, and every library in the worker's transitive `DT_NEEDED` closure. Changing any source, tool, or runtime package creates a new cache identity. Updating a source font or native builder therefore creates a new object instead of colliding with or silently overwriting an older cache entry. Expired leases can be taken over, but stale workers cannot publish after takeover.

The build stage generates the package manifest from a provisional worker, embeds its digest into the final worker, and confirms that rebuilding the final worker does not change the manifest. The final runtime image regenerates its installed closure and requires an exact byte comparison with the build-stage runtime manifest. Both canonical manifests and their digest are retained under `/usr/local/share/emfont/` for audit. Hardened controller environments reject worker identities that omit this structure.

## Generated Inputs

- Source migrations live in `db/migrations`.
- Source sqlc queries live in `db/query`.
- sqlc config lives in `sqlc.yaml`.
- Generated query code lives in `internal/controller/infrastructure/postgres/sqlc` and must not be edited by hand.
- Embedded OpenAPI lives in `internal/controller/transport/http/openapi/openapi.json`.

## Operations

- Run migrations before the controller; readiness fails until the font tables exist.
- MinIO is optional for metadata-only endpoints. Set `EMFONT_MINIO_ENABLED=true` for generation; credentials and bucket are then required.
- The controller does not require cgo. Building `emfont-fontworker` requires cgo, HarfBuzz subset, and WOFF2; `backend/Dockerfile` builds both binaries and provides only the worker's pinned native runtime dependencies.
- Original objects use `original-fonts/{font}/{weight}.{ttf|otf}` unless a `font_sources.object_key` overrides the path.
- Public generated routes are available under both `/api/{version}` and the legacy root paths.

## Official Reference Test

`internal/integration/font_pipeline_test.go` runs the real HTTP, PostgreSQL, MinIO, and worker-subprocess pipeline, then compares the downloaded WOFF2 byte-for-byte with the same host versions of `hb-subset` and `woff2_compress`. The final-image gate builds the Dockerfile's `font-reference` stage from the same pinned Debian base, snapshot, HarfBuzz revision, and WOFF2 revision as production. It runs `hb-subset` plus `woff2_compress`, byte-compares that result with the isolated worker output, and only then checks the retained production hash and size.

The verified fixture is Google Fonts commit `ec0464b978de222073645d6d3366f3fdf03376d8`, file `ofl/notosanstc/NotoSansTC[wght].ttf`, source SHA-256 `864727d210d54f2537bbe23b3a839436c3992af72de9322af5270897246bd44f`. Subset bytes are version-dependent: for text `測試字型ABC`, HarfBuzz 6.0.0 with WOFF2 1.0.2 produces SHA-256 `f294100c8c8e33890890515a9e6a73d1cd7804b144ecc9b14b7a4194862ce2d5` (2756 bytes), while the production HarfBuzz 10.2.0 with WOFF2 1.0.2 produces SHA-256 `3e365346851cf540ccbef2b61ca7c05c51ff93833c8a928c5a816884373819e2` (2868 bytes). In each case, the worker output must exactly match the same-version official CLI output.

## Boundary Rules

Architecture tests enforce:

- `internal/domain/...` does not import controller, platform, or cmd packages.
- `internal/controller/application/...` does not import app, config, infrastructure, logger, transport, or cmd packages.
- `internal/controller/infrastructure/postgres/...` does not import application, transport, or app packages.

Transport maps application/domain errors to protocol responses. Infrastructure maps pgx/sqlc errors to domain errors. Application services stay framework- and database-agnostic.
