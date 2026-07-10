package minio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	miniogo "github.com/minio/minio-go/v7"
)

func TestStoreIntegration(t *testing.T) {
	endpoint := os.Getenv("EMFONT_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("EMFONT_TEST_MINIO_ENDPOINT is not set")
	}
	accessKey := os.Getenv("EMFONT_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("EMFONT_TEST_MINIO_SECRET_KEY")
	if accessKey == "" {
		accessKey = "minioadmin"
	}
	if secretKey == "" {
		secretKey = "minioadmin"
	}
	bucket := "emfont-integration"
	store, err := New(Config{
		Endpoint: endpoint, AccessKey: accessKey, SecretKey: secretKey,
		Bucket: bucket, PresignExpiry: time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exists, err := store.client.BucketExists(ctx, bucket)
	if err != nil {
		t.Fatalf("BucketExists: %v", err)
	}
	if !exists {
		if err := store.client.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{}); err != nil {
			t.Fatalf("MakeBucket: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = store.client.RemoveObject(cleanupCtx, bucket, "artifacts/test.woff2", miniogo.RemoveObjectOptions{})
		_ = store.client.RemoveBucket(cleanupCtx, bucket)
	})

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
	stored, err := store.StatObject(ctx, "artifacts/test.woff2")
	if err != nil {
		t.Fatalf("StatObject: %v", err)
	}
	if stored.ChecksumSHA256 != checksum || stored.ETag == "" {
		t.Fatalf("stored metadata = %#v", stored)
	}
	reader, _, err := store.OpenObject(ctx, "artifacts/test.woff2")
	if err != nil {
		t.Fatalf("OpenObject: %v", err)
	}
	readBack, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(readBack, data) {
		t.Fatalf("read back = %q, %v", readBack, err)
	}
	location, err := store.PublicURL(ctx, "artifacts/test.woff2")
	if err != nil || location == "" {
		t.Fatalf("PublicURL = %q, %v", location, err)
	}
}
