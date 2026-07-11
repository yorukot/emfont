package app

import (
	"context"
	"errors"
	"fmt"

	appcleanup "github.com/emfont/emfont/backend/internal/controller/application/fontcleanup"
	"github.com/emfont/emfont/backend/internal/controller/config"
	miniostore "github.com/emfont/emfont/backend/internal/controller/infrastructure/objectstore/minio"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FontCleanupApplication struct {
	Config  config.Config
	Service *appcleanup.Service
	DBPool  *pgxpool.Pool
}

func NewFontCleanup(ctx context.Context) (*FontCleanupApplication, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return NewFontCleanupWithConfig(ctx, cfg)
}

func NewFontCleanupWithConfig(ctx context.Context, cfg config.Config) (*FontCleanupApplication, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	if !cfg.ObjectStorage.Enabled {
		return nil, errors.New("font cleanup requires MinIO object storage")
	}

	pool, err := postgres.NewPool(ctx, postgres.Config{
		DatabaseURL: cfg.Database.URL, ApplicationName: cfg.ServiceName + "-fontcleanup",
		MaxConns: int32(cfg.Database.MaxOpenConns), MinIdleConns: int32(cfg.Database.MinIdleConns),
		MaxConnLifetime: cfg.Database.ConnMaxLifetime,
	})
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	store, err := miniostore.New(miniostore.Config{
		Endpoint: cfg.ObjectStorage.Endpoint, AccessKey: cfg.ObjectStorage.AccessKey,
		SecretKey: cfg.ObjectStorage.SecretKey, SessionToken: cfg.ObjectStorage.SessionToken,
		Bucket: cfg.ObjectStorage.Bucket, Region: cfg.ObjectStorage.Region,
		Secure: cfg.ObjectStorage.Secure, PublicBaseURL: cfg.ObjectStorage.PublicBaseURL,
		PresignExpiry: cfg.ObjectStorage.PresignExpiry,
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create MinIO object store: %w", err)
	}
	service, err := appcleanup.NewService(
		postgres.NewFontRepositoryFromPool(pool),
		store,
		appcleanup.Config{
			ArtifactRetention: cfg.Cleanup.ArtifactRetention,
			RetirementGrace:   cfg.Cleanup.RetirementGrace,
			OrphanGrace:       cfg.Cleanup.OrphanGrace,
			DatabaseBatchSize: cfg.Cleanup.DatabaseBatchSize,
			MaxDatabaseRows:   cfg.Cleanup.MaxDatabaseRows,
			ObjectPageSize:    cfg.Cleanup.ObjectPageSize,
			MaxObjectPages:    cfg.Cleanup.MaxObjectPages,
		},
	)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create font cleanup service: %w", err)
	}
	return &FontCleanupApplication{Config: cfg, Service: service, DBPool: pool}, nil
}

func (a *FontCleanupApplication) Close() error {
	if a != nil && a.DBPool != nil {
		a.DBPool.Close()
	}
	return nil
}
