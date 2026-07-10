package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domain "github.com/emfont/emfont/backend/internal/domain/system"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrSystemMetadataNotFound = errors.New("system metadata not found")
	ErrInvalidMetadataKey     = errors.New("invalid metadata key")
)

const systemMetadataPrefix = "system:"

// SystemMetadata is the repository-facing shape for system metadata.
type SystemMetadata struct {
	Key   string
	Value string
}

type storedSystem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Version     string `json:"version"`
	Revision    string `json:"revision,omitempty"`
	Status      string `json:"status"`
}

// SystemMetadataQuerier is implemented by the sqlc generated *Queries type.
type SystemMetadataQuerier interface {
	GetSystemMetadataValue(ctx context.Context, metadataKey string) (string, error)
	SetSystemMetadataValue(ctx context.Context, metadataKey string, metadataValue string) error
	DeleteSystemMetadata(ctx context.Context, metadataKey string) error
}

// SystemRepository wraps generated sqlc calls without exposing generated row types.
type SystemRepository struct {
	queries SystemMetadataQuerier
}

func NewSystemRepository(queries SystemMetadataQuerier) *SystemRepository {
	return &SystemRepository{queries: queries}
}

func NewSystemRepositoryFromPool(pool *pgxpool.Pool) *SystemRepository {
	return NewSystemRepository(NewQueries(pool))
}

func (r *SystemRepository) Get(ctx context.Context, key string) (SystemMetadata, error) {
	if err := ValidateMetadataKey(key); err != nil {
		return SystemMetadata{}, err
	}
	if r == nil || r.queries == nil {
		return SystemMetadata{}, errors.New("system repository is not configured")
	}

	value, err := r.queries.GetSystemMetadataValue(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SystemMetadata{}, fmt.Errorf("%w: %s", ErrSystemMetadataNotFound, key)
		}
		return SystemMetadata{}, fmt.Errorf("get system metadata %q: %w", key, err)
	}

	return SystemMetadata{
		Key:   key,
		Value: value,
	}, nil
}

func (r *SystemRepository) Set(ctx context.Context, metadata SystemMetadata) error {
	if err := ValidateMetadataKey(metadata.Key); err != nil {
		return err
	}
	if r == nil || r.queries == nil {
		return errors.New("system repository is not configured")
	}

	if err := r.queries.SetSystemMetadataValue(ctx, metadata.Key, metadata.Value); err != nil {
		return fmt.Errorf("set system metadata %q: %w", metadata.Key, err)
	}
	return nil
}

func (r *SystemRepository) Delete(ctx context.Context, key string) error {
	if err := ValidateMetadataKey(key); err != nil {
		return err
	}
	if r == nil || r.queries == nil {
		return errors.New("system repository is not configured")
	}

	if err := r.queries.DeleteSystemMetadata(ctx, key); err != nil {
		return fmt.Errorf("delete system metadata %q: %w", key, err)
	}
	return nil
}

func (r *SystemRepository) GetSystem(ctx context.Context, id domain.ID) (domain.System, error) {
	metadata, err := r.Get(ctx, systemMetadataKey(id))
	if err != nil {
		if errors.Is(err, ErrSystemMetadataNotFound) {
			return domain.System{}, domain.ErrSystemNotFound
		}
		return domain.System{}, err
	}

	var stored storedSystem
	if err := json.Unmarshal([]byte(metadata.Value), &stored); err != nil {
		return domain.System{}, fmt.Errorf("decode system metadata %q: %w", id, err)
	}

	system, err := domain.VN(domain.VNInput{
		ID:          stored.ID,
		Name:        stored.Name,
		Environment: stored.Environment,
		Version:     stored.Version,
		Revision:    stored.Revision,
		Status:      stored.Status,
	})
	if err != nil {
		return domain.System{}, fmt.Errorf("decode system metadata %q: %w", id, err)
	}

	return system, nil
}

func (r *SystemRepository) UpsertSystem(ctx context.Context, system domain.System) error {
	value, err := json.Marshal(storedSystem{
		ID:          system.ID().String(),
		Name:        system.Name(),
		Environment: system.Environment().String(),
		Version:     system.Version(),
		Revision:    system.Revision(),
		Status:      system.Status().String(),
	})
	if err != nil {
		return fmt.Errorf("encode system metadata %q: %w", system.ID(), err)
	}

	return r.Set(ctx, SystemMetadata{
		Key:   systemMetadataKey(system.ID()),
		Value: string(value),
	})
}

func systemMetadataKey(id domain.ID) string {
	return systemMetadataPrefix + id.String()
}
