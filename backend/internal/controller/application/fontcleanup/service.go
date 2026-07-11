package fontcleanup

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	GeneratedObjectPrefix    = "_generated/"
	DefaultDatabaseBatchSize = 100
	DefaultMaxDatabaseRows   = 1_000
	DefaultObjectPageSize    = 500
	DefaultMaxObjectPages    = 20
	maxObjectPageSize        = 1_000
	minimumOrphanGrace       = time.Minute
)

var (
	ErrInvalidConfiguration = errors.New("invalid font cleanup configuration")
	ErrObjectNotFound       = errors.New("font cleanup object not found")
	ErrObjectChanged        = errors.New("font cleanup object changed after listing")
)

type Config struct {
	ArtifactRetention time.Duration
	RetirementGrace   time.Duration
	OrphanGrace       time.Duration

	DatabaseBatchSize int
	MaxDatabaseRows   int
	ObjectPageSize    int
	MaxObjectPages    int
}

type Report struct {
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"completedAt"`

	ArtifactsRetired       int64 `json:"artifactsRetired"`
	ArtifactRowsDeleted    int64 `json:"artifactRowsDeleted"`
	ObjectPagesScanned     int   `json:"objectPagesScanned"`
	ObjectsScanned         int   `json:"objectsScanned"`
	ObjectsReferenced      int   `json:"objectsReferenced"`
	ObjectsTooYoung        int   `json:"objectsTooYoung"`
	ObjectsWithUnknownAge  int   `json:"objectsWithUnknownAge"`
	ObjectsChanged         int   `json:"objectsChanged"`
	ObjectsDeleted         int   `json:"objectsDeleted"`
	ObjectsAlreadyMissing  int   `json:"objectsAlreadyMissing"`
	ObjectDeletionFailures int   `json:"objectDeletionFailures"`

	RetirementLimitReached  bool `json:"retirementLimitReached"`
	RowDeletionLimitReached bool `json:"rowDeletionLimitReached"`
	ObjectPageLimitReached  bool `json:"objectPageLimitReached"`
}

type Option func(*Service) error

func WithClock(clock func() time.Time) Option {
	return func(service *Service) error {
		if clock == nil {
			return fmt.Errorf("%w: clock is required", ErrInvalidConfiguration)
		}
		service.clock = clock
		return nil
	}
}

type Service struct {
	repository Repository
	objects    ObjectStore
	config     Config
	clock      func() time.Time
}

func NewService(repository Repository, objects ObjectStore, cfg Config, options ...Option) (*Service, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: repository is required", ErrInvalidConfiguration)
	}
	if objects == nil {
		return nil, fmt.Errorf("%w: object store is required", ErrInvalidConfiguration)
	}
	if err := applyDefaultsAndValidate(&cfg); err != nil {
		return nil, err
	}
	service := &Service{repository: repository, objects: objects, config: cfg, clock: time.Now}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: option is required", ErrInvalidConfiguration)
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (s *Service) Run(ctx context.Context) (report Report, runErr error) {
	if s == nil || s.repository == nil || s.objects == nil || s.clock == nil {
		return report, fmt.Errorf("%w: service is not configured", ErrInvalidConfiguration)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	now := s.clock().UTC()
	report.StartedAt = now
	defer func() { report.CompletedAt = s.clock().UTC() }()

	var phaseErrors []error
	retired, limitReached, err := runDatabaseBatches(ctx, s.config, func(batchSize int32) (int64, error) {
		return s.repository.RetireFontArtifacts(ctx, now.Add(-s.config.ArtifactRetention), now, batchSize)
	})
	report.ArtifactsRetired = retired
	report.RetirementLimitReached = limitReached
	if err != nil {
		phaseErrors = append(phaseErrors, fmt.Errorf("retire font artifacts: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return report, errors.Join(append(phaseErrors, err)...)
	}

	deletedRows, limitReached, err := runDatabaseBatches(ctx, s.config, func(batchSize int32) (int64, error) {
		return s.repository.DeleteRetiredFontArtifacts(ctx, now.Add(-s.config.RetirementGrace), batchSize)
	})
	report.ArtifactRowsDeleted = deletedRows
	report.RowDeletionLimitReached = limitReached
	if err != nil {
		phaseErrors = append(phaseErrors, fmt.Errorf("delete retired font artifacts: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return report, errors.Join(append(phaseErrors, err)...)
	}

	objectErrors := s.cleanupOrphanObjects(ctx, now.Add(-s.config.OrphanGrace), &report)
	phaseErrors = append(phaseErrors, objectErrors...)
	if err := ctx.Err(); err != nil && !errors.Is(errors.Join(phaseErrors...), err) {
		phaseErrors = append(phaseErrors, err)
	}
	return report, errors.Join(phaseErrors...)
}

func runDatabaseBatches(
	ctx context.Context,
	cfg Config,
	operation func(int32) (int64, error),
) (total int64, limitReached bool, err error) {
	maximum := int64(cfg.MaxDatabaseRows)
	for total < maximum {
		if err := ctx.Err(); err != nil {
			return total, false, err
		}
		batchSize := int64(cfg.DatabaseBatchSize)
		if remaining := maximum - total; remaining < batchSize {
			batchSize = remaining
		}
		rows, err := operation(int32(batchSize))
		if err != nil {
			return total, false, err
		}
		if rows < 0 || rows > batchSize {
			return total, false, fmt.Errorf("repository returned %d rows for batch size %d", rows, batchSize)
		}
		total += rows
		if rows < batchSize {
			return total, false, nil
		}
	}
	return total, true, nil
}

func (s *Service) cleanupOrphanObjects(ctx context.Context, olderThan time.Time, report *Report) []error {
	cursor := ""
	var collected []error
	for pageNumber := 0; pageNumber < s.config.MaxObjectPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return append(collected, err)
		}
		page, err := s.objects.ListObjects(ctx, GeneratedObjectPrefix, cursor, s.config.ObjectPageSize)
		if err != nil {
			return append(collected, fmt.Errorf("list generated objects after %q: %w", cursor, err))
		}
		report.ObjectPagesScanned++
		if err := validateObjectPage(page, cursor, s.config.ObjectPageSize); err != nil {
			return append(collected, err)
		}
		report.ObjectsScanned += len(page.Objects)

		candidates := make([]Object, 0, len(page.Objects))
		candidateKeys := make([]string, 0, len(page.Objects))
		for _, object := range page.Objects {
			switch {
			case object.LastModified.IsZero():
				report.ObjectsWithUnknownAge++
			case !object.LastModified.Before(olderThan):
				report.ObjectsTooYoung++
			default:
				candidates = append(candidates, object)
				candidateKeys = append(candidateKeys, object.Key)
			}
		}

		if len(candidates) > 0 {
			referencedKeys, err := s.repository.FindReferencedFontObjectKeys(ctx, candidateKeys)
			if err != nil {
				return append(collected, fmt.Errorf("find referenced generated objects: %w", err))
			}
			referenced, err := referenceSet(candidateKeys, referencedKeys)
			if err != nil {
				return append(collected, err)
			}
			collected = append(collected, s.deleteUnreferenced(ctx, candidates, referenced, report)...)
			if ctx.Err() != nil {
				return collected
			}
		}

		if !page.HasMore {
			return collected
		}
		cursor = page.NextCursor
	}
	report.ObjectPageLimitReached = true
	return collected
}

func (s *Service) deleteUnreferenced(
	ctx context.Context,
	candidates []Object,
	referenced map[string]struct{},
	report *Report,
) []error {
	var deleteErrors []error
	for _, object := range candidates {
		if _, ok := referenced[object.Key]; ok {
			report.ObjectsReferenced++
			continue
		}
		if err := ctx.Err(); err != nil {
			return append(deleteErrors, err)
		}
		if err := s.objects.DeleteObject(ctx, object); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return append(deleteErrors, contextErr)
			}
			if errors.Is(err, ErrObjectNotFound) {
				report.ObjectsAlreadyMissing++
				continue
			}
			if errors.Is(err, ErrObjectChanged) {
				report.ObjectsChanged++
				continue
			}
			report.ObjectDeletionFailures++
			deleteErrors = append(deleteErrors, fmt.Errorf("delete generated object %q: %w", object.Key, err))
			continue
		}
		report.ObjectsDeleted++
	}
	return deleteErrors
}

func validateObjectPage(page ObjectPage, cursor string, limit int) error {
	if len(page.Objects) > limit {
		return fmt.Errorf("object store returned %d objects for page size %d", len(page.Objects), limit)
	}
	previous := cursor
	seen := make(map[string]struct{}, len(page.Objects))
	for _, object := range page.Objects {
		if !strings.HasPrefix(object.Key, GeneratedObjectPrefix) {
			return fmt.Errorf("object store returned key %q outside prefix %q", object.Key, GeneratedObjectPrefix)
		}
		if object.Key <= previous {
			return fmt.Errorf("object store returned non-increasing key %q after %q", object.Key, previous)
		}
		if _, exists := seen[object.Key]; exists {
			return fmt.Errorf("object store returned duplicate key %q", object.Key)
		}
		seen[object.Key] = struct{}{}
		previous = object.Key
	}
	if page.HasMore {
		if len(page.Objects) == 0 {
			return errors.New("object store returned an empty truncated page")
		}
		if page.NextCursor != previous {
			return fmt.Errorf("object store next cursor %q does not match last key %q", page.NextCursor, previous)
		}
	}
	return nil
}

func referenceSet(requested, referenced []string) (map[string]struct{}, error) {
	requestedSet := make(map[string]struct{}, len(requested))
	for _, key := range requested {
		requestedSet[key] = struct{}{}
	}
	result := make(map[string]struct{}, len(referenced))
	for _, key := range referenced {
		if _, ok := requestedSet[key]; !ok {
			return nil, fmt.Errorf("repository returned unexpected object reference %q", key)
		}
		result[key] = struct{}{}
	}
	return result, nil
}

func applyDefaultsAndValidate(cfg *Config) error {
	if cfg.ArtifactRetention <= 0 {
		return fmt.Errorf("%w: artifact retention must be greater than zero", ErrInvalidConfiguration)
	}
	if cfg.RetirementGrace <= 0 {
		return fmt.Errorf("%w: retirement grace must be greater than zero", ErrInvalidConfiguration)
	}
	if cfg.OrphanGrace < minimumOrphanGrace {
		return fmt.Errorf("%w: orphan grace must be at least %s", ErrInvalidConfiguration, minimumOrphanGrace)
	}
	if cfg.DatabaseBatchSize < 0 || cfg.MaxDatabaseRows < 0 || cfg.ObjectPageSize < 0 || cfg.MaxObjectPages < 0 {
		return fmt.Errorf("%w: cleanup bounds must not be negative", ErrInvalidConfiguration)
	}
	if cfg.DatabaseBatchSize == 0 {
		cfg.DatabaseBatchSize = DefaultDatabaseBatchSize
	}
	if cfg.MaxDatabaseRows == 0 {
		cfg.MaxDatabaseRows = DefaultMaxDatabaseRows
	}
	if cfg.ObjectPageSize == 0 {
		cfg.ObjectPageSize = DefaultObjectPageSize
	}
	if cfg.MaxObjectPages == 0 {
		cfg.MaxObjectPages = DefaultMaxObjectPages
	}
	if cfg.DatabaseBatchSize > math.MaxInt32 {
		return fmt.Errorf("%w: database batch size exceeds %d", ErrInvalidConfiguration, math.MaxInt32)
	}
	if cfg.ObjectPageSize > maxObjectPageSize {
		return fmt.Errorf("%w: object page size exceeds %d", ErrInvalidConfiguration, maxObjectPageSize)
	}
	return nil
}
