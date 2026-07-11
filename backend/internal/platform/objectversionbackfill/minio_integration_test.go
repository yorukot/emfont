package objectversionbackfill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestMinIOIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("EMFONT_TEST_MINIO_ENDPOINT"))
	accessKey := os.Getenv("EMFONT_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("EMFONT_TEST_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("EMFONT_TEST_MINIO_ENDPOINT, EMFONT_TEST_MINIO_ACCESS_KEY, and EMFONT_TEST_MINIO_SECRET_KEY must be set")
	}

	client, err := miniogo.New(endpoint, &miniogo.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), TrailingHeaders: true,
	})
	if err != nil {
		t.Fatalf("create MinIO client: %v", err)
	}
	bucket := fmt.Sprintf("emfont-backfill-%d", time.Now().UnixNano())
	key := "original-fonts/legacy.ttf"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for object := range client.ListObjects(cleanupCtx, bucket, miniogo.ListObjectsOptions{
			Recursive: true, WithVersions: true,
		}) {
			if object.Err == nil {
				_ = client.RemoveObject(cleanupCtx, bucket, object.Key, miniogo.RemoveObjectOptions{VersionID: object.VersionID})
			}
		}
		_ = client.RemoveBucket(cleanupCtx, bucket)
	})

	body := []byte("legacy-font-content")
	expires := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	tags := map[string]string{"family": "Inter", "source": "legacy"}
	_, err = client.PutObject(ctx, bucket, key, bytes.NewReader(body), int64(len(body)), miniogo.PutObjectOptions{
		ContentType:        "font/ttf",
		CacheControl:       "public, max-age=3600",
		ContentEncoding:    "identity",
		ContentLanguage:    "en",
		ContentDisposition: `attachment; filename="legacy.ttf"`,
		Expires:            expires,
		StorageClass:       "REDUCED_REDUNDANCY",
		UserMetadata: map[string]string{
			"X-Amz-Meta-Content-Type": "legacy-label",
			"owner":                   "fonts",
		},
		UserTags: tags, Checksum: miniogo.ChecksumSHA256, DisableMultipart: true,
	})
	if err != nil {
		t.Fatalf("put null-version object: %v", err)
	}
	if err := client.EnableVersioning(ctx, bucket); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}

	store, err := NewMinIOStore(Config{
		Endpoint: endpoint, Bucket: bucket, AccessKey: accessKey, SecretKey: secretKey,
	})
	if err != nil {
		t.Fatalf("NewMinIOStore: %v", err)
	}
	legacy, err := store.StatVersion(ctx, key, NullVersionID)
	if err != nil {
		t.Fatalf("stat legacy version: %v", err)
	}
	legacyChecksum := sha256.Sum256(body)
	wantLegacyChecksum := Checksum{
		Algorithm: ChecksumSHA256,
		Value:     base64.StdEncoding.EncodeToString(legacyChecksum[:]),
	}
	if legacy.Checksum != wantLegacyChecksum {
		t.Fatalf("legacy checksum = %#v, want %#v", legacy.Checksum, wantLegacyChecksum)
	}
	legacyTags, err := store.GetTags(ctx, key, NullVersionID)
	if err != nil {
		t.Fatalf("get legacy tags: %v", err)
	}
	versionsBeforePreconditions := integrationVersionCount(t, ctx, client, bucket, key)
	t.Run("source ETag precondition", func(t *testing.T) {
		request := integrationRewriteRequest(t, legacy, legacyTags, body)
		request.SourceETag = "incorrect-source-etag"
		_, rewriteErr := store.Rewrite(ctx, request)
		if rewriteErr == nil || !strings.Contains(rewriteErr.Error(), "PreconditionFailed") ||
			!strings.Contains(rewriteErr.Error(), "HTTP 412") {
			t.Fatalf("Rewrite error = %v, want S3 PreconditionFailed HTTP 412", rewriteErr)
		}
		if got := integrationVersionCount(t, ctx, client, bucket, key); got != versionsBeforePreconditions {
			t.Fatalf("versions after rejected rewrite = %d, want %d", got, versionsBeforePreconditions)
		}
	})
	t.Run("destination If-Match behavior", func(t *testing.T) {
		request := integrationRewriteRequest(t, legacy, legacyTags, body)
		request.CurrentETag = "incorrect-current-etag"
		_, rewriteErr := store.Rewrite(ctx, request)
		if rewriteErr == nil || !strings.Contains(rewriteErr.Error(), "PreconditionFailed") ||
			!strings.Contains(rewriteErr.Error(), "HTTP 412") {
			t.Fatalf("Rewrite error = %v, want S3 PreconditionFailed HTTP 412", rewriteErr)
		}
		if got := integrationVersionCount(t, ctx, client, bucket, key); got != versionsBeforePreconditions {
			t.Fatalf("versions after rejected destination CAS = %d, want %d", got, versionsBeforePreconditions)
		}
	})
	result, err := Run(ctx, store, 2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != (Result{Scanned: 1, NullVersions: 1, Rewritten: 1}) {
		t.Fatalf("result = %#v", result)
	}

	current, err := client.StatObject(ctx, bucket, key, miniogo.StatObjectOptions{Checksum: true})
	if err != nil {
		t.Fatalf("stat current version: %v", err)
	}
	if isNullVersion(current.VersionID) {
		t.Fatalf("current version = %q", current.VersionID)
	}
	currentObject, err := objectFromInfo(current)
	if err != nil {
		t.Fatalf("inspect current checksum: %v", err)
	}
	if currentObject.Checksum != wantLegacyChecksum {
		t.Fatalf("current checksum = %#v, want %#v", currentObject.Checksum, wantLegacyChecksum)
	}
	if current.ContentType != "font/ttf" || current.Metadata.Get("Cache-Control") != "public, max-age=3600" ||
		current.Metadata.Get("Content-Encoding") != "identity" || current.Metadata.Get("Content-Language") != "en" ||
		current.Metadata.Get("Content-Disposition") != `attachment; filename="legacy.ttf"` ||
		current.Metadata.Get("X-Amz-Storage-Class") != "REDUCED_REDUNDANCY" || !current.Expires.Equal(expires) {
		t.Fatalf("current standard metadata = %#v", current)
	}
	if current.UserMetadata["Content-Type"] != "legacy-label" || current.UserMetadata["Owner"] != "fonts" {
		t.Fatalf("current user metadata = %#v", current.UserMetadata)
	}
	if _, _, markerErr := parseMarkers(currentObject.Metadata); markerErr != nil {
		t.Fatalf("current verification markers: %v", markerErr)
	}
	currentTags, err := client.GetObjectTagging(ctx, bucket, key, miniogo.GetObjectTaggingOptions{VersionID: current.VersionID})
	if err != nil {
		t.Fatalf("get current tags: %v", err)
	}
	if !equalMap(currentTags.ToMap(), tags) {
		t.Fatalf("current tags = %#v", currentTags.ToMap())
	}
	object, err := client.GetObject(ctx, bucket, key, miniogo.GetObjectOptions{VersionID: current.VersionID})
	if err != nil {
		t.Fatalf("get current version: %v", err)
	}
	readBack, readErr := io.ReadAll(object)
	closeErr := object.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(readBack, body) {
		t.Fatalf("current bytes = %q, read error = %v, close error = %v", readBack, readErr, closeErr)
	}

	if err := client.RemoveObject(ctx, bucket, key, miniogo.RemoveObjectOptions{VersionID: NullVersionID}); err != nil {
		t.Fatalf("expire original null version: %v", err)
	}
	versionsBeforeRerun := integrationVersionCount(t, ctx, client, bucket, key)
	if versionsBeforeRerun != 1 {
		t.Fatalf("versions after null-version expiry = %d, want 1", versionsBeforeRerun)
	}
	second, err := Run(ctx, store, 2)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second != (Result{Scanned: 1, AlreadyVersioned: 1}) {
		t.Fatalf("second result = %#v", second)
	}
	if versionsAfterRerun := integrationVersionCount(t, ctx, client, bucket, key); versionsAfterRerun != versionsBeforeRerun {
		t.Fatalf("versions after rerun = %d, want %d", versionsAfterRerun, versionsBeforeRerun)
	}
}

func TestMinIOChecksumPreservationIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("EMFONT_TEST_MINIO_ENDPOINT"))
	accessKey := os.Getenv("EMFONT_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("EMFONT_TEST_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("EMFONT_TEST_MINIO_ENDPOINT, EMFONT_TEST_MINIO_ACCESS_KEY, and EMFONT_TEST_MINIO_SECRET_KEY must be set")
	}
	client, err := miniogo.New(endpoint, &miniogo.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), TrailingHeaders: true,
	})
	if err != nil {
		t.Fatalf("create MinIO client: %v", err)
	}
	tests := []struct {
		name      string
		algorithm ChecksumAlgorithm
		kind      miniogo.ChecksumType
	}{
		{name: "none"},
		{name: "CRC32", algorithm: ChecksumCRC32, kind: miniogo.ChecksumCRC32},
		{name: "CRC32C", algorithm: ChecksumCRC32C, kind: miniogo.ChecksumCRC32C},
		{name: "SHA1", algorithm: ChecksumSHA1, kind: miniogo.ChecksumSHA1},
		{name: "SHA256", algorithm: ChecksumSHA256, kind: miniogo.ChecksumSHA256},
		{name: "CRC64NVME", algorithm: ChecksumCRC64NVME, kind: miniogo.ChecksumCRC64NVME},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bucket := fmt.Sprintf("emfont-checksum-%d-%d", time.Now().UnixNano(), index)
			key := "original-fonts/checksum.ttf"
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{}); err != nil {
				t.Fatalf("make bucket: %v", err)
			}
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cleanupCancel()
				for object := range client.ListObjects(cleanupCtx, bucket, miniogo.ListObjectsOptions{
					Recursive: true, WithVersions: true,
				}) {
					if object.Err == nil {
						_ = client.RemoveObject(cleanupCtx, bucket, object.Key, miniogo.RemoveObjectOptions{VersionID: object.VersionID})
					}
				}
				_ = client.RemoveBucket(cleanupCtx, bucket)
			})

			body := []byte("checksum-preservation-" + test.name)
			putOptions := miniogo.PutObjectOptions{
				ContentType: "font/ttf", DisableMultipart: true, Checksum: test.kind,
				SendContentMd5: !test.kind.IsSet(),
			}
			if _, err := client.PutObject(ctx, bucket, key, bytes.NewReader(body), int64(len(body)), putOptions); err != nil {
				t.Fatalf("put null-version object: %v", err)
			}
			if err := client.EnableVersioning(ctx, bucket); err != nil {
				t.Fatalf("enable versioning: %v", err)
			}
			store, err := NewMinIOStore(Config{
				Endpoint: endpoint, Bucket: bucket, AccessKey: accessKey, SecretKey: secretKey,
			})
			if err != nil {
				t.Fatalf("NewMinIOStore: %v", err)
			}
			want := Checksum{}
			if test.kind.IsSet() {
				want = Checksum{Algorithm: test.algorithm, Value: test.kind.ChecksumBytes(body).Encoded()}
			}
			source, err := store.StatVersion(ctx, key, NullVersionID)
			if err != nil {
				t.Fatalf("stat null version: %v", err)
			}
			if source.Checksum != want {
				t.Fatalf("source checksum = %#v, want %#v", source.Checksum, want)
			}
			result, err := Run(ctx, store, 1)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result != (Result{Scanned: 1, NullVersions: 1, Rewritten: 1}) {
				t.Fatalf("result = %#v", result)
			}
			current, err := store.StatCurrent(ctx, key)
			if err != nil {
				t.Fatalf("stat rewritten version: %v", err)
			}
			if current.Checksum != want {
				t.Fatalf("destination checksum = %#v, want %#v", current.Checksum, want)
			}
		})
	}
}

var errIntegrationVerification = errors.New("injected post-commit integration verification failure")

type failMarkedDigestOnceStore struct {
	Store
	mu     sync.Mutex
	failed bool
}

func (store *failMarkedDigestOnceStore) SHA256(
	ctx context.Context,
	key, versionID, etag string,
) (Digest, error) {
	store.mu.Lock()
	if !isNullVersion(versionID) && !store.failed {
		store.failed = true
		store.mu.Unlock()
		return Digest{}, errIntegrationVerification
	}
	store.mu.Unlock()
	return store.Store.SHA256(ctx, key, versionID, etag)
}

func TestMinIOResumableMarkerVerificationIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("EMFONT_TEST_MINIO_ENDPOINT"))
	accessKey := os.Getenv("EMFONT_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("EMFONT_TEST_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("EMFONT_TEST_MINIO_ENDPOINT, EMFONT_TEST_MINIO_ACCESS_KEY, and EMFONT_TEST_MINIO_SECRET_KEY must be set")
	}
	client, err := miniogo.New(endpoint, &miniogo.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""),
	})
	if err != nil {
		t.Fatalf("create MinIO client: %v", err)
	}
	bucket := fmt.Sprintf("emfont-backfill-resume-%d", time.Now().UnixNano())
	key := "original-fonts/resumable.ttf"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for object := range client.ListObjects(cleanupCtx, bucket, miniogo.ListObjectsOptions{
			Recursive: true, WithVersions: true,
		}) {
			if object.Err == nil {
				_ = client.RemoveObject(cleanupCtx, bucket, object.Key, miniogo.RemoveObjectOptions{VersionID: object.VersionID})
			}
		}
		_ = client.RemoveBucket(cleanupCtx, bucket)
	})

	body := []byte("resumable-font-content")
	tags := map[string]string{"family": "Inter", "state": "legacy"}
	_, err = client.PutObject(ctx, bucket, key, bytes.NewReader(body), int64(len(body)), miniogo.PutObjectOptions{
		ContentType: "font/ttf",
		UserMetadata: map[string]string{
			"owner": "fonts",
		},
		UserTags: tags, DisableMultipart: true,
	})
	if err != nil {
		t.Fatalf("put null-version object: %v", err)
	}
	if err := client.EnableVersioning(ctx, bucket); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}
	store, err := NewMinIOStore(Config{
		Endpoint: endpoint, Bucket: bucket, AccessKey: accessKey, SecretKey: secretKey,
	})
	if err != nil {
		t.Fatalf("NewMinIOStore: %v", err)
	}

	first, err := Run(ctx, &failMarkedDigestOnceStore{Store: store}, 1)
	if !errors.Is(err, errIntegrationVerification) {
		t.Fatalf("first Run error = %v, want injected post-commit failure", err)
	}
	if first != (Result{Scanned: 1, NullVersions: 1}) {
		t.Fatalf("first result = %#v", first)
	}
	if got := integrationVersionCount(t, ctx, client, bucket, key); got != 2 {
		t.Fatalf("versions after committed failed run = %d, want 2", got)
	}
	committed, err := store.StatCurrent(ctx, key)
	if err != nil {
		t.Fatalf("stat committed version: %v", err)
	}
	if isNullVersion(committed.VersionID) || !hasReservedMarker(committed.Metadata) {
		t.Fatalf("committed version = %#v, want real marked version", committed)
	}

	versionsBeforeResume := integrationVersionCount(t, ctx, client, bucket, key)
	second, err := Run(ctx, store, 1)
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	if second != (Result{Scanned: 1, AlreadyVersioned: 1}) {
		t.Fatalf("resume result = %#v", second)
	}
	if got := integrationVersionCount(t, ctx, client, bucket, key); got != versionsBeforeResume {
		t.Fatalf("versions after resume = %d, want %d", got, versionsBeforeResume)
	}
	if err := client.RemoveObject(ctx, bucket, key, miniogo.RemoveObjectOptions{VersionID: NullVersionID}); err != nil {
		t.Fatalf("expire resumable null version: %v", err)
	}
	third, err := Run(ctx, store, 1)
	if err != nil {
		t.Fatalf("Run after resumable null-version expiry: %v", err)
	}
	if third != (Result{Scanned: 1, AlreadyVersioned: 1}) {
		t.Fatalf("post-expiry result = %#v", third)
	}
}

type injectSameContentVersionStore struct {
	Store
	client *miniogo.Client
	bucket string
	body   []byte
	mu     sync.Mutex
	done   bool
}

func (store *injectSameContentVersionStore) Rewrite(
	ctx context.Context,
	request RewriteRequest,
) (RewriteResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.done {
		store.done = true
		if _, err := store.client.PutObject(
			ctx,
			store.bucket,
			request.Source.Key,
			bytes.NewReader(store.body),
			int64(len(store.body)),
			miniogo.PutObjectOptions{DisableMultipart: true, SendContentMd5: true},
		); err != nil {
			return RewriteResult{}, fmt.Errorf("inject same-content version: %w", err)
		}
	}
	return store.Store.Rewrite(ctx, request)
}

func TestMinIOSameContentConcurrentVersionFailsPersistentlyIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("EMFONT_TEST_MINIO_ENDPOINT"))
	accessKey := os.Getenv("EMFONT_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("EMFONT_TEST_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("EMFONT_TEST_MINIO_ENDPOINT, EMFONT_TEST_MINIO_ACCESS_KEY, and EMFONT_TEST_MINIO_SECRET_KEY must be set")
	}
	client, err := miniogo.New(endpoint, &miniogo.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""), TrailingHeaders: true,
	})
	if err != nil {
		t.Fatalf("create MinIO client: %v", err)
	}
	bucket := fmt.Sprintf("emfont-backfill-race-%d", time.Now().UnixNano())
	key := "original-fonts/concurrent.ttf"
	body := []byte("same-content-concurrent-version")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for object := range client.ListObjects(cleanupCtx, bucket, miniogo.ListObjectsOptions{
			Recursive: true, WithVersions: true,
		}) {
			if object.Err == nil {
				_ = client.RemoveObject(cleanupCtx, bucket, object.Key, miniogo.RemoveObjectOptions{VersionID: object.VersionID})
			}
		}
		_ = client.RemoveBucket(cleanupCtx, bucket)
	})
	if _, err := client.PutObject(
		ctx, bucket, key, bytes.NewReader(body), int64(len(body)),
		miniogo.PutObjectOptions{DisableMultipart: true, SendContentMd5: true},
	); err != nil {
		t.Fatalf("put null-version object: %v", err)
	}
	if err := client.EnableVersioning(ctx, bucket); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}
	store, err := NewMinIOStore(Config{
		Endpoint: endpoint, Bucket: bucket, AccessKey: accessKey, SecretKey: secretKey,
	})
	if err != nil {
		t.Fatalf("NewMinIOStore: %v", err)
	}
	injected := &injectSameContentVersionStore{
		Store: store, client: client, bucket: bucket, body: body,
	}
	if _, err := Run(ctx, injected, 1); err == nil || !strings.Contains(err.Error(), "version set changed concurrently") {
		t.Fatalf("first Run error = %v, want concurrent version-set failure", err)
	}
	if got := integrationVersionCount(t, ctx, client, bucket, key); got != 3 {
		t.Fatalf("versions after concurrent rewrite = %d, want 3", got)
	}
	for retry := 1; retry <= 2; retry++ {
		if _, err := Run(ctx, store, 1); err == nil || !strings.Contains(err.Error(), "version set changed concurrently") {
			t.Fatalf("retry %d error = %v, want persistent concurrent version-set failure", retry, err)
		}
		if got := integrationVersionCount(t, ctx, client, bucket, key); got != 3 {
			t.Fatalf("versions after retry %d = %d, want 3", retry, got)
		}
	}
}

func TestMinIOObjectLockRejectedIntegration(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("EMFONT_TEST_MINIO_ENDPOINT"))
	accessKey := os.Getenv("EMFONT_TEST_MINIO_ACCESS_KEY")
	secretKey := os.Getenv("EMFONT_TEST_MINIO_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("EMFONT_TEST_MINIO_ENDPOINT, EMFONT_TEST_MINIO_ACCESS_KEY, and EMFONT_TEST_MINIO_SECRET_KEY must be set")
	}
	client, err := miniogo.New(endpoint, &miniogo.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""),
	})
	if err != nil {
		t.Fatalf("create MinIO client: %v", err)
	}
	bucket := fmt.Sprintf("emfont-backfill-lock-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{ObjectLocking: true}); err != nil {
		t.Fatalf("make object-lock bucket: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = client.RemoveBucket(cleanupCtx, bucket)
	})
	store, err := NewMinIOStore(Config{
		Endpoint: endpoint, Bucket: bucket, AccessKey: accessKey, SecretKey: secretKey,
	})
	if err != nil {
		t.Fatalf("NewMinIOStore: %v", err)
	}
	security, err := store.BucketSecurity(ctx)
	if err != nil {
		t.Fatalf("BucketSecurity: %v", err)
	}
	if !security.ObjectLockConfigured {
		t.Fatalf("bucket security = %#v, want object lock configured", security)
	}
	result, err := Run(ctx, store, 1)
	if err == nil || !strings.Contains(err.Error(), "object lock") {
		t.Fatalf("Run error = %v, want object-lock rejection", err)
	}
	if result != (Result{}) {
		t.Fatalf("result = %#v, want zero", result)
	}
}

func integrationRewriteRequest(t *testing.T, source Object, tags map[string]string, body []byte) RewriteRequest {
	t.Helper()
	contentDigest := sha256.Sum256(body)
	metadataDigest, err := canonicalStateDigest(source.Metadata, tags)
	if err != nil {
		t.Fatalf("canonical metadata digest: %v", err)
	}
	versionSetDigest, err := canonicalVersionSetDigest([]Version{{
		VersionID: source.VersionID, ETag: source.ETag, Size: source.Size,
		LastModified: source.LastModified,
	}})
	if err != nil {
		t.Fatalf("canonical version-set digest: %v", err)
	}
	metadata, err := metadataWithMarkers(source.Metadata, contentDigest, metadataDigest, versionSetDigest)
	if err != nil {
		t.Fatalf("metadata markers: %v", err)
	}
	return RewriteRequest{
		SourceVersionID: NullVersionID,
		SourceETag:      source.ETag,
		CurrentETag:     source.ETag,
		Source:          source,
		Metadata:        metadata,
		Tags:            tags,
		ExpectedDigest: Digest{
			Size: int64(len(body)), SHA256: contentDigest, Checksum: source.Checksum,
		},
	}
}

func integrationVersionCount(t *testing.T, ctx context.Context, client *miniogo.Client, bucket, key string) int {
	t.Helper()
	count := 0
	for object := range client.ListObjects(ctx, bucket, miniogo.ListObjectsOptions{
		Prefix: key, Recursive: true, WithVersions: true,
	}) {
		if object.Err != nil {
			t.Fatalf("list object versions: %v", object.Err)
		}
		if object.Key == key {
			count++
		}
	}
	return count
}
