# Backend Guidelines

This directory is a Go backend module following the Netstamp-style controller architecture.

## Structure

- Keep `cmd/controller` and `cmd/migrate` thin.
- Wire concrete dependencies only in `internal/controller/app`.
- Keep HTTP request/response details in `internal/controller/transport/http`.
- Keep business use cases and ports in `internal/controller/application`.
- Keep domain models and validation in `internal/domain`.
- Keep pgx/sqlc details in `internal/controller/infrastructure/postgres`.
- Keep native font code in `internal/controller/infrastructure/fontbuild`; cgo must not leak into application or domain packages.
- Keep MinIO details in `internal/controller/infrastructure/objectstore`.

## Rules

- Do not let handlers call repositories directly.
- Do not let application packages import HTTP, pgx, sqlc, logger, config, or app packages.
- Do not let Postgres adapters import application or transport packages.
- Do not hand-edit generated sqlc files.
- Keep migrations in `db/migrations` and sqlc source queries in `db/query`.
- Run `go test ./...` from `backend/` after backend changes.

## Commands

- Test: `go test ./...`
- Generate sqlc: `go run -modfile=tools/go.mod github.com/sqlc-dev/sqlc/cmd/sqlc -f sqlc.yaml --no-remote generate`
- Migration status: `go run ./cmd/migrate -command status`
- Run controller: `go run ./cmd/controller`
- Build image: `docker build -t emfont-backend .`
