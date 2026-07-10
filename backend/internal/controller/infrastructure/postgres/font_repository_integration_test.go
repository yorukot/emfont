package postgres

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	domain "github.com/emfont/emfont/backend/internal/domain/font"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFontRepositoryBuildLeaseIntegration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer pool.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "integration-" + suffix
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "Integration "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_sources (family_id, weight, format, object_key, checksum_sha256)
		VALUES ($1, 400, 'ttf', $2, 'source-checksum')`, familyID, "original-fonts/"+familyID+"/400.ttf"); err != nil {
		t.Fatalf("insert font source: %v", err)
	}

	repository := NewFontRepositoryFromPool(pool)
	family, err := repository.GetFontFamily(ctx, familyID)
	if err != nil {
		t.Fatalf("GetFontFamily: %v", err)
	}
	if len(family.Weights) != 1 || family.Weights[0] != 400 {
		t.Fatalf("family weights = %#v", family.Weights)
	}
	source, err := repository.GetFontSource(ctx, familyID, 400)
	if err != nil {
		t.Fatalf("GetFontSource: %v", err)
	}

	artifact := domain.Artifact{
		Key: "dynamic:" + suffix, Kind: domain.BuildModeDynamic, Status: "pending",
		FamilyID: familyID, Weight: 400, WordHash: suffix, NormalizedWordSet: "AB",
		SourceChecksum: source.ChecksumSHA256, BuilderVersion: domain.DefaultBuilderVersion,
		ObjectKey: "_generated/" + suffix + ".woff2", ContentType: domain.ContentTypeWOFF2,
	}
	if err := repository.CreateFontArtifact(ctx, artifact); err != nil {
		t.Fatalf("CreateFontArtifact: %v", err)
	}
	acquired, err := repository.AcquireBuildJob(ctx, artifact.Key, "worker-a:lease-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first AcquireBuildJob = %v, %v; want true, nil", acquired, err)
	}
	acquired, err = repository.AcquireBuildJob(ctx, artifact.Key, "worker-b:lease-2", time.Minute)
	if err != nil || acquired {
		t.Fatalf("second AcquireBuildJob = %v, %v; want false, nil", acquired, err)
	}
	if err := repository.MarkFontArtifactReady(ctx, artifact.Key, "worker-b:lease-2", domain.ArtifactObject{SizeBytes: 1}); !errors.Is(err, domain.ErrBuildNotReady) {
		t.Fatalf("wrong-owner MarkFontArtifactReady error = %v, want ErrBuildNotReady", err)
	}
	if err := repository.MarkFontArtifactReady(ctx, artifact.Key, "worker-a:lease-1", domain.ArtifactObject{
		SizeBytes: 123, ETag: "etag", ChecksumSHA256: "artifact-checksum",
	}); err != nil {
		t.Fatalf("MarkFontArtifactReady: %v", err)
	}
	if err := repository.CompleteBuildJob(ctx, artifact.Key, "worker-a:lease-1"); err != nil {
		t.Fatalf("CompleteBuildJob: %v", err)
	}
	stored, err := repository.GetFontArtifact(ctx, artifact.Key)
	if err != nil {
		t.Fatalf("GetFontArtifact: %v", err)
	}
	if stored.Status != "ready" || stored.SizeBytes != 123 {
		t.Fatalf("stored artifact = %#v", stored)
	}
	if err := repository.MarkFontArtifactMissing(ctx, artifact.Key, "integration repair"); err != nil {
		t.Fatalf("MarkFontArtifactMissing: %v", err)
	}
	acquired, err = repository.AcquireBuildJob(ctx, artifact.Key, "worker-c:repair", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("repair AcquireBuildJob = %v, %v; want true, nil", acquired, err)
	}
}
