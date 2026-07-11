package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emfont/emfont/backend/internal/controller/infrastructure/postgres/sqlc"
	domain "github.com/emfont/emfont/backend/internal/domain/font"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FontQuerier interface {
	GetFontFamily(context.Context, string) (sqlc.GetFontFamilyRow, error)
	ListFontFamilies(context.Context, sqlc.ListFontFamiliesParams) ([]sqlc.ListFontFamiliesRow, error)
	GetFontSource(context.Context, string, int16) (sqlc.GetFontSourceRow, error)
	GetFontArtifact(context.Context, string) (sqlc.GetFontArtifactRow, error)
	LockFontArtifactAdmission(context.Context) error
	CreateFontArtifact(context.Context, sqlc.CreateFontArtifactParams) (string, error)
	MarkFontArtifactReady(context.Context, sqlc.MarkFontArtifactReadyParams) (string, error)
	MarkFontArtifactMissing(context.Context, sqlc.MarkFontArtifactMissingParams) (string, error)
	TouchFontArtifact(context.Context, sqlc.TouchFontArtifactParams) (bool, error)
	GetStaticPackSnapshot(context.Context, string, []string) ([]sqlc.GetStaticPackSnapshotRow, error)
	AcquireFontBuildJob(context.Context, sqlc.AcquireFontBuildJobParams) (int64, error)
	GetFontBuildRetryAfterSeconds(context.Context, string) (int64, error)
	FailFontBuildJob(context.Context, sqlc.FailFontBuildJobParams) (int64, error)
	FailFontBuildJobTerminal(context.Context, sqlc.FailFontBuildJobTerminalParams) (int64, error)
}

const (
	defaultMaxArtifacts        = int64(100_000)
	defaultMaxAccountedBytes   = int64(50 << 30)
	defaultArtifactReservation = int64(128 << 20)
	defaultMaxTerminalFailures = int64(10_000)
)

var ErrArtifactQuotaTransactionRequired = errors.New("font artifact quota mutation requires a transaction boundary")

type FontRepositoryConfig struct {
	MaxArtifacts        int64
	MaxAccountedBytes   int64
	ArtifactReservation int64
	MaxTerminalFailures int64
}

type fontCleanupQuerier interface {
	RetireFontArtifacts(context.Context, sqlc.RetireFontArtifactsParams) (int64, error)
	DeleteRetiredFontArtifacts(context.Context, pgtype.Timestamptz, int32) (int64, error)
	FindReferencedFontObjectKeys(context.Context, []string) ([]string, error)
}

type FontRepository struct {
	queries               FontQuerier
	transactor            *Transactor
	quotaTransactionBound bool
	config                FontRepositoryConfig
	configErr             error
}

func NewFontRepository(queries FontQuerier, configs ...FontRepositoryConfig) *FontRepository {
	cfg, err := normalizeFontRepositoryConfig(configs)
	return &FontRepository{queries: queries, config: cfg, configErr: err}
}

func NewFontRepositoryFromPool(pool *pgxpool.Pool, configs ...FontRepositoryConfig) *FontRepository {
	repository := NewFontRepository(NewQueries(pool), configs...)
	repository.transactor = NewTransactor(pool)
	return repository
}

// NewFontRepositoryFromTx binds quota mutations to an existing real database
// transaction. The caller owns commit or rollback.
func NewFontRepositoryFromTx(tx pgx.Tx, configs ...FontRepositoryConfig) *FontRepository {
	if tx == nil {
		repository := NewFontRepository(nil, configs...)
		repository.configErr = errors.Join(repository.configErr, ErrArtifactQuotaTransactionRequired)
		return repository
	}
	repository := NewFontRepository(NewQueries(tx), configs...)
	repository.quotaTransactionBound = true
	return repository
}

func (r *FontRepository) GetFontFamily(ctx context.Context, id string) (domain.Family, error) {
	if err := r.validate(); err != nil {
		return domain.Family{}, err
	}
	row, err := r.queries.GetFontFamily(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Family{}, domain.ErrFontNotFound
		}
		return domain.Family{}, fmt.Errorf("get font family %q: %w", id, err)
	}
	return familyFromRow(
		row.ID, row.Name, row.NameZh, row.NameEn, row.Weights, row.License, row.Version,
		row.Description, row.Category, row.Family, row.Tags, row.RepoUrl, row.Authors,
		row.Format, row.DemoContentID,
	), nil
}

func (r *FontRepository) ListFontFamilies(
	ctx context.Context,
	search, afterID string,
	limit int,
) ([]domain.Family, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	rows, err := r.queries.ListFontFamilies(ctx, sqlc.ListFontFamiliesParams{
		Search: search, AfterID: afterID, PageLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list font families: %w", err)
	}
	result := make([]domain.Family, 0, len(rows))
	for _, row := range rows {
		result = append(result, familyFromRow(
			row.ID, row.Name, row.NameZh, row.NameEn, row.Weights, row.License, row.Version,
			row.Description, row.Category, row.Family, row.Tags, row.RepoUrl, row.Authors,
			row.Format, row.DemoContentID,
		))
	}
	return result, nil
}

func (r *FontRepository) GetFontSource(ctx context.Context, familyID string, weight int) (domain.Source, error) {
	if err := r.validate(); err != nil {
		return domain.Source{}, err
	}
	row, err := r.queries.GetFontSource(ctx, familyID, int16(weight))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Source{}, domain.ErrSourceNotFound
		}
		return domain.Source{}, fmt.Errorf("get font source %s/%d: %w", familyID, weight, err)
	}
	return domain.Source{
		FamilyID: row.FamilyID, Weight: int(row.Weight), Format: row.Format,
		ObjectKey: row.ObjectKey, ChecksumSHA256: row.ChecksumSha256,
		SizeBytes: row.SizeBytes, SourceVersion: row.SourceVersion,
	}, nil
}

func (r *FontRepository) GetFontArtifact(ctx context.Context, key string) (domain.Artifact, error) {
	if err := r.validate(); err != nil {
		return domain.Artifact{}, err
	}
	row, err := r.queries.GetFontArtifact(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Artifact{}, domain.ErrArtifactNotFound
		}
		return domain.Artifact{}, fmt.Errorf("get font artifact %q: %w", key, err)
	}
	return domain.Artifact{
		Key: row.ArtifactKey, Kind: row.Kind, Status: row.Status, FamilyID: row.FamilyID,
		Weight: int(row.Weight), Version: int(row.Version), Pack: row.Pack, WordHash: row.WordHash,
		NormalizedWordSet: row.NormalizedWordSet, SourceChecksum: row.SourceChecksumSha256,
		BuilderVersion: row.BuilderVersion, ProtocolVersion: row.ArtifactProtocolVersion,
		ObjectKey: row.ObjectKey, ObjectVersionID: row.ObjectVersionID, ContentType: row.ContentType,
		SizeBytes: row.SizeBytes, ETag: row.Etag, ChecksumSHA256: row.ChecksumSha256,
		Generation: row.Generation, FailureCode: row.FailureCode,
	}, nil
}

func (r *FontRepository) CreateFontArtifact(ctx context.Context, artifact domain.Artifact) error {
	if err := r.validate(); err != nil {
		return err
	}
	params := sqlc.CreateFontArtifactParams{
		ArtifactKey: artifact.Key, Kind: artifact.Kind, Status: artifact.Status, FamilyID: artifact.FamilyID,
		Weight: int16(artifact.Weight), Version: artifact.Version, Pack: artifact.Pack,
		WordHash: artifact.WordHash, NormalizedWordSet: artifact.NormalizedWordSet,
		SourceChecksumSha256: artifact.SourceChecksum, BuilderVersion: artifact.BuilderVersion,
		ArtifactProtocolVersion: artifact.ProtocolVersion,
		ObjectKey:               artifact.ObjectKey,
		ContentType:             artifact.ContentType,
		MaxArtifacts:            r.config.MaxArtifacts,
		MaxAccountedBytes:       r.config.MaxAccountedBytes,
		ArtifactReservation:     r.config.ArtifactReservation,
	}
	var result string
	err := r.withinArtifactQuotaTx(ctx, func(operationCtx context.Context, queries FontQuerier) error {
		var err error
		result, err = queries.CreateFontArtifact(operationCtx, params)
		return err
	})
	if err != nil {
		return fmt.Errorf("create font artifact %q: %w", artifact.Key, err)
	}
	switch result {
	case "admitted":
		return nil
	case "capacity":
		return fmt.Errorf("create font artifact %q: %w", artifact.Key, domain.ErrArtifactCapacity)
	case "conflict":
		return fmt.Errorf("create font artifact %q: %w", artifact.Key, domain.ErrArtifactConflict)
	case "terminal":
		return fmt.Errorf("create font artifact %q: %w", artifact.Key, domain.ErrTerminalFailureCached)
	default:
		return fmt.Errorf("create font artifact %q: unexpected admission result %q", artifact.Key, result)
	}
}

func (r *FontRepository) MarkFontArtifactReady(
	ctx context.Context,
	key string,
	claim domain.BuildClaim,
	object domain.ArtifactObject,
) error {
	if err := r.validate(); err != nil {
		return err
	}
	result, err := r.queries.MarkFontArtifactReady(ctx, sqlc.MarkFontArtifactReadyParams{
		ArtifactKey: key, LockedBy: text(claim.Owner), Fence: claim.Fence, ObjectKey: object.ObjectKey,
		ObjectVersionID: object.VersionID, SizeBytes: object.SizeBytes,
		Etag: object.ETag, ChecksumSha256: object.ChecksumSHA256,
	})
	if err != nil {
		return fmt.Errorf("mark font artifact %q ready: %w", key, err)
	}
	switch result {
	case "ready":
		return nil
	case "capacity":
		return fmt.Errorf("mark font artifact %q ready with %d bytes: %w", key, object.SizeBytes, domain.ErrArtifactCapacity)
	case "not_ready":
		return domain.ErrBuildNotReady
	default:
		return fmt.Errorf("mark font artifact %q ready: unexpected result %q", key, result)
	}
}

func (r *FontRepository) MarkFontArtifactMissing(
	ctx context.Context,
	key, objectKey string,
	generation int64,
	reason string,
) (bool, error) {
	if err := r.validate(); err != nil {
		return false, err
	}
	params := sqlc.MarkFontArtifactMissingParams{
		ArtifactKey: key, ObjectKey: objectKey, Generation: generation, Error: text(reason),
		MaxAccountedBytes: r.config.MaxAccountedBytes,
	}
	var result string
	err := r.withinArtifactQuotaTx(ctx, func(operationCtx context.Context, queries FontQuerier) error {
		var err error
		result, err = queries.MarkFontArtifactMissing(operationCtx, params)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("mark font artifact %q missing: %w", key, err)
	}
	switch result {
	case "marked":
		return true, nil
	case "stale":
		return false, nil
	case "capacity":
		return false, fmt.Errorf("mark font artifact %q missing: %w", key, domain.ErrArtifactCapacity)
	default:
		return false, fmt.Errorf("mark font artifact %q missing: unexpected result %q", key, result)
	}
}

func (r *FontRepository) TouchFontArtifact(
	ctx context.Context,
	key, objectKey string,
	generation int64,
	minimumInterval time.Duration,
) (bool, error) {
	if err := r.validate(); err != nil {
		return false, err
	}
	intervalMilliseconds := minimumInterval.Milliseconds()
	if intervalMilliseconds < 1 {
		intervalMilliseconds = 1
	}
	current, err := r.queries.TouchFontArtifact(ctx, sqlc.TouchFontArtifactParams{
		ArtifactKey: key, ObjectKey: objectKey, Generation: generation,
		MinimumIntervalMs: intervalMilliseconds,
	})
	if err != nil {
		return false, fmt.Errorf("touch font artifact %q: %w", key, err)
	}
	return current, nil
}

func (r *FontRepository) GetStaticPackSnapshot(
	ctx context.Context,
	familyID string,
	characters []string,
) ([]domain.StaticPackSnapshot, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	rows, err := r.queries.GetStaticPackSnapshot(ctx, familyID, characters)
	if err != nil {
		return nil, fmt.Errorf("get static pack snapshot: %w", err)
	}
	result := make([]domain.StaticPackSnapshot, len(rows))
	for index, row := range rows {
		result[index] = domain.StaticPackSnapshot{
			Version: int(row.Version), Number: int(row.Pack), Characters: row.Characters,
			CoverageComplete: row.CoverageComplete.Valid && row.CoverageComplete.Bool,
		}
	}
	return result, nil
}

func (r *FontRepository) AcquireBuildJob(
	ctx context.Context,
	artifactKey, leaseOwner string,
	lease time.Duration,
) (domain.BuildClaim, bool, error) {
	if err := r.validate(); err != nil {
		return domain.BuildClaim{}, false, err
	}
	leaseMilliseconds := lease.Milliseconds()
	if leaseMilliseconds < 1 {
		leaseMilliseconds = 1
	}
	params := sqlc.AcquireFontBuildJobParams{
		ArtifactKey:       artifactKey,
		LockedBy:          text(leaseOwner),
		LeaseDurationMs:   leaseMilliseconds,
		MaxArtifacts:      r.config.MaxArtifacts,
		MaxAccountedBytes: r.config.MaxAccountedBytes,
	}
	var fence int64
	err := r.withinArtifactQuotaTx(ctx, func(operationCtx context.Context, queries FontQuerier) error {
		var err error
		fence, err = queries.AcquireFontBuildJob(operationCtx, params)
		return err
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BuildClaim{}, false, nil
		}
		return domain.BuildClaim{}, false, fmt.Errorf("acquire font build job %q: %w", artifactKey, err)
	}
	return domain.BuildClaim{Owner: leaseOwner, Fence: fence}, true, nil
}

func (r *FontRepository) BuildRetryAfter(ctx context.Context, artifactKey string) (time.Duration, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	seconds, err := r.queries.GetFontBuildRetryAfterSeconds(ctx, artifactKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Second, nil
		}
		return 0, fmt.Errorf("get font build retry delay %q: %w", artifactKey, err)
	}
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second, nil
}

func (r *FontRepository) FailBuildJob(
	ctx context.Context,
	artifactKey string,
	claim domain.BuildClaim,
	reason string,
) error {
	if err := r.validate(); err != nil {
		return err
	}
	rows, err := r.queries.FailFontBuildJob(ctx, sqlc.FailFontBuildJobParams{
		ArtifactKey: artifactKey, LockedBy: text(claim.Owner), Fence: claim.Fence, Error: text(reason),
	})
	if err != nil {
		return fmt.Errorf("fail font build job %q: %w", artifactKey, err)
	}
	if rows != 1 {
		return domain.ErrBuildNotReady
	}
	return nil
}

func (r *FontRepository) FailBuildJobTerminal(
	ctx context.Context,
	artifactKey string,
	claim domain.BuildClaim,
	failureCode, reason string,
) error {
	if err := r.validate(); err != nil {
		return err
	}
	params := sqlc.FailFontBuildJobTerminalParams{
		ArtifactKey: artifactKey, LockedBy: text(claim.Owner), Fence: claim.Fence,
		FailureCode: failureCode, Error: text(reason), MaxTerminalFailures: r.config.MaxTerminalFailures,
	}
	var rows int64
	err := r.withinArtifactQuotaTx(ctx, func(operationCtx context.Context, queries FontQuerier) error {
		var err error
		rows, err = queries.FailFontBuildJobTerminal(operationCtx, params)
		return err
	})
	if err != nil {
		return fmt.Errorf("record terminal font build failure %q: %w", artifactKey, err)
	}
	if rows != 1 {
		return domain.ErrBuildNotReady
	}
	return nil
}

func (r *FontRepository) RetireFontArtifacts(
	ctx context.Context,
	inactiveBefore, retiredAt time.Time,
	batchSize int32,
) (int64, error) {
	queries, err := r.cleanupQueries()
	if err != nil {
		return 0, err
	}
	if inactiveBefore.IsZero() || retiredAt.IsZero() {
		return 0, errors.New("artifact retirement timestamps are required")
	}
	if batchSize <= 0 {
		return 0, errors.New("artifact retirement batch size must be greater than zero")
	}
	rows, err := queries.RetireFontArtifacts(ctx, sqlc.RetireFontArtifactsParams{
		InactiveBefore: timestamp(inactiveBefore),
		RetiredAt:      timestamp(retiredAt),
		BatchSize:      batchSize,
	})
	if err != nil {
		return 0, fmt.Errorf("retire font artifacts inactive before %s: %w", inactiveBefore.UTC().Format(time.RFC3339), err)
	}
	return rows, nil
}

func (r *FontRepository) DeleteRetiredFontArtifacts(
	ctx context.Context,
	retiredBefore time.Time,
	batchSize int32,
) (int64, error) {
	queries, err := r.cleanupQueries()
	if err != nil {
		return 0, err
	}
	if retiredBefore.IsZero() {
		return 0, errors.New("artifact deletion cutoff is required")
	}
	if batchSize <= 0 {
		return 0, errors.New("artifact deletion batch size must be greater than zero")
	}
	rows, err := queries.DeleteRetiredFontArtifacts(ctx, timestamp(retiredBefore), batchSize)
	if err != nil {
		return 0, fmt.Errorf("delete font artifacts retired before %s: %w", retiredBefore.UTC().Format(time.RFC3339), err)
	}
	return rows, nil
}

func (r *FontRepository) FindReferencedFontObjectKeys(ctx context.Context, objectKeys []string) ([]string, error) {
	queries, err := r.cleanupQueries()
	if err != nil {
		return nil, err
	}
	if len(objectKeys) == 0 {
		return []string{}, nil
	}
	references, err := queries.FindReferencedFontObjectKeys(ctx, objectKeys)
	if err != nil {
		return nil, fmt.Errorf("find referenced font object keys: %w", err)
	}
	return references, nil
}

func (r *FontRepository) validate() error {
	if r == nil {
		return errors.New("font repository is not configured")
	}
	if r.configErr != nil {
		return r.configErr
	}
	if r.queries == nil {
		return errors.New("font repository is not configured")
	}
	return nil
}

func (r *FontRepository) withinArtifactQuotaTx(
	ctx context.Context,
	operation func(context.Context, FontQuerier) error,
) error {
	run := func(operationCtx context.Context, queries FontQuerier) error {
		if err := queries.LockFontArtifactAdmission(operationCtx); err != nil {
			return fmt.Errorf("lock font artifact quota: %w", err)
		}
		return operation(operationCtx, queries)
	}
	if r.transactor == nil {
		if !r.quotaTransactionBound {
			return ErrArtifactQuotaTransactionRequired
		}
		return run(ctx, r.queries)
	}
	return r.transactor.WithinTx(ctx, pgx.TxOptions{}, func(txCtx context.Context, tx pgx.Tx) error {
		return run(txCtx, NewQueries(tx))
	})
}

func normalizeFontRepositoryConfig(configs []FontRepositoryConfig) (FontRepositoryConfig, error) {
	cfg := FontRepositoryConfig{
		MaxArtifacts: defaultMaxArtifacts, MaxAccountedBytes: defaultMaxAccountedBytes,
		ArtifactReservation: defaultArtifactReservation, MaxTerminalFailures: defaultMaxTerminalFailures,
	}
	if len(configs) > 0 {
		configured := configs[len(configs)-1]
		if configured.MaxArtifacts != 0 {
			cfg.MaxArtifacts = configured.MaxArtifacts
		}
		if configured.MaxAccountedBytes != 0 {
			cfg.MaxAccountedBytes = configured.MaxAccountedBytes
		}
		if configured.ArtifactReservation != 0 {
			cfg.ArtifactReservation = configured.ArtifactReservation
		}
		if configured.MaxTerminalFailures != 0 {
			cfg.MaxTerminalFailures = configured.MaxTerminalFailures
		}
	}
	var errs []error
	if cfg.MaxArtifacts <= 0 {
		errs = append(errs, errors.New("font artifact maximum must be greater than zero"))
	}
	if cfg.MaxAccountedBytes <= 0 {
		errs = append(errs, errors.New("font artifact accounted byte maximum must be greater than zero"))
	}
	if cfg.ArtifactReservation <= 0 {
		errs = append(errs, errors.New("font artifact byte reservation must be greater than zero"))
	}
	if cfg.MaxTerminalFailures <= 0 {
		errs = append(errs, errors.New("font terminal failure maximum must be greater than zero"))
	}
	if cfg.MaxAccountedBytes > 0 && cfg.ArtifactReservation > cfg.MaxAccountedBytes {
		errs = append(errs, errors.New("font artifact accounted byte maximum must cover one artifact reservation"))
	}
	return cfg, errors.Join(errs...)
}

func (r *FontRepository) cleanupQueries() (fontCleanupQuerier, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	queries, ok := r.queries.(fontCleanupQuerier)
	if !ok {
		return nil, errors.New("font cleanup queries are not configured")
	}
	return queries, nil
}

func familyFromRow(
	id, name, nameZH, nameEN string,
	weights []int16,
	license, version, description, category, family string,
	tags []string,
	repoURL string,
	authors []string,
	format string,
	demoContentID int32,
) domain.Family {
	convertedWeights := make([]int, len(weights))
	for index, weight := range weights {
		convertedWeights[index] = int(weight)
	}
	return domain.Family{
		ID: id, Name: name, NameZH: nameZH, NameEN: nameEN, Weights: convertedWeights,
		License: license, Version: version, Description: description, Category: category,
		Family: family, Tags: append([]string(nil), tags...), RepoURL: repoURL,
		Authors: append([]string(nil), authors...), Format: format, DemoContentID: int(demoContentID),
	}
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}
