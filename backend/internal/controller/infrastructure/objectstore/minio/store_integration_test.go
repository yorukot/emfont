package minio

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	appcleanup "github.com/emfont/emfont/backend/internal/controller/application/fontcleanup"
	miniogo "github.com/minio/minio-go/v7"
)

func TestStoreIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("EMFONT_TEST_MINIO_ENDPOINT"))
	accessKey := strings.TrimSpace(os.Getenv("EMFONT_TEST_MINIO_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("EMFONT_TEST_MINIO_SECRET_KEY"))
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("EMFONT_TEST_MINIO_ENDPOINT, EMFONT_TEST_MINIO_ACCESS_KEY, and EMFONT_TEST_MINIO_SECRET_KEY must be set")
	}
	bucket := integrationBucketName(t)
	generatedPrefix := "_generated/integration/"
	generatedKeys := []string{generatedPrefix + "a.woff2", generatedPrefix + "b.woff2", generatedPrefix + "c.woff2"}
	store, err := New(Config{
		Endpoint: endpoint, AccessKey: accessKey, SecretKey: secretKey,
		Bucket: bucket, PresignExpiry: time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	createdBucket := false
	t.Cleanup(func() {
		if !createdBucket {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		for object := range store.client.ListObjects(cleanupCtx, bucket, miniogo.ListObjectsOptions{Recursive: true, WithVersions: true}) {
			if object.Err == nil {
				_ = store.client.RemoveObject(cleanupCtx, bucket, object.Key, miniogo.RemoveObjectOptions{VersionID: object.VersionID})
			}
		}
		_ = store.client.RemoveBucket(cleanupCtx, bucket)
	})
	if err := store.client.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("MakeBucket: %v", err)
	}
	createdBucket = true
	legacyGeneratedKey := generatedPrefix + "legacy-null.woff2"
	legacyGeneratedBody := []byte("pre-versioning-generated")
	if _, err := store.client.PutObject(
		ctx, bucket, legacyGeneratedKey, bytes.NewReader(legacyGeneratedBody), int64(len(legacyGeneratedBody)),
		miniogo.PutObjectOptions{DisableMultipart: true},
	); err != nil {
		t.Fatalf("put legacy null version: %v", err)
	}
	if err := store.client.EnableVersioning(ctx, bucket); err != nil {
		t.Fatalf("EnableVersioning: %v", err)
	}
	legacyReplacement, err := store.client.PutObject(
		ctx, bucket, legacyGeneratedKey, bytes.NewReader(legacyGeneratedBody), int64(len(legacyGeneratedBody)),
		miniogo.PutObjectOptions{Checksum: miniogo.ChecksumSHA256, DisableMultipart: true},
	)
	if err != nil {
		t.Fatalf("put replacement over legacy null version: %v", err)
	}
	legacyCurrent, err := store.client.StatObject(ctx, bucket, legacyGeneratedKey, miniogo.StatObjectOptions{})
	if err != nil {
		t.Fatalf("stat replacement over legacy null version: %v", err)
	}
	if legacyCurrent.VersionID == "" || legacyCurrent.VersionID == "null" || legacyCurrent.VersionID != legacyReplacement.VersionID {
		t.Fatalf("replacement identity = %#v", legacyCurrent)
	}
	if err := store.DeleteObject(ctx, appcleanup.Object{
		Key: legacyGeneratedKey, ETag: legacyCurrent.ETag, SizeBytes: legacyCurrent.Size,
		LastModified: legacyCurrent.LastModified,
	}); err != nil {
		t.Fatalf("delete replacement over legacy null version: %v", err)
	}
	if _, err := store.client.StatObject(ctx, bucket, legacyGeneratedKey, miniogo.StatObjectOptions{}); err == nil {
		t.Fatal("latest legacy generated object unexpectedly exists")
	} else if response, ok := minioErrorResponse(err); !ok || response.Code != "NoSuchKey" {
		t.Fatalf("latest legacy generated object stat error = %v, want NoSuchKey", err)
	}
	var foundNull, foundReplacement, foundLatestDeleteMarker bool
	for version := range store.client.ListObjects(ctx, bucket, miniogo.ListObjectsOptions{
		Prefix: legacyGeneratedKey, Recursive: true, WithVersions: true,
	}) {
		if version.Err != nil {
			t.Fatalf("list legacy generated versions: %v", version.Err)
		}
		if version.Key != legacyGeneratedKey {
			continue
		}
		switch {
		case version.VersionID == "null" && !version.IsDeleteMarker:
			foundNull = true
			if version.IsLatest {
				t.Fatal("legacy null version became current")
			}
		case version.VersionID == legacyReplacement.VersionID:
			foundReplacement = true
		case version.IsDeleteMarker && version.IsLatest:
			foundLatestDeleteMarker = true
		}
	}
	if !foundNull || foundReplacement || !foundLatestDeleteMarker {
		t.Fatalf(
			"post-delete versions: null=%t replacement=%t latest_marker=%t",
			foundNull, foundReplacement, foundLatestDeleteMarker,
		)
	}

	data := []byte("wOF2-integration")
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])
	info, err := store.PutObject(ctx, "artifacts/test.woff2", bytes.NewReader(data), int64(len(data)), appfont.PutObjectOptions{
		ContentType: "font/woff2", ChecksumSHA256: checksum,
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if info.SizeBytes != int64(len(data)) {
		t.Fatalf("uploaded size = %d", info.SizeBytes)
	}
	if info.VersionID == "" || info.ChecksumSHA256 != checksum {
		t.Fatalf("uploaded identity = %#v", info)
	}
	reused, err := store.PutObject(ctx, "artifacts/test.woff2", bytes.NewReader(data), int64(len(data)), appfont.PutObjectOptions{
		ContentType: "font/woff2", ChecksumSHA256: checksum,
	})
	if err != nil {
		t.Fatalf("idempotent PutObject: %v", err)
	}
	if reused.VersionID != info.VersionID {
		t.Fatalf("idempotent PutObject version = %q, want %q", reused.VersionID, info.VersionID)
	}
	different := []byte("wOF2-different")
	differentSum := sha256.Sum256(different)
	if _, err := store.PutObject(
		ctx,
		"artifacts/test.woff2",
		bytes.NewReader(different),
		int64(len(different)),
		appfont.PutObjectOptions{ContentType: "font/woff2", ChecksumSHA256: hex.EncodeToString(differentSum[:])},
	); !errors.Is(err, appfont.ErrObjectStorageUnavailable) {
		t.Fatalf("conflicting immutable PutObject error = %v, want ErrObjectStorageUnavailable", err)
	}
	stored, err := store.StatObject(ctx, "artifacts/test.woff2", info.VersionID)
	if err != nil {
		t.Fatalf("StatObject: %v", err)
	}
	if stored.ChecksumSHA256 != checksum || stored.ETag == "" {
		t.Fatalf("stored metadata = %#v", stored)
	}
	reader, _, err := store.OpenObject(ctx, "artifacts/test.woff2", info.VersionID)
	if err != nil {
		t.Fatalf("OpenObject: %v", err)
	}
	readBack, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(readBack, data) {
		t.Fatalf("read back = %q, %v", readBack, err)
	}
	location, err := store.PublicURL(ctx, "artifacts/test.woff2", info.VersionID)
	if err != nil || location == "" {
		t.Fatalf("PublicURL = %q, %v", location, err)
	}
	parsedLocation, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse PublicURL %q: %v", location, err)
	}
	if got := parsedLocation.Query().Get("versionId"); got != info.VersionID {
		t.Fatalf("PublicURL version = %q, want %q", got, info.VersionID)
	}

	sourceKey := "original-fonts/integration/400.ttf"
	sourceV1 := []byte("source-version-one")
	sourceV2 := []byte("source-version-two")
	putSource := func(data []byte) miniogo.UploadInfo {
		t.Helper()
		sum := sha256.Sum256(data)
		uploaded, putErr := store.client.PutObject(
			ctx, bucket, sourceKey, bytes.NewReader(data), int64(len(data)),
			miniogo.PutObjectOptions{
				ContentType: "font/ttf", UserMetadata: map[string]string{"sha256": hex.EncodeToString(sum[:])},
				Checksum: miniogo.ChecksumSHA256, DisableMultipart: true,
			},
		)
		if putErr != nil {
			t.Fatalf("put source version: %v", putErr)
		}
		if uploaded.VersionID == "" || uploaded.VersionID == "null" {
			t.Fatalf("source upload has no concrete version: %#v", uploaded)
		}
		return uploaded
	}
	firstSource := putSource(sourceV1)
	secondSource := putSource(sourceV2)
	if firstSource.VersionID == secondSource.VersionID {
		t.Fatalf("source replacement reused version %q", firstSource.VersionID)
	}
	pinnedSource, err := store.StatObject(ctx, sourceKey, firstSource.VersionID)
	if err != nil || pinnedSource.VersionID != firstSource.VersionID {
		t.Fatalf("StatObject pinned source = %#v, %v", pinnedSource, err)
	}
	latestSource, err := store.StatObject(ctx, sourceKey, "")
	if err != nil || latestSource.VersionID != secondSource.VersionID {
		t.Fatalf("StatObject latest source = %#v, %v", latestSource, err)
	}
	pinnedReader, pinnedInfo, err := store.OpenObject(ctx, sourceKey, firstSource.VersionID)
	if err != nil {
		t.Fatalf("OpenObject pinned source: %v", err)
	}
	pinnedBytes, readErr := io.ReadAll(pinnedReader)
	_ = pinnedReader.Close()
	if readErr != nil || !bytes.Equal(pinnedBytes, sourceV1) || pinnedInfo.VersionID != firstSource.VersionID {
		t.Fatalf("pinned source = %q, %#v, %v", pinnedBytes, pinnedInfo, readErr)
	}
	pinnedLocation, err := store.PublicURL(ctx, sourceKey, firstSource.VersionID)
	if err != nil {
		t.Fatalf("PublicURL pinned source: %v", err)
	}
	parsedPinnedLocation, err := url.Parse(pinnedLocation)
	if err != nil {
		t.Fatalf("parse pinned source URL %q: %v", pinnedLocation, err)
	}
	if got := parsedPinnedLocation.Query().Get("versionId"); got != firstSource.VersionID {
		t.Fatalf("pinned source URL version = %q, want %q", got, firstSource.VersionID)
	}
	if err := store.client.RemoveObject(ctx, bucket, sourceKey, miniogo.RemoveObjectOptions{VersionID: firstSource.VersionID}); err != nil {
		t.Fatalf("delete pinned source version: %v", err)
	}
	if _, err := store.StatObject(ctx, sourceKey, firstSource.VersionID); !errors.Is(err, appfont.ErrObjectNotFound) {
		t.Fatalf("deleted source version StatObject error = %v, want ErrObjectNotFound", err)
	}

	for _, key := range generatedKeys {
		if _, err := store.PutObject(ctx, key, bytes.NewReader(data), int64(len(data)), appfont.PutObjectOptions{
			ContentType: "font/woff2", ChecksumSHA256: checksum,
		}); err != nil {
			t.Fatalf("PutObject %q: %v", key, err)
		}
	}
	firstPage, err := store.ListObjects(ctx, generatedPrefix, "", 2)
	if err != nil {
		t.Fatalf("first ListObjects: %v", err)
	}
	if len(firstPage.Objects) != 2 || !firstPage.HasMore || firstPage.NextCursor != generatedKeys[1] {
		t.Fatalf("first object page = %#v", firstPage)
	}
	secondPage, err := store.ListObjects(ctx, generatedPrefix, firstPage.NextCursor, 2)
	if err != nil {
		t.Fatalf("second ListObjects: %v", err)
	}
	if len(secondPage.Objects) != 1 || secondPage.Objects[0].Key != generatedKeys[2] || secondPage.HasMore {
		t.Fatalf("second object page = %#v", secondPage)
	}
	if err := store.DeleteObject(ctx, firstPage.Objects[0]); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if err := store.DeleteObject(ctx, firstPage.Objects[0]); !errors.Is(err, appcleanup.ErrObjectNotFound) {
		t.Fatalf("idempotent DeleteObject error = %v, want ErrObjectNotFound", err)
	}
	if _, err := store.StatObject(ctx, generatedKeys[0], ""); !errors.Is(err, appfont.ErrObjectNotFound) {
		t.Fatalf("StatObject deleted key error = %v, want ErrObjectNotFound", err)
	}
}

func integrationBucketName(t *testing.T) string {
	t.Helper()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate integration bucket name: %v", err)
	}
	return fmt.Sprintf("emfont-it-%x", suffix)
}
