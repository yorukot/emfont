package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	appsystem "github.com/emfont/emfont/backend/internal/controller/application/system"
	"github.com/emfont/emfont/backend/internal/controller/config"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzz"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/objectstore"
	miniostore "github.com/emfont/emfont/backend/internal/controller/infrastructure/objectstore/minio"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/postgres"
	"github.com/emfont/emfont/backend/internal/controller/logger"
	httpserver "github.com/emfont/emfont/backend/internal/controller/transport/http"
	httpmiddleware "github.com/emfont/emfont/backend/internal/controller/transport/http/middleware"
	"github.com/emfont/emfont/backend/internal/platform/observability/metrics"
	"github.com/emfont/emfont/backend/internal/platform/observability/tracing"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type Application struct {
	Config config.Config
	Log    *zap.Logger

	HTTPServer  *httpserver.Server
	FontService *appfont.Service
	DBPool      *pgxpool.Pool
	Metrics     *metrics.Metrics
	Tracing     *tracing.Provider

	readiness          *readinessChecker
	shutdownFont       func(context.Context) error
	waitForPropagation func(context.Context, time.Duration) error
	shutdownOnce       sync.Once
	shutdownErr        error
}

func New(ctx context.Context) (*Application, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return NewWithConfig(ctx, cfg)
}

func NewWithConfig(ctx context.Context, cfg config.Config) (*Application, error) {
	if err := cfg.ValidateController(); err != nil {
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
		ServiceName: cfg.ServiceName, ServiceVersion: cfg.Version,
		Enabled: cfg.Tracing.Enabled, OTLPEndpoint: cfg.Tracing.OTLPEndpoint,
		SampleRatio: cfg.Tracing.SampleRatio, RequireHTTPS: config.IsHardenedEnvironment(cfg.Environment),
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
	fontService, minioStore, err := buildFontService(cfg, dbPool, metricsProvider)
	if err != nil {
		dbPool.Close()
		return nil, err
	}
	fontRateLimit, err := buildFontRateLimit(cfg)
	if err != nil {
		dbPool.Close()
		return nil, err
	}
	aggregateRateLimit, err := buildAggregateRateLimit(cfg)
	if err != nil {
		dbPool.Close()
		return nil, err
	}
	healthRateLimit, err := buildHealthRateLimit(cfg)
	if err != nil {
		dbPool.Close()
		return nil, err
	}
	metricsAuthenticatedRateLimit, err := buildMetricsRateLimit(cfg)
	if err != nil {
		dbPool.Close()
		return nil, err
	}
	metricsRejectedRateLimit, err := buildMetricsRateLimit(cfg)
	if err != nil {
		dbPool.Close()
		return nil, err
	}

	readinessCheck := newReadinessCheckerWithConfig(readinessDependencies(dbPool, minioStore), readinessCheckerConfig{
		SuccessTTL:   readinessSuccessTTL,
		FailureTTL:   readinessFailureTTL,
		Timeout:      readinessProbeTimeout,
		OnTransition: readinessTransitionLogger(log),
	})
	handler := httpserver.NewRouter(httpserver.Dependencies{
		Log:                           log,
		APIVersion:                    cfg.APIVersion,
		ServiceName:                   cfg.ServiceName,
		Version:                       cfg.Version,
		RequestTimeout:                cfg.HTTP.RequestTimeout,
		ReadinessCheck:                readinessCheck.Check,
		Metrics:                       metricsProvider,
		MetricsPath:                   cfg.Metrics.Path,
		MetricsToken:                  cfg.Metrics.BearerToken,
		AllowedOrigins:                cfg.HTTP.AllowedOrigins,
		FontService:                   fontService,
		FontRateLimit:                 fontRateLimit,
		AggregateRateLimit:            aggregateRateLimit,
		HealthRateLimit:               healthRateLimit,
		MetricsAuthenticatedRateLimit: metricsAuthenticatedRateLimit,
		MetricsRejectedRateLimit:      metricsRejectedRateLimit,
		SystemService:                 systemService,
		Tracing:                       cfg.Tracing.Enabled,
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
			MaxHeaderBytes:    cfg.HTTP.MaxHeaderBytes,
			ShutdownTimeout:   cfg.ShutdownTimeout,
		}, handler, log),
		FontService:  fontService,
		DBPool:       dbPool,
		Metrics:      metricsProvider,
		Tracing:      tracingProvider,
		readiness:    readinessCheck,
		shutdownFont: fontService.Shutdown,
	}, nil
}

func readinessDependencies(dbPool *pgxpool.Pool, minioStore *miniostore.Store) readinessProbe {
	return func(ctx context.Context) error {
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
			versioningEnabled, err := minioStore.BucketVersioningEnabled(ctx)
			if err != nil {
				return err
			}
			if !versioningEnabled {
				return errors.New("configured MinIO bucket versioning is not enabled")
			}
		}
		return nil
	}
}

func buildFontService(
	cfg config.Config,
	pool *pgxpool.Pool,
	observer appfont.Observer,
) (*appfont.Service, *miniostore.Store, error) {
	repository := postgres.NewFontRepositoryFromPool(pool, fontRepositoryConfig(cfg))
	builder, err := harfbuzz.NewWithConfig(harfbuzz.Config{
		WorkerPath:                cfg.FontBuild.WorkerPath,
		RequireProductionIdentity: config.IsHardenedEnvironment(cfg.Environment),
		MaxSourceBytes:            cfg.FontBuild.MaxSourceBytes,
		MaxOutputBytes:            cfg.FontBuild.WorkerMaxOutputBytes,
		AddressSpaceLimitBytes:    uint64(cfg.FontBuild.WorkerAddressSpaceBytes),
		CPUTimeLimitSeconds:       uint64(cfg.FontBuild.WorkerCPUSeconds),
		FileSizeLimitBytes:        uint64(cfg.FontBuild.WorkerFileSizeBytes),
		OpenFilesLimit:            uint64(cfg.FontBuild.WorkerOpenFiles),
		StderrLimitBytes:          cfg.FontBuild.WorkerStderrBytes,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("configure font worker: %w", err)
	}
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
	options := make([]appfont.Option, 0, 1)
	if observer != nil {
		options = append(options, appfont.WithObserver(observer))
	}
	service, err := appfont.NewService(repository, objects, builder, appfont.Config{
		BuilderVersion:         cfg.FontBuild.BuilderVersion,
		BuildLease:             cfg.FontBuild.BuildLease,
		BuildTimeout:           cfg.FontBuild.BuildTimeout,
		StaticBuildConcurrency: cfg.FontBuild.StaticBuildConcurrency,
		MaxPendingBuilds:       cfg.FontBuild.MaxPendingBuilds,
		ForceMin:               cfg.FontBuild.ForceMin,
		MaxSourceBytes:         cfg.FontBuild.MaxSourceBytes,
		ArtifactTouchInterval:  cfg.FontBuild.ArtifactTouchInterval,
	}, options...)
	if err != nil {
		return nil, nil, fmt.Errorf("create font service: %w", err)
	}
	return service, minioStore, nil
}

func fontRepositoryConfig(cfg config.Config) postgres.FontRepositoryConfig {
	return postgres.FontRepositoryConfig{
		MaxArtifacts:        cfg.FontBuild.MaxArtifacts,
		MaxAccountedBytes:   cfg.FontBuild.MaxAccountedBytes,
		ArtifactReservation: cfg.FontBuild.WorkerMaxOutputBytes,
		MaxTerminalFailures: cfg.FontBuild.MaxTerminalFailures,
	}
}

func buildFontRateLimit(cfg config.Config) (func(http.Handler) http.Handler, error) {
	if !cfg.RateLimit.Enabled {
		return nil, nil
	}
	keyFunc := httpmiddleware.RemoteIPRateLimitKey
	if cfg.RateLimit.TrustProxyHeaders {
		trustedProxyKey, err := httpmiddleware.NewTrustedProxyIPRateLimitKey(cfg.RateLimit.TrustedProxyCIDRs)
		if err != nil {
			return nil, fmt.Errorf("create trusted proxy rate limit key: %w", err)
		}
		keyFunc = trustedProxyKey
	}
	limiter, err := httpmiddleware.NewRateLimiter(httpmiddleware.RateLimitConfig{
		Rate: cfg.RateLimit.RequestsPerSecond, Burst: cfg.RateLimit.Burst,
		MaxClients: cfg.RateLimit.MaxClients, IdleTimeout: cfg.RateLimit.IdleTimeout,
		KeyFunc: keyFunc,
	})
	if err != nil {
		return nil, fmt.Errorf("create font rate limiter: %w", err)
	}
	return limiter.Middleware, nil
}

func buildAggregateRateLimit(cfg config.Config) (func(http.Handler) http.Handler, error) {
	if !cfg.RateLimit.Enabled {
		return nil, nil
	}
	limiter, err := httpmiddleware.NewRateLimiter(httpmiddleware.RateLimitConfig{
		Rate:        cfg.RateLimit.GlobalRequestsPerSecond,
		Burst:       cfg.RateLimit.GlobalBurst,
		MaxClients:  1,
		IdleTimeout: cfg.RateLimit.IdleTimeout,
		KeyFunc:     httpmiddleware.GlobalRateLimitKey,
	})
	if err != nil {
		return nil, fmt.Errorf("create aggregate rate limiter: %w", err)
	}
	return limiter.Middleware, nil
}

func buildHealthRateLimit(cfg config.Config) (func(http.Handler) http.Handler, error) {
	keyFunc := httpmiddleware.RemoteIPRateLimitKey
	if cfg.RateLimit.TrustProxyHeaders {
		trustedProxyKey, err := httpmiddleware.NewTrustedProxyIPRateLimitKey(cfg.RateLimit.TrustedProxyCIDRs)
		if err != nil {
			return nil, fmt.Errorf("create trusted proxy health rate limit key: %w", err)
		}
		keyFunc = trustedProxyKey
	}
	baseConfig := httpmiddleware.RateLimitConfig{
		Rate: 5, Burst: 10, MaxClients: httpmiddleware.DefaultRateLimitMaxClients,
		IdleTimeout: time.Minute, KeyFunc: keyFunc,
	}
	privateLimiter, err := httpmiddleware.NewRateLimiter(baseConfig)
	if err != nil {
		return nil, fmt.Errorf("create private health rate limiter: %w", err)
	}
	publicConfig := baseConfig
	publicConfig.GlobalRequestsPerSecond = 100
	publicConfig.GlobalBurst = 200
	publicLimiter, err := httpmiddleware.NewRateLimiter(publicConfig)
	if err != nil {
		return nil, fmt.Errorf("create public liveness rate limiter: %w", err)
	}
	return func(next http.Handler) http.Handler {
		publicLiveness := publicLimiter.Middleware(next)
		privateHealth := privateLimiter.Middleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/livez") {
				publicLiveness.ServeHTTP(w, r)
				return
			}
			privateHealth.ServeHTTP(w, r)
		})
	}, nil
}

func buildMetricsRateLimit(cfg config.Config) (func(http.Handler) http.Handler, error) {
	if !cfg.Metrics.Enabled {
		return nil, nil
	}
	limiter, err := httpmiddleware.NewRateLimiter(httpmiddleware.RateLimitConfig{
		Rate:       cfg.Metrics.AuthRequestsPerSecond,
		Burst:      cfg.Metrics.AuthBurst,
		MaxClients: 1,
		KeyFunc:    httpmiddleware.GlobalRateLimitKey,
	})
	if err != nil {
		return nil, fmt.Errorf("create metrics rate limiter: %w", err)
	}
	return limiter.Middleware, nil
}

func readinessTransitionLogger(log *zap.Logger) func(error) {
	if log == nil {
		log = zap.NewNop()
	}
	return func(err error) {
		switch {
		case errors.Is(err, errReadinessDraining):
			log.Info("readiness disabled for shutdown")
		case err != nil:
			log.Warn("readiness dependency check failed", zap.Error(err))
		default:
			log.Info("readiness dependency check recovered")
		}
	}
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
	if ctx == nil {
		ctx = context.Background()
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	group, groupCtx := errgroup.WithContext(runCtx)
	group.Go(func() error {
		defer cancelRun()
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
	a.shutdownOnce.Do(func() {
		a.shutdownErr = a.shutdown(ctx)
	})
	return a.shutdownErr
}

func (a *Application) shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.Config.ShutdownTimeout)
	defer cancel()

	var errs []error
	if a.readiness != nil {
		a.readiness.BeginDrain()
	}
	if a.Log != nil {
		a.Log.Info("application marked unready")
	}
	if a.HTTPServer != nil && a.HTTPServer.IsServing() && a.Config.ShutdownPropagationDelay > 0 {
		waitForPropagation := a.waitForPropagation
		if waitForPropagation == nil {
			waitForPropagation = waitForShutdownPropagation
		}
		if err := waitForPropagation(ctx, a.Config.ShutdownPropagationDelay); err != nil {
			errs = append(errs, fmt.Errorf("wait for readiness propagation: %w", err))
		}
	}

	if a.HTTPServer != nil {
		if err := a.HTTPServer.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown http: %w", err))
		}
	}
	shutdownFont := a.shutdownFont
	if shutdownFont == nil && a.FontService != nil {
		shutdownFont = a.FontService.Shutdown
	}
	if shutdownFont != nil {
		if err := shutdownFont(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown font service: %w", err))
		}
	}
	if a.DBPool != nil {
		closed := make(chan struct{})
		go func() {
			a.DBPool.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("close postgres pool: %w", ctx.Err()))
		}
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

func waitForShutdownPropagation(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
