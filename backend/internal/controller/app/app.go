package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	appsystem "github.com/emfont/emfont/backend/internal/controller/application/system"
	"github.com/emfont/emfont/backend/internal/controller/config"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzz"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/objectstore"
	miniostore "github.com/emfont/emfont/backend/internal/controller/infrastructure/objectstore/minio"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/postgres"
	"github.com/emfont/emfont/backend/internal/controller/logger"
	httpserver "github.com/emfont/emfont/backend/internal/controller/transport/http"
	"github.com/emfont/emfont/backend/internal/platform/observability/metrics"
	"github.com/emfont/emfont/backend/internal/platform/observability/tracing"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type Application struct {
	Config config.Config
	Log    *zap.Logger

	HTTPServer *httpserver.Server
	DBPool     *pgxpool.Pool
	Metrics    *metrics.Metrics
	Tracing    *tracing.Provider
}

func New(ctx context.Context) (*Application, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return NewWithConfig(ctx, cfg)
}

func NewWithConfig(ctx context.Context, cfg config.Config) (*Application, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	log, err := logger.New(logger.Config{
		Level:       cfg.Log.Level,
		Environment: cfg.Environment,
		Service:     cfg.ServiceName,
		Version:     cfg.Version,
		Encoding:    cfg.Log.Format,
	})
	if err != nil {
		return nil, fmt.Errorf("create logger: %w", err)
	}

	metricsProvider, err := buildMetrics(cfg)
	if err != nil {
		return nil, fmt.Errorf("create metrics: %w", err)
	}

	tracingProvider, err := tracing.NewProvider(ctx, tracing.Config{
		ServiceName:  cfg.ServiceName,
		Enabled:      cfg.Tracing.Enabled,
		OTLPEndpoint: cfg.Tracing.OTLPEndpoint,
	})
	if err != nil {
		return nil, fmt.Errorf("create tracing: %w", err)
	}

	dbPool, err := postgres.NewPool(ctx, postgres.Config{
		DatabaseURL:     cfg.Database.URL,
		ApplicationName: cfg.ServiceName,
		MaxConns:        int32(cfg.Database.MaxOpenConns),
		MinIdleConns:    int32(cfg.Database.MinIdleConns),
		MaxConnLifetime: cfg.Database.ConnMaxLifetime,
	})
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	systemService, err := buildSystemService(dbPool)
	if err != nil {
		dbPool.Close()
		return nil, err
	}
	fontService, minioStore, err := buildFontService(cfg, dbPool)
	if err != nil {
		dbPool.Close()
		return nil, err
	}

	handler := httpserver.NewRouter(httpserver.Dependencies{
		Log:            log,
		APIVersion:     cfg.APIVersion,
		ServiceName:    cfg.ServiceName,
		Version:        cfg.Version,
		RequestTimeout: cfg.HTTP.RequestTimeout,
		ReadinessCheck: func(ctx context.Context) error {
			if err := postgres.Ping(ctx, dbPool); err != nil {
				return err
			}
			if err := postgres.FontSchemaReady(ctx, dbPool); err != nil {
				return err
			}
			if minioStore != nil {
				exists, err := minioStore.BucketExists(ctx)
				if err != nil {
					return err
				}
				if !exists {
					return errors.New("configured MinIO bucket does not exist")
				}
			}
			return nil
		},
		Metrics:       metricsProvider,
		MetricsPath:   cfg.Metrics.Path,
		FontService:   fontService,
		SystemService: systemService,
		Tracing:       cfg.Tracing.Enabled,
		OpenAPI: httpserver.OpenAPIConfig{
			Version:        cfg.APIVersion,
			BackendBaseURL: cfg.HTTP.BackendBaseURL,
		},
	})

	return &Application{
		Config: cfg,
		Log:    log,
		HTTPServer: httpserver.NewServer(httpserver.ServerConfig{
			Addr:              cfg.HTTP.Addr,
			ReadTimeout:       cfg.HTTP.ReadTimeout,
			ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
			WriteTimeout:      cfg.HTTP.WriteTimeout,
			IdleTimeout:       cfg.HTTP.IdleTimeout,
			ShutdownTimeout:   cfg.ShutdownTimeout,
		}, handler, log),
		DBPool:  dbPool,
		Metrics: metricsProvider,
		Tracing: tracingProvider,
	}, nil
}

func buildFontService(cfg config.Config, pool *pgxpool.Pool) (*appfont.Service, *miniostore.Store, error) {
	repository := postgres.NewFontRepositoryFromPool(pool)
	builder := harfbuzz.New()
	var objects appfont.ObjectStore = objectstore.Unavailable{}
	var minioStore *miniostore.Store
	if cfg.ObjectStorage.Enabled {
		if err := builder.Available(); err != nil {
			return nil, nil, fmt.Errorf("font builder is unavailable: %w", err)
		}
		store, err := miniostore.New(miniostore.Config{
			Endpoint: cfg.ObjectStorage.Endpoint, AccessKey: cfg.ObjectStorage.AccessKey,
			SecretKey: cfg.ObjectStorage.SecretKey, SessionToken: cfg.ObjectStorage.SessionToken,
			Bucket: cfg.ObjectStorage.Bucket, Region: cfg.ObjectStorage.Region,
			Secure: cfg.ObjectStorage.Secure, PublicBaseURL: cfg.ObjectStorage.PublicBaseURL,
			PresignExpiry: cfg.ObjectStorage.PresignExpiry,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("create MinIO object store: %w", err)
		}
		objects = store
		minioStore = store
	}
	service, err := appfont.NewService(repository, objects, builder, appfont.Config{
		BuilderVersion:         cfg.FontBuild.BuilderVersion,
		BuildLease:             cfg.FontBuild.BuildLease,
		BuildTimeout:           cfg.FontBuild.BuildTimeout,
		StaticBuildConcurrency: cfg.FontBuild.StaticBuildConcurrency,
		ForceMin:               cfg.FontBuild.ForceMin,
		MaxSourceBytes:         cfg.FontBuild.MaxSourceBytes,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create font service: %w", err)
	}
	return service, minioStore, nil
}

func buildMetrics(cfg config.Config) (*metrics.Metrics, error) {
	if !cfg.Metrics.Enabled {
		return nil, nil
	}
	return metrics.New(metrics.Config{
		Service:        cfg.ServiceName,
		Version:        cfg.Version,
		IncludeRuntime: true,
	})
}

func buildSystemService(pool *pgxpool.Pool) (*appsystem.Service, error) {
	repository := postgres.NewSystemRepositoryFromPool(pool)
	service, err := appsystem.NewService(repository)
	if err != nil {
		return nil, fmt.Errorf("create system service: %w", err)
	}
	return service, nil
}

func (a *Application) Run(ctx context.Context) error {
	if a == nil || a.HTTPServer == nil {
		return errors.New("application is not configured")
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		if err := a.HTTPServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})
	group.Go(func() error {
		<-groupCtx.Done()
		return a.Shutdown(groupCtx)
	})

	return group.Wait()
}

func (a *Application) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.Config.ShutdownTimeout)
	defer cancel()

	var errs []error
	if a.HTTPServer != nil {
		if err := a.HTTPServer.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown http: %w", err))
		}
	}
	if a.DBPool != nil {
		a.DBPool.Close()
	}
	if a.Metrics != nil {
		if err := a.Metrics.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown metrics: %w", err))
		}
	}
	if a.Tracing != nil {
		if err := a.Tracing.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown tracing: %w", err))
		}
	}

	return errors.Join(errs...)
}
