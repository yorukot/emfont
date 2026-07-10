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
	ListFontFamilies(context.Context, string) ([]sqlc.ListFontFamiliesRow, error)
	GetFontSource(context.Context, string, int16) (sqlc.GetFontSourceRow, error)
	GetFontArtifact(context.Context, string) (sqlc.GetFontArtifactRow, error)
	CreateFontArtifact(context.Context, sqlc.CreateFontArtifactParams) error
	MarkFontArtifactReady(context.Context, sqlc.MarkFontArtifactReadyParams) (int64, error)
	MarkFontArtifactMissing(context.Context, string, pgtype.Text) error
	MarkFontArtifactFailed(context.Context, sqlc.MarkFontArtifactFailedParams) (int64, error)
	GetCurrentStaticVersion(context.Context) (int32, error)
	FindStaticPacks(context.Context, string, []string) ([]int16, error)
	GetStaticPackCharacters(context.Context, int16, string) (string, error)
	AcquireFontBuildJob(context.Context, sqlc.AcquireFontBuildJobParams) (bool, error)
	CompleteFontBuildJob(context.Context, string, pgtype.Text) (int64, error)
	FailFontBuildJob(context.Context, sqlc.FailFontBuildJobParams) (int64, error)
}

type FontRepository struct {
	queries FontQuerier
}

func NewFontRepository(queries FontQuerier) *FontRepository {
	return &FontRepository{queries: queries}
}

func NewFontRepositoryFromPool(pool *pgxpool.Pool) *FontRepository {
	return NewFontRepository(NewQueries(pool))
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

func (r *FontRepository) ListFontFamilies(ctx context.Context, search string) ([]domain.Family, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	rows, err := r.queries.ListFontFamilies(ctx, search)
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
		BuilderVersion: row.BuilderVersion, ObjectKey: row.ObjectKey, ContentType: row.ContentType,
		SizeBytes: row.SizeBytes, ETag: row.Etag, ChecksumSHA256: row.ChecksumSha256,
	}, nil
}

func (r *FontRepository) CreateFontArtifact(ctx context.Context, artifact domain.Artifact) error {
	if err := r.validate(); err != nil {
		return err
	}
	err := r.queries.CreateFontArtifact(ctx, sqlc.CreateFontArtifactParams{
		ArtifactKey: artifact.Key, Kind: artifact.Kind, Status: artifact.Status, FamilyID: artifact.FamilyID,
		Weight: int16(artifact.Weight), Column6: artifact.Version, Column7: artifact.Pack,
		Column8: artifact.WordHash, Column9: artifact.NormalizedWordSet, Column10: artifact.SourceChecksum,
		BuilderVersion: artifact.BuilderVersion, ObjectKey: artifact.ObjectKey, ContentType: artifact.ContentType,
	})
	if err != nil {
		return fmt.Errorf("create font artifact %q: %w", artifact.Key, err)
	}
	return nil
}

func (r *FontRepository) MarkFontArtifactReady(ctx context.Context, key, leaseOwner string, object domain.ArtifactObject) error {
	if err := r.validate(); err != nil {
		return err
	}
	rows, err := r.queries.MarkFontArtifactReady(ctx, sqlc.MarkFontArtifactReadyParams{
		ArtifactKey: key, LockedBy: text(leaseOwner), SizeBytes: object.SizeBytes,
		Column4: object.ETag, Column5: object.ChecksumSHA256,
	})
	if err != nil {
		return fmt.Errorf("mark font artifact %q ready: %w", key, err)
	}
	if rows != 1 {
		return domain.ErrBuildNotReady
	}
	return nil
}

func (r *FontRepository) MarkFontArtifactMissing(ctx context.Context, key, reason string) error {
	if err := r.validate(); err != nil {
		return err
	}
	if err := r.queries.MarkFontArtifactMissing(ctx, key, text(reason)); err != nil {
		return fmt.Errorf("mark font artifact %q missing: %w", key, err)
	}
	return nil
}

func (r *FontRepository) MarkFontArtifactFailed(ctx context.Context, key, leaseOwner, reason string) error {
	if err := r.validate(); err != nil {
		return err
	}
	rows, err := r.queries.MarkFontArtifactFailed(ctx, sqlc.MarkFontArtifactFailedParams{
		ArtifactKey: key, LockedBy: text(leaseOwner), Error: text(reason),
	})
	if err != nil {
		return fmt.Errorf("mark font artifact %q failed: %w", key, err)
	}
	if rows != 1 {
		return domain.ErrBuildNotReady
	}
	return nil
}

func (r *FontRepository) CurrentStaticVersion(ctx context.Context) (int, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	version, err := r.queries.GetCurrentStaticVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("get current static version: %w", err)
	}
	return int(version), nil
}

func (r *FontRepository) FindStaticPacks(ctx context.Context, familyID string, characters []string) ([]int, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	packs, err := r.queries.FindStaticPacks(ctx, familyID, characters)
	if err != nil {
		return nil, fmt.Errorf("find static packs: %w", err)
	}
	result := make([]int, len(packs))
	for index, pack := range packs {
		result[index] = int(pack)
	}
	return result, nil
}

func (r *FontRepository) GetStaticPackCharacters(ctx context.Context, familyID string, pack int) (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	characters, err := r.queries.GetStaticPackCharacters(ctx, int16(pack), familyID)
	if err != nil {
		return "", fmt.Errorf("get static pack %d characters: %w", pack, err)
	}
	return characters, nil
}

func (r *FontRepository) AcquireBuildJob(ctx context.Context, artifactKey, leaseOwner string, lease time.Duration) (bool, error) {
	if err := r.validate(); err != nil {
		return false, err
	}
	leaseMilliseconds := lease.Milliseconds()
	if leaseMilliseconds < 1 {
		leaseMilliseconds = 1
	}
	acquired, err := r.queries.AcquireFontBuildJob(ctx, sqlc.AcquireFontBuildJobParams{
		ArtifactKey: artifactKey,
		LockedBy:    text(leaseOwner),
		Column3:     leaseMilliseconds,
	})
	if err != nil {
		return false, fmt.Errorf("acquire font build job %q: %w", artifactKey, err)
	}
	return acquired, nil
}

func (r *FontRepository) CompleteBuildJob(ctx context.Context, artifactKey, workerID string) error {
	if err := r.validate(); err != nil {
		return err
	}
	rows, err := r.queries.CompleteFontBuildJob(ctx, artifactKey, text(workerID))
	if err != nil {
		return fmt.Errorf("complete font build job %q: %w", artifactKey, err)
	}
	if rows != 1 {
		return domain.ErrBuildNotReady
	}
	return nil
}

func (r *FontRepository) FailBuildJob(ctx context.Context, artifactKey, workerID, reason string) error {
	if err := r.validate(); err != nil {
		return err
	}
	rows, err := r.queries.FailFontBuildJob(ctx, sqlc.FailFontBuildJobParams{
		ArtifactKey: artifactKey, LockedBy: text(workerID), Error: text(reason),
	})
	if err != nil {
		return fmt.Errorf("fail font build job %q: %w", artifactKey, err)
	}
	if rows != 1 {
		return domain.ErrBuildNotReady
	}
	return nil
}

func (r *FontRepository) validate() error {
	if r == nil || r.queries == nil {
		return errors.New("font repository is not configured")
	}
	return nil
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
