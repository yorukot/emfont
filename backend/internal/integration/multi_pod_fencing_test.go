package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	miniostore "github.com/emfont/emfont/backend/internal/controller/infrastructure/objectstore/minio"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/postgres"
	domainfont "github.com/emfont/emfont/backend/internal/domain/font"
	"github.com/jackc/pgx/v5/pgxpool"
	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestLatePodCannotOverwriteTakeoverPublication(t *testing.T) {
	databaseURL := requiredEnv(t, "EMFONT_TEST_DATABASE_URL")
	minioEndpoint := requiredEnv(t, "EMFONT_TEST_MINIO_ENDPOINT")
	accessKey := envOrDefault("EMFONT_TEST_MINIO_ACCESS_KEY", "minioadmin")
	secretKey := envOrDefault("EMFONT_TEST_MINIO_SECRET_KEY", "minioadmin")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "FencingFont" + suffix
	bucket := "emfont-fencing-" + strings.ToLower(suffix)
	sourceKey := domainfont.OriginalObjectKey(familyID, 400, "ttf")
	sourceBytes := []byte("source-font-for-fencing")
	sourceSum := sha256.Sum256(sourceBytes)
	sourceChecksum := hex.EncodeToString(sourceSum[:])

	minioClient, err := miniogo.New(minioEndpoint, &miniogo.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: false,
	})
	if err != nil {
		t.Fatalf("create MinIO client: %v", err)
	}
	if err := minioClient.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("create MinIO bucket: %v", err)
	}
	if err := minioClient.EnableVersioning(ctx, bucket); err != nil {
		t.Fatalf("enable MinIO bucket versioning: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for object := range minioClient.ListObjects(cleanupCtx, bucket, miniogo.ListObjectsOptions{Recursive: true, WithVersions: true}) {
			if object.Err == nil {
				_ = minioClient.RemoveObject(cleanupCtx, bucket, object.Key, miniogo.RemoveObjectOptions{VersionID: object.VersionID})
			}
		}
		_ = minioClient.RemoveBucket(cleanupCtx, bucket)
	})
	if _, err := minioClient.PutObject(
		ctx,
		bucket,
		sourceKey,
		bytes.NewReader(sourceBytes),
		int64(len(sourceBytes)),
		miniogo.PutObjectOptions{ContentType: "font/ttf", UserMetadata: map[string]string{"sha256": sourceChecksum}},
	); err != nil {
		t.Fatalf("upload source font: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, version, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'fencing-v1', 'ttf')`,
		familyID, "Fencing Font "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("cleanup font family: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_sources (family_id, weight, format, object_key, checksum_sha256, size_bytes)
		VALUES ($1, 400, 'ttf', $2, $3, $4)`,
		familyID, sourceKey, sourceChecksum, len(sourceBytes)); err != nil {
		t.Fatalf("insert font source: %v", err)
	}

	objects, err := miniostore.New(miniostore.Config{
		Endpoint: minioEndpoint, AccessKey: accessKey, SecretKey: secretKey,
		Bucket: bucket, PresignExpiry: time.Minute,
	})
	if err != nil {
		t.Fatalf("create object store: %v", err)
	}
	repository := postgres.NewFontRepositoryFromPool(pool)
	sharedOutput := []byte("wOF2-shared-output")
	barrierObjects := newPublicationBarrierStore(objects)
	podA, err := appfont.NewService(repository, barrierObjects, fixedOutputBuilder{data: sharedOutput}, appfont.Config{
		BuilderVersion: "fencing-e2e", BuildLease: time.Minute, BuildTimeout: 20 * time.Second,
		StaticBuildConcurrency: 1, MaxPendingBuilds: 2, WorkerID: "pod-a",
	})
	if err != nil {
		t.Fatalf("create pod A service: %v", err)
	}
	podB, err := appfont.NewService(repository, barrierObjects, fixedOutputBuilder{data: sharedOutput}, appfont.Config{
		BuilderVersion: "fencing-e2e", BuildLease: time.Minute, BuildTimeout: 20 * time.Second,
		StaticBuildConcurrency: 1, MaxPendingBuilds: 2, WorkerID: "pod-b",
	})
	if err != nil {
		t.Fatalf("create pod B service: %v", err)
	}
	request := appfont.GenerateRequest{FontID: familyID, Words: "AB", Min: true, Weight: "400"}

	podAResult := make(chan error, 1)
	go func() {
		_, generateErr := podA.Generate(ctx, request)
		podAResult <- generateErr
	}()
	waitForTestSignal(t, barrierObjects.firstUploaded, "pod A object upload")
	loserPage, err := objects.ListObjects(ctx, "_generated/", "", 10)
	if err != nil {
		t.Fatalf("list first unpublished object: %v", err)
	}
	if len(loserPage.Objects) != 1 {
		t.Fatalf("generated objects before takeover = %#v, want one", loserPage.Objects)
	}
	loserObject := loserPage.Objects[0]
	if _, err := pool.Exec(ctx, `
		UPDATE font_build_jobs AS job
		SET lease_until = now() - interval '1 second'
		FROM font_artifacts AS artifact
		WHERE job.artifact_key = artifact.artifact_key
		  AND artifact.family_id = $1`, familyID); err != nil {
		t.Fatalf("expire pod A lease: %v", err)
	}

	podBResponse, err := podB.Generate(ctx, request)
	if err != nil {
		t.Fatalf("pod B Generate: %v", err)
	}
	generatedPage, err := objects.ListObjects(ctx, "_generated/", "", 10)
	if err != nil {
		t.Fatalf("list generated objects after takeover: %v", err)
	}
	if len(generatedPage.Objects) != 2 {
		t.Fatalf("generated objects after takeover = %#v, want two fence-separated keys", generatedPage.Objects)
	}
	close(barrierObjects.releaseFirst)
	select {
	case err := <-podAResult:
		if !errors.Is(err, appfont.ErrBuildNotReady) {
			t.Fatalf("pod A late result = %v, want ErrBuildNotReady", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pod A late publication")
	}
	if err := objects.DeleteObject(ctx, loserObject); err != nil {
		t.Fatalf("delete first unpublished object as cleanup would: %v", err)
	}

	cachedResponse, err := podA.Generate(ctx, request)
	if err != nil {
		t.Fatalf("cache read after takeover: %v", err)
	}
	if len(podBResponse.Location) != 1 || len(cachedResponse.Location) != 1 ||
		podBResponse.Location[0] != cachedResponse.Location[0] {
		t.Fatalf("pod B locations = %#v; cached locations = %#v", podBResponse.Location, cachedResponse.Location)
	}
	response, err := http.Get(cachedResponse.Location[0])
	if err != nil {
		t.Fatalf("download winning artifact: %v", err)
	}
	winningBytes, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("download winning artifact: status=%d read=%v close=%v", response.StatusCode, readErr, closeErr)
	}
	if !bytes.Equal(winningBytes, sharedOutput) {
		t.Fatalf("winning bytes = %q, want shared bytes %q", winningBytes, sharedOutput)
	}

	var status, objectKey, objectVersionID, checksum string
	var generation int64
	var attempts int64
	if err := pool.QueryRow(ctx, `
		SELECT artifact.status, artifact.object_key, COALESCE(artifact.object_version_id, ''),
		       COALESCE(artifact.checksum_sha256, ''), artifact.generation, job.attempts
		FROM font_artifacts AS artifact
		JOIN font_build_jobs AS job USING (artifact_key)
		WHERE artifact.family_id = $1`, familyID).Scan(
		&status, &objectKey, &objectVersionID, &checksum, &generation, &attempts,
	); err != nil {
		t.Fatalf("read winning artifact row: %v", err)
	}
	winnerSum := sha256.Sum256(sharedOutput)
	if status != "ready" || generation <= 0 || attempts != 2 || objectVersionID == "" || checksum != hex.EncodeToString(winnerSum[:]) ||
		!strings.Contains(objectKey, checksum) || !strings.Contains(objectKey, "-f"+strconv.FormatInt(generation, 10)+"-") ||
		objectKey == loserObject.Key {
		t.Fatalf("winning artifact row: status=%q key=%q version=%q checksum=%q generation=%d attempts=%d", status, objectKey, objectVersionID, checksum, generation, attempts)
	}
}

type publicationBarrierStore struct {
	*miniostore.Store
	mu            sync.Mutex
	generatedPuts int
	firstUploaded chan struct{}
	releaseFirst  chan struct{}
}

func newPublicationBarrierStore(store *miniostore.Store) *publicationBarrierStore {
	return &publicationBarrierStore{
		Store: store, firstUploaded: make(chan struct{}), releaseFirst: make(chan struct{}),
	}
}

func (s *publicationBarrierStore) PutObject(
	ctx context.Context,
	key string,
	reader io.Reader,
	size int64,
	options appfont.PutObjectOptions,
) (appfont.ObjectInfo, error) {
	info, err := s.Store.PutObject(ctx, key, reader, size, options)
	if err != nil || !strings.HasPrefix(key, "_generated/") {
		return info, err
	}
	s.mu.Lock()
	s.generatedPuts++
	first := s.generatedPuts == 1
	if first {
		close(s.firstUploaded)
	}
	s.mu.Unlock()
	if !first {
		return info, nil
	}
	select {
	case <-s.releaseFirst:
		return info, nil
	case <-ctx.Done():
		return appfont.ObjectInfo{}, ctx.Err()
	}
}

type fixedOutputBuilder struct {
	data []byte
}

func (b fixedOutputBuilder) BuildSubset(context.Context, appfont.BuildInput) (appfont.BuildOutput, error) {
	return appfont.BuildOutput{
		Data: b.data, ContentType: domainfont.ContentTypeWOFF2,
		Format: domainfont.OutputFormatWOFF2, GlyphCount: 1,
	}, nil
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
