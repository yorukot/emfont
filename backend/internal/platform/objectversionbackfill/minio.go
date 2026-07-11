package objectversionbackfill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strings"

	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const maxSinglePutSize = int64(5 * 1024 * 1024 * 1024)

const completionRecordPrefix = "_emfont_internal/object-version-backfill/completed/"

var preservedHeaders = []string{
	"Content-Type",
	"Cache-Control",
	"Content-Encoding",
	"Content-Language",
	"Content-Disposition",
	"Expires",
	"X-Amz-Storage-Class",
	"X-Amz-Website-Redirect-Location",
}

type minioStore struct {
	client *miniogo.Client
	core   miniogo.Core
	bucket string
}

// NewMinIOStore creates the root-credential adapter used only by this helper.
func NewMinIOStore(config Config) (Store, error) {
	if strings.TrimSpace(config.Endpoint) == "" {
		return nil, errors.New("MinIO endpoint is required")
	}
	if strings.TrimSpace(config.Bucket) == "" {
		return nil, errors.New("MinIO bucket is required")
	}
	if config.AccessKey == "" || config.SecretKey == "" {
		return nil, errors.New("MinIO root credentials are required")
	}
	client, err := miniogo.New(config.Endpoint, &miniogo.Options{
		Creds:           credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""),
		Secure:          config.Secure,
		Region:          config.Region,
		TrailingHeaders: true,
	})
	if err != nil {
		return nil, errors.New("create MinIO backfill client: invalid endpoint configuration")
	}
	return &minioStore{client: client, core: miniogo.Core{Client: client}, bucket: config.Bucket}, nil
}

func (store *minioStore) BucketVersioningEnabled(ctx context.Context) (bool, error) {
	configuration, err := store.client.GetBucketVersioning(ctx, store.bucket)
	if err != nil {
		return false, storageError("get bucket versioning", err)
	}
	return configuration.Enabled(), nil
}

func (store *minioStore) BucketSecurity(ctx context.Context) (BucketSecurity, error) {
	var security BucketSecurity
	objectLock, mode, validity, unit, err := store.client.GetObjectLockConfig(ctx, store.bucket)
	if err != nil {
		if !isMissingObjectLockConfiguration(err) {
			return BucketSecurity{}, storageError("get bucket object-lock configuration", err)
		}
	} else if strings.TrimSpace(objectLock) != "" || mode != nil || validity != nil || unit != nil {
		security.ObjectLockConfigured = true
	}

	encryption, err := store.client.GetBucketEncryption(ctx, store.bucket)
	if err != nil {
		if !isMissingBucketEncryptionConfiguration(err) {
			return BucketSecurity{}, storageError("get bucket encryption configuration", err)
		}
	} else if encryption != nil {
		security.EncryptionConfigured = true
	}
	return security, nil
}

func isMissingObjectLockConfiguration(err error) bool {
	switch s3ErrorCode(err) {
	case "NoSuchObjectLockConfiguration", "ObjectLockConfigurationNotFoundError":
		return true
	default:
		return false
	}
}

func isMissingBucketEncryptionConfiguration(err error) bool {
	switch s3ErrorCode(err) {
	case "ServerSideEncryptionConfigurationNotFoundError", "NoSuchServerSideEncryptionConfiguration", "NoSuchBucketEncryption":
		return true
	default:
		return false
	}
}

func s3ErrorCode(err error) string {
	var response miniogo.ErrorResponse
	if errors.As(err, &response) {
		return response.Code
	}
	var responsePointer *miniogo.ErrorResponse
	if errors.As(err, &responsePointer) && responsePointer != nil {
		return responsePointer.Code
	}
	return miniogo.ToErrorResponse(err).Code
}

func (store *minioStore) ListCurrent(ctx context.Context) <-chan ListedObject {
	results := make(chan ListedObject)
	objects := store.client.ListObjects(ctx, store.bucket, miniogo.ListObjectsOptions{
		Recursive: true, WithVersions: true,
	})
	go func() {
		defer close(results)
		for info := range objects {
			if info.Err != nil {
				sendListed(ctx, results, ListedObject{Err: storageError("list current objects", info.Err)})
				continue
			}
			if !info.IsLatest || info.IsDeleteMarker {
				continue
			}
			if strings.HasPrefix(info.Key, completionRecordPrefix) {
				continue
			}
			if !sendListed(ctx, results, ListedObject{Object: listedObjectFromInfo(info)}) {
				// Keep draining the SDK stream so its listing goroutine can exit.
				continue
			}
		}
	}()
	return results
}

func sendListed(ctx context.Context, destination chan<- ListedObject, item ListedObject) bool {
	select {
	case destination <- item:
		return true
	case <-ctx.Done():
		return false
	}
}

func (store *minioStore) StatCurrent(ctx context.Context, key string) (Object, error) {
	return store.stat(ctx, key, "")
}

func (store *minioStore) StatVersion(ctx context.Context, key, versionID string) (Object, error) {
	return store.stat(ctx, key, versionID)
}

func (store *minioStore) stat(ctx context.Context, key, versionID string) (Object, error) {
	info, err := store.client.StatObject(ctx, store.bucket, key, miniogo.StatObjectOptions{
		Checksum: true, VersionID: versionID,
	})
	if err != nil {
		return Object{}, storageError("stat object", err)
	}
	object, err := objectFromInfo(info)
	if err != nil {
		return Object{}, fmt.Errorf("inspect object checksum state: %w", err)
	}
	return object, nil
}

func (store *minioStore) ListVersions(ctx context.Context, key string) ([]Version, error) {
	objects := store.client.ListObjects(ctx, store.bucket, miniogo.ListObjectsOptions{
		Prefix: key, Recursive: true, WithVersions: true,
	})
	versions := make([]Version, 0, 2)
	for info := range objects {
		if info.Err != nil {
			return nil, storageError("list object versions", info.Err)
		}
		if info.Key != key {
			continue
		}
		versions = append(versions, Version{
			VersionID: info.VersionID, ETag: info.ETag, Size: info.Size,
			LastModified: info.LastModified, DeleteMarker: info.IsDeleteMarker,
		})
	}
	return versions, nil
}

func (store *minioStore) GetTags(ctx context.Context, key, versionID string) (map[string]string, error) {
	objectTags, err := store.client.GetObjectTagging(ctx, store.bucket, key, miniogo.GetObjectTaggingOptions{
		VersionID: versionID,
	})
	if err != nil {
		return nil, storageError("get object tags", err)
	}
	return cloneMap(objectTags.ToMap()), nil
}

func (store *minioStore) SHA256(ctx context.Context, key, versionID, etag string) (Digest, error) {
	bareETag, err := normalizeETag(etag)
	if err != nil {
		return Digest{}, errors.New("prepare guarded object read: invalid ETag")
	}
	options := miniogo.GetObjectOptions{Checksum: true, VersionID: versionID}
	if err := options.SetMatchETag(bareETag); err != nil {
		return Digest{}, errors.New("prepare guarded object read: invalid ETag")
	}
	reader, info, headers, err := store.core.GetObject(ctx, store.bucket, key, options)
	if err != nil {
		return Digest{}, storageError("open pinned object", err)
	}
	object, objectErr := objectFromInfo(info)
	if objectErr != nil {
		_ = reader.Close()
		return Digest{}, fmt.Errorf("inspect pinned object checksum state: %w", objectErr)
	}
	if securityFromHeaders(headers).unsafe() || object.Security.unsafe() {
		_ = reader.Close()
		return Digest{}, errors.New("pinned object has unsupported encryption or object-lock state")
	}
	digestWriter, err := newDigestWriter(object.Checksum)
	if err != nil {
		_ = reader.Close()
		return Digest{}, fmt.Errorf("prepare pinned object checksum verification: %w", err)
	}
	size, readErr := io.Copy(digestWriter, reader)
	closeErr := reader.Close()
	if readErr != nil {
		return Digest{}, storageError("read pinned object", readErr)
	}
	if closeErr != nil {
		return Digest{}, storageError("close pinned object", closeErr)
	}
	digest := digestWriter.digest()
	digest.Size = size
	if err := requireStoredChecksum(object, digest); err != nil {
		return Digest{}, fmt.Errorf("verify pinned object checksum: %w", err)
	}
	return digest, nil
}

func (store *minioStore) Rewrite(ctx context.Context, request RewriteRequest) (RewriteResult, error) {
	if request.SourceVersionID != NullVersionID {
		return RewriteResult{}, errors.New("rewrite object: source version must be null")
	}
	if request.Source.Size < 0 || request.Source.Size > maxSinglePutSize {
		return RewriteResult{}, errors.New("object exceeds the guarded single-PUT size limit")
	}
	if request.Source.Security.unsafe() {
		return RewriteResult{}, errors.New("rewrite object: source has unsupported encryption or object-lock state")
	}
	sourceETag, err := normalizeETag(request.SourceETag)
	if err != nil {
		return RewriteResult{}, errors.New("rewrite object: invalid source ETag")
	}
	currentETag, err := normalizeETag(request.CurrentETag)
	if err != nil {
		return RewriteResult{}, errors.New("rewrite object: invalid current ETag")
	}
	if !isNullVersion(request.Source.VersionID) {
		return RewriteResult{}, errors.New("rewrite object: inconsistent pinned source identity")
	}
	parsedMarker, cleanMetadata, err := parseMarkers(request.Metadata)
	if err != nil {
		return RewriteResult{}, fmt.Errorf("rewrite object: %w", err)
	}
	stateDigest, err := canonicalStateDigest(cleanMetadata, request.Tags)
	if err != nil {
		return RewriteResult{}, fmt.Errorf("rewrite object: digest metadata and tags: %w", err)
	}
	if parsedMarker.ContentSHA256 != request.ExpectedDigest.SHA256 ||
		parsedMarker.MetadataSHA256 != stateDigest ||
		request.ExpectedDigest.Size != request.Source.Size ||
		request.ExpectedDigest.Checksum != request.Source.Checksum ||
		!equalMetadata(cleanMetadata, request.Source.Metadata) {
		return RewriteResult{}, errors.New("rewrite object: verification markers do not match source state")
	}

	putOptions, err := putOptionsFromMetadata(request.Metadata, request.Tags)
	if err != nil {
		return RewriteResult{}, err
	}
	putOptions.DisableMultipart = true
	putOptions.Checksum, err = minioChecksumType(request.Source.Checksum.Algorithm)
	if err != nil {
		return RewriteResult{}, fmt.Errorf("rewrite object: %w", err)
	}
	putOptions.SendContentMd5 = request.Source.Checksum.empty()
	putOptions.SetMatchETag(currentETag)

	getOptions := miniogo.GetObjectOptions{Checksum: true, VersionID: NullVersionID}
	if err := getOptions.SetMatchETag(sourceETag); err != nil {
		return RewriteResult{}, errors.New("rewrite object: invalid source ETag")
	}
	sourceReader, sourceInfo, rawHeaders, err := store.core.GetObject(
		ctx, store.bucket, request.Source.Key, getOptions,
	)
	if err != nil {
		return RewriteResult{}, storageError("get pinned rewrite source", err)
	}
	if securityFromHeaders(rawHeaders).unsafe() {
		_ = sourceReader.Close()
		return RewriteResult{}, errors.New("rewrite source has unsupported encryption or object-lock state")
	}
	streamSource, err := objectFromInfo(sourceInfo)
	if err != nil {
		_ = sourceReader.Close()
		return RewriteResult{}, fmt.Errorf("inspect rewrite source checksum state: %w", err)
	}
	if streamSource.Security.unsafe() {
		_ = sourceReader.Close()
		return RewriteResult{}, errors.New("rewrite source has unsupported encryption or object-lock state")
	}
	if !sameObjectIdentity(request.Source, streamSource) ||
		request.Source.Checksum != streamSource.Checksum ||
		!equalMetadata(request.Source.Metadata, streamSource.Metadata) {
		_ = sourceReader.Close()
		return RewriteResult{}, errors.New("pinned rewrite source changed before guarded PUT")
	}

	streamDigest, err := newDigestWriter(request.Source.Checksum)
	if err != nil {
		_ = sourceReader.Close()
		return RewriteResult{}, fmt.Errorf("prepare rewrite checksum verification: %w", err)
	}
	upload, putErr := store.client.PutObject(
		ctx,
		store.bucket,
		request.Source.Key,
		io.TeeReader(sourceReader, streamDigest),
		request.Source.Size,
		putOptions,
	)
	result := RewriteResult{
		Object: Object{
			Key: request.Source.Key, VersionID: upload.VersionID, ETag: upload.ETag,
			Size: upload.Size, LastModified: upload.LastModified,
		},
		Streamed: streamDigest.digest(),
	}
	if putErr != nil {
		_ = sourceReader.Close()
		return result, storageError("put guarded rewritten object", putErr)
	}

	var extra [1]byte
	extraBytes, trailingErr := io.ReadFull(sourceReader, extra[:])
	closeErr := sourceReader.Close()
	if extraBytes != 0 || !errors.Is(trailingErr, io.EOF) {
		return result, errors.New("pinned rewrite source did not end at the declared size")
	}
	if closeErr != nil {
		return result, storageError("close pinned rewrite source", closeErr)
	}
	if isNullVersion(upload.VersionID) {
		return result, errors.New("guarded PUT did not return a real object version")
	}
	if result.Streamed.Size != request.ExpectedDigest.Size {
		return result, errors.New("guarded PUT streamed an unexpected byte count")
	}
	if result.Streamed.SHA256 != request.ExpectedDigest.SHA256 {
		return result, errors.New("guarded PUT streamed an unexpected SHA-256")
	}
	if err := requireStoredChecksum(streamSource, result.Streamed); err != nil {
		return result, fmt.Errorf("verify rewrite source checksum: %w", err)
	}
	verifiedUpload, err := store.stat(ctx, request.Source.Key, upload.VersionID)
	if err != nil {
		return result, fmt.Errorf("verify guarded rewritten object: %w", err)
	}
	if !sameObjectIdentity(result.Object, verifiedUpload) {
		return result, errors.New("guarded PUT verification returned a different object identity")
	}
	if verifiedUpload.Checksum != request.Source.Checksum {
		return result, errors.New("guarded PUT did not preserve the exact checksum state")
	}
	if err := requireStoredChecksum(verifiedUpload, request.ExpectedDigest); err != nil {
		return result, fmt.Errorf("verify guarded PUT checksum: %w", err)
	}
	result.Object = verifiedUpload
	return result, nil
}

func (store *minioStore) CompletionRecordExists(
	ctx context.Context,
	proof [sha256.Size]byte,
) (bool, error) {
	key := completionRecordPrefix + hex.EncodeToString(proof[:])
	reader, info, headers, err := store.core.GetObject(
		ctx, store.bucket, key, miniogo.GetObjectOptions{Checksum: true},
	)
	if err != nil {
		if isMissingObject(err) {
			return false, nil
		}
		return false, storageError("read backfill completion record", err)
	}
	stored, objectErr := objectFromInfo(info)
	if objectErr != nil {
		_ = reader.Close()
		return false, fmt.Errorf("inspect backfill completion record checksum state: %w", objectErr)
	}
	if securityFromHeaders(headers).unsafe() || stored.Security.unsafe() {
		_ = reader.Close()
		return false, errors.New("backfill completion record has unsupported encryption or object-lock state")
	}
	body, readErr := io.ReadAll(io.LimitReader(reader, sha256.Size+1))
	closeErr := reader.Close()
	if readErr != nil {
		return false, storageError("read backfill completion record body", readErr)
	}
	if closeErr != nil {
		return false, storageError("close backfill completion record", closeErr)
	}
	if isNullVersion(info.VersionID) || !bytes.Equal(body, proof[:]) {
		return false, errors.New("backfill completion record has invalid identity or content")
	}
	expectedSHA256 := sha256.Sum256(proof[:])
	expected := Digest{
		Size: sha256.Size, SHA256: expectedSHA256,
		Checksum: Checksum{Algorithm: ChecksumSHA256, Value: base64.StdEncoding.EncodeToString(expectedSHA256[:])},
	}
	if stored.Checksum.Algorithm != ChecksumSHA256 {
		return false, errors.New("backfill completion record has no stored SHA-256 checksum")
	}
	if err := requireStoredChecksum(stored, expected); err != nil {
		return false, fmt.Errorf("verify backfill completion record checksum: %w", err)
	}
	return true, nil
}

func (store *minioStore) PutCompletionRecord(
	ctx context.Context,
	proof [sha256.Size]byte,
) error {
	key := completionRecordPrefix + hex.EncodeToString(proof[:])
	options := miniogo.PutObjectOptions{
		ContentType: "application/octet-stream", Checksum: miniogo.ChecksumSHA256,
		DisableMultipart: true,
	}
	options.SetMatchETagExcept("*")
	upload, err := store.client.PutObject(
		ctx, store.bucket, key, bytes.NewReader(proof[:]), sha256.Size, options,
	)
	if err != nil && s3ErrorCode(err) != "PreconditionFailed" {
		return storageError("write backfill completion record", err)
	}
	if err == nil && isNullVersion(upload.VersionID) {
		return errors.New("backfill completion record has no real version identifier")
	}
	exists, verifyErr := store.CompletionRecordExists(ctx, proof)
	if verifyErr != nil {
		return verifyErr
	}
	if !exists {
		return errors.New("backfill completion record was not stored")
	}
	return nil
}

type digestWriter struct {
	sha256Hash        hash.Hash
	checksumType      miniogo.ChecksumType
	checksumHash      hash.Hash
	checksumAlgorithm ChecksumAlgorithm
	size              int64
}

func newDigestWriter(checksum Checksum) (*digestWriter, error) {
	checksumType, err := minioChecksumType(checksum.Algorithm)
	if err != nil {
		return nil, err
	}
	writer := &digestWriter{
		sha256Hash: sha256.New(), checksumType: checksumType,
		checksumAlgorithm: checksum.Algorithm,
	}
	if checksumType.IsSet() {
		writer.checksumHash = checksumType.Hasher()
		if writer.checksumHash == nil {
			return nil, errors.New("checksum algorithm has no hasher")
		}
	}
	return writer, nil
}

func (writer *digestWriter) Write(data []byte) (int, error) {
	written, err := writer.sha256Hash.Write(data)
	writer.size += int64(written)
	if err == nil && writer.checksumHash != nil {
		checksumWritten, checksumErr := writer.checksumHash.Write(data)
		if checksumErr != nil {
			return checksumWritten, checksumErr
		}
		if checksumWritten != written {
			return checksumWritten, io.ErrShortWrite
		}
	}
	return written, err
}

func (writer *digestWriter) digest() Digest {
	var sha256Digest [sha256.Size]byte
	copy(sha256Digest[:], writer.sha256Hash.Sum(nil))
	digest := Digest{Size: writer.size, SHA256: sha256Digest}
	if writer.checksumHash != nil {
		digest.Checksum = Checksum{
			Algorithm: writer.checksumAlgorithm,
			Value:     base64.StdEncoding.EncodeToString(writer.checksumHash.Sum(nil)),
		}
	}
	return digest
}

func putOptionsFromMetadata(metadata Metadata, tags map[string]string) (miniogo.PutObjectOptions, error) {
	values := make(map[string]string, len(metadata.Headers))
	for name, value := range metadata.Headers {
		normalized := strings.ToLower(name)
		if _, duplicate := values[normalized]; duplicate {
			return miniogo.PutObjectOptions{}, errors.New("rewrite object: duplicate preserved header name")
		}
		values[normalized] = value
	}
	known := make(map[string]struct{}, len(preservedHeaders))
	for _, name := range preservedHeaders {
		known[strings.ToLower(name)] = struct{}{}
	}
	for name := range values {
		if _, ok := known[name]; !ok {
			return miniogo.PutObjectOptions{}, errors.New("rewrite object: unsupported preserved header")
		}
	}

	options := miniogo.PutObjectOptions{
		UserMetadata:            putUserMetadata(metadata.User),
		UserTags:                cloneMap(tags),
		ContentType:             values["content-type"],
		CacheControl:            values["cache-control"],
		ContentEncoding:         values["content-encoding"],
		ContentLanguage:         values["content-language"],
		ContentDisposition:      values["content-disposition"],
		StorageClass:            values["x-amz-storage-class"],
		WebsiteRedirectLocation: values["x-amz-website-redirect-location"],
	}
	if expires := values["expires"]; expires != "" {
		parsed, err := http.ParseTime(expires)
		if err != nil {
			return miniogo.PutObjectOptions{}, errors.New("rewrite object: invalid preserved expiry")
		}
		options.Expires = parsed
	}
	return options, nil
}

func putUserMetadata(metadata map[string]string) map[string]string {
	headers := make(map[string]string, len(metadata))
	for name, value := range metadata {
		headers["X-Amz-Meta-"+name] = value
	}
	return headers
}

func normalizeETag(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, `"`) || strings.HasSuffix(value, `"`) {
		if len(value) < 2 || !strings.HasPrefix(value, `"`) || !strings.HasSuffix(value, `"`) {
			return "", errors.New("invalid ETag")
		}
		value = strings.TrimPrefix(strings.TrimSuffix(value, `"`), `"`)
	}
	if value == "" || strings.ContainsAny(value, "\"\r\n") {
		return "", errors.New("invalid ETag")
	}
	return value, nil
}

func listedObjectFromInfo(info miniogo.ObjectInfo) Object {
	return objectBaseFromInfo(info)
}

func objectFromInfo(info miniogo.ObjectInfo) (Object, error) {
	object := objectBaseFromInfo(info)
	checksum, err := checksumFromInfo(info)
	if err != nil {
		return Object{}, err
	}
	object.Checksum = checksum
	return object, nil
}

func objectBaseFromInfo(info miniogo.ObjectInfo) Object {
	headers := make(map[string]string, len(preservedHeaders))
	for _, name := range preservedHeaders {
		var value string
		switch name {
		case "Content-Type":
			value = info.ContentType
		case "Expires":
			if !info.Expires.IsZero() {
				value = info.Expires.UTC().Format(http.TimeFormat)
			}
		case "X-Amz-Storage-Class":
			value = info.StorageClass
			if value == "" {
				value = info.Metadata.Get(name)
			}
		default:
			value = info.Metadata.Get(name)
		}
		if value != "" {
			headers[name] = value
		}
	}
	userMetadata := make(map[string]string, len(info.UserMetadata))
	for name, value := range info.UserMetadata {
		userMetadata[strings.ToLower(name)] = value
	}
	return Object{
		Key: info.Key, VersionID: info.VersionID, ETag: strings.Trim(info.ETag, `"`),
		Size: info.Size, LastModified: info.LastModified,
		Metadata: Metadata{Headers: headers, User: userMetadata},
		Security: securityFromHeaders(info.Metadata),
	}
}

func checksumFromInfo(info miniogo.ObjectInfo) (Checksum, error) {
	type candidate struct {
		algorithm ChecksumAlgorithm
		value     string
	}
	candidates := []candidate{
		{ChecksumCRC32, info.ChecksumCRC32},
		{ChecksumCRC32C, info.ChecksumCRC32C},
		{ChecksumSHA1, info.ChecksumSHA1},
		{ChecksumSHA256, info.ChecksumSHA256},
		{ChecksumCRC64NVME, info.ChecksumCRC64NVME},
	}
	knownHeaders := make(map[string]ChecksumAlgorithm, len(candidates))
	for _, item := range candidates {
		checksumType, err := minioChecksumType(item.algorithm)
		if err != nil {
			return Checksum{}, err
		}
		knownHeaders[strings.ToLower(checksumType.Key())] = item.algorithm
	}

	values := make(map[ChecksumAlgorithm][]string, len(candidates))
	for _, item := range candidates {
		if strings.TrimSpace(item.value) != "" {
			values[item.algorithm] = append(values[item.algorithm], item.value)
		}
	}
	for name, headerValues := range info.Metadata {
		normalizedName := strings.ToLower(name)
		if normalizedName == "x-amz-checksum-type" {
			for _, value := range headerValues {
				if strings.TrimSpace(value) != "" {
					return Checksum{}, errors.New("composite or explicitly typed checksum state is unsupported")
				}
			}
			continue
		}
		algorithm, known := knownHeaders[normalizedName]
		if !known {
			if strings.HasPrefix(normalizedName, "x-amz-checksum-") {
				return Checksum{}, fmt.Errorf("unsupported checksum header %q", name)
			}
			continue
		}
		values[algorithm] = append(values[algorithm], headerValues...)
	}

	var result Checksum
	for _, item := range candidates {
		algorithmValues := values[item.algorithm]
		if len(algorithmValues) == 0 {
			continue
		}
		checksumType, err := minioChecksumType(item.algorithm)
		if err != nil {
			return Checksum{}, err
		}
		canonical := ""
		for _, value := range algorithmValues {
			normalized, normalizeErr := normalizeChecksumValue(value, checksumType.RawByteLen())
			if normalizeErr != nil {
				return Checksum{}, fmt.Errorf("invalid %s checksum: %w", item.algorithm, normalizeErr)
			}
			if canonical != "" && canonical != normalized {
				return Checksum{}, fmt.Errorf("ambiguous %s checksum values", item.algorithm)
			}
			canonical = normalized
		}
		if !result.empty() {
			return Checksum{}, errors.New("multiple stored checksum algorithms are unsupported")
		}
		result = Checksum{Algorithm: item.algorithm, Value: canonical}
	}
	return result, nil
}

func normalizeChecksumValue(value string, rawLength int) (string, error) {
	value = strings.TrimSpace(value)
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != rawLength {
		return "", errors.New("value is not canonical base64 with the expected length")
	}
	return base64.StdEncoding.EncodeToString(decoded), nil
}

func minioChecksumType(algorithm ChecksumAlgorithm) (miniogo.ChecksumType, error) {
	switch algorithm {
	case "":
		return miniogo.ChecksumNone, nil
	case ChecksumCRC32:
		return miniogo.ChecksumCRC32, nil
	case ChecksumCRC32C:
		return miniogo.ChecksumCRC32C, nil
	case ChecksumSHA1:
		return miniogo.ChecksumSHA1, nil
	case ChecksumSHA256:
		return miniogo.ChecksumSHA256, nil
	case ChecksumCRC64NVME:
		return miniogo.ChecksumCRC64NVME, nil
	default:
		return miniogo.ChecksumNone, fmt.Errorf("unsupported checksum algorithm %q", algorithm)
	}
}

func isMissingObject(err error) bool {
	switch s3ErrorCode(err) {
	case "NoSuchKey", "NoSuchObject", "NoSuchVersion":
		return true
	default:
		return false
	}
}

func securityFromHeaders(headers http.Header) SecurityState {
	var security SecurityState
	for name := range headers {
		normalized := strings.ToLower(name)
		switch {
		case strings.HasPrefix(normalized, "x-amz-server-side-encryption"):
			security.Encryption = true
		case strings.HasPrefix(normalized, "x-amz-object-lock-"):
			security.ObjectLock = true
		case strings.HasPrefix(normalized, "x-minio-") && strings.Contains(normalized, "server-side-encryption"):
			security.Encryption = true
		case strings.HasPrefix(normalized, "x-minio-") && strings.Contains(normalized, "object-lock"):
			security.ObjectLock = true
		}
	}
	return security
}

func storageError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	var response miniogo.ErrorResponse
	if errors.As(err, &response) {
		if response.StatusCode != 0 {
			return fmt.Errorf("%s: S3 %s (HTTP %d)", operation, response.Code, response.StatusCode)
		}
		return fmt.Errorf("%s: S3 %s", operation, response.Code)
	}
	var responsePointer *miniogo.ErrorResponse
	if errors.As(err, &responsePointer) && responsePointer != nil {
		if responsePointer.StatusCode != 0 {
			return fmt.Errorf("%s: S3 %s (HTTP %d)", operation, responsePointer.Code, responsePointer.StatusCode)
		}
		return fmt.Errorf("%s: S3 %s", operation, responsePointer.Code)
	}
	return fmt.Errorf("%s: object storage request failed", operation)
}
