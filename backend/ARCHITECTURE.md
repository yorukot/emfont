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
- `internal/controller/infrastructure/fontbuild`: HarfBuzz subset + WOFF2 cgo adapter.
- `internal/platform/observability`: shared metrics and tracing helpers.

The dependency direction is:

```text
HTTP transport -> application service/ports -> PostgreSQL adapter -> sqlc/pgx -> PostgreSQL
                         |                  -> MinIO adapter ----------------> MinIO
                         |                  -> HarfBuzz/WOFF2 adapter --------> native libraries
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
- `infrastructure/fontbuild/harfbuzz`: native Unicode subsetting followed by WOFF2 compression.
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

Object keys include a short source/builder revision. Updating a source font or native builder therefore creates a new object instead of colliding with or silently overwriting an older cache entry. Expired leases can be taken over, but stale workers cannot publish after takeover.

## Generated Inputs

- Source migrations live in `db/migrations`.
- Source sqlc queries live in `db/query`.
- sqlc config lives in `sqlc.yaml`.
- Generated query code lives in `internal/controller/infrastructure/postgres/sqlc` and must not be edited by hand.
- Embedded OpenAPI lives in `internal/controller/transport/http/openapi/openapi.json`.

## Operations

- Run migrations before the controller; readiness fails until the font tables exist.
- MinIO is optional for metadata-only endpoints. Set `EMFONT_MINIO_ENABLED=true` for generation; credentials and bucket are then required.
- Native builds require cgo, HarfBuzz subset, and WOFF2. `backend/Dockerfile` provides the build/runtime dependencies.
- Original objects use `original-fonts/{font}/{weight}.{ttf|otf}` unless a `font_sources.object_key` overrides the path.
- Public generated routes are available under both `/api/{version}` and the legacy root paths.

## Official Reference Test

`internal/integration/font_pipeline_test.go` runs the real HTTP, PostgreSQL, MinIO, and cgo pipeline, then compares the downloaded WOFF2 byte-for-byte with `hb-subset` plus `woff2_compress`.

The verified fixture is Google Fonts commit `ec0464b978de222073645d6d3366f3fdf03376d8`, file `ofl/notosanstc/NotoSansTC[wght].ttf`, source SHA-256 `864727d210d54f2537bbe23b3a839436c3992af72de9322af5270897246bd44f`. For text `測試字型ABC`, both implementations produce WOFF2 SHA-256 `f294100c8c8e33890890515a9e6a73d1cd7804b144ecc9b14b7a4194862ce2d5`.

## Boundary Rules

Architecture tests enforce:

- `internal/domain/...` does not import controller, platform, or cmd packages.
- `internal/controller/application/...` does not import app, config, infrastructure, logger, transport, or cmd packages.
- `internal/controller/infrastructure/postgres/...` does not import application, transport, or app packages.

Transport maps application/domain errors to protocol responses. Infrastructure maps pgx/sqlc errors to domain errors. Application services stay framework- and database-agnostic.
