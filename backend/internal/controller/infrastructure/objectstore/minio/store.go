package minio

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	appcleanup "github.com/emfont/emfont/backend/internal/controller/application/fontcleanup"
	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Config struct {
	Endpoint      string
	AccessKey     string
	SecretKey     string
	SessionToken  string
	Bucket        string
	Region        string
	Secure        bool
	PublicBaseURL string
	PresignExpiry time.Duration
}

type Store struct {
	client        *miniogo.Client
	bucket        string
	publicBaseURL *url.URL
	presignExpiry time.Duration
}

func New(cfg Config) (*Store, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, errors.New("minio endpoint is required")
	}
	if strings.Contains(endpoint, "://") || strings.ContainsAny(endpoint, "/?#@") ||
		strings.ContainsAny(endpoint, " \t\r\n") {
		return nil, errors.New("minio endpoint must contain only a host and optional port")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("minio bucket is required")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("minio access key and secret key are required")
	}
	publicBaseURL, err := parsePublicBaseURL(cfg.PublicBaseURL)
	if err != nil {
		return nil, err
	}
	client, err := miniogo.New(endpoint, &miniogo.Options{
		Creds:           credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken),
		Secure:          cfg.Secure,
		Region:          strings.TrimSpace(cfg.Region),
		TrailingHeaders: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}
	if cfg.PresignExpiry <= 0 {
		cfg.PresignExpiry = time.Hour
	}
	return &Store{
		client: client, bucket: cfg.Bucket,
		publicBaseURL: publicBaseURL,
		presignExpiry: cfg.PresignExpiry,
	}, nil
}

func (s *Store) StatObject(ctx context.Context, key, versionID string) (appfont.ObjectInfo, error) {
	if err := s.validate(); err != nil {
		return appfont.ObjectInfo{}, err
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, miniogo.StatObjectOptions{Checksum: true, VersionID: versionID})
	if err != nil {
		return appfont.ObjectInfo{}, mapError("stat", key, err)
	}
	return objectInfo(key, info), nil
}

func (s *Store) OpenObject(ctx context.Context, key, versionID string) (io.ReadCloser, appfont.ObjectInfo, error) {
	if err := s.validate(); err != nil {
		return nil, appfont.ObjectInfo{}, err
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, miniogo.GetObjectOptions{Checksum: true, VersionID: versionID})
	if err != nil {
		return nil, appfont.ObjectInfo{}, mapError("open", key, err)
	}
	info, err := object.Stat()
	if err != nil {
		mappedErr := mapError("open", key, err)
		if closeErr := object.Close(); closeErr != nil {
			mappedErr = errors.Join(mappedErr, fmt.Errorf("close minio object %q after open failure: %w", key, closeErr))
		}
		return nil, appfont.ObjectInfo{}, mappedErr
	}
	return object, objectInfo(key, info), nil
}

func (s *Store) PutObject(ctx context.Context, key string, reader io.Reader, size int64, options appfont.PutObjectOptions) (appfont.ObjectInfo, error) {
	if err := s.validate(); err != nil {
		return appfont.ObjectInfo{}, err
	}
	if size <= 0 {
		return appfont.ObjectInfo{}, errors.New("object size must be greater than zero")
	}
	checksum, err := requiredSHA256(options.ChecksumSHA256)
	if err != nil {
		return appfont.ObjectInfo{}, err
	}
	metadata := make(map[string]string, 1)
	if checksum != "" {
		metadata["sha256"] = checksum
	}
	putOptions := miniogo.PutObjectOptions{
		ContentType:      options.ContentType,
		UserMetadata:     metadata,
		Checksum:         miniogo.ChecksumSHA256,
		DisableMultipart: true,
	}
	putOptions.SetMatchETagExcept("*")
	uploaded, err := s.client.PutObject(ctx, s.bucket, key, reader, size, putOptions)
	if err != nil {
		if isPreconditionFailed(err) {
			info, statErr := s.client.StatObject(ctx, s.bucket, key, miniogo.StatObjectOptions{Checksum: true})
			if statErr != nil {
				return appfont.ObjectInfo{}, mapError("stat immutable", key, statErr)
			}
			if info.Size != size || normalizeS3SHA256(info.ChecksumSHA256) != checksum {
				return appfont.ObjectInfo{}, fmt.Errorf("%w: existing immutable object %q does not match expected content", appfont.ErrObjectStorageUnavailable, key)
			}
			if info.VersionID == "" || info.VersionID == "null" {
				return appfont.ObjectInfo{}, fmt.Errorf("%w: bucket versioning is required for immutable artifacts", appfont.ErrObjectStorageUnavailable)
			}
			return objectInfo(key, info), nil
		}
		return appfont.ObjectInfo{}, mapError("put", key, err)
	}
	serverChecksum := normalizeS3SHA256(uploaded.ChecksumSHA256)
	if serverChecksum == "" {
		info, statErr := s.client.StatObject(ctx, s.bucket, key, miniogo.StatObjectOptions{Checksum: true, VersionID: uploaded.VersionID})
		if statErr != nil {
			return appfont.ObjectInfo{}, mapError("verify put", key, statErr)
		}
		serverChecksum = normalizeS3SHA256(info.ChecksumSHA256)
		if uploaded.VersionID == "" {
			uploaded.VersionID = info.VersionID
		}
	}
	if serverChecksum == "" || serverChecksum != checksum {
		return appfont.ObjectInfo{}, fmt.Errorf("%w: object store did not verify SHA-256 for %q", appfont.ErrObjectStorageUnavailable, key)
	}
	if uploaded.VersionID == "" || uploaded.VersionID == "null" {
		return appfont.ObjectInfo{}, fmt.Errorf("%w: bucket versioning is required for immutable artifacts", appfont.ErrObjectStorageUnavailable)
	}
	return appfont.ObjectInfo{
		Key: key, VersionID: uploaded.VersionID, ETag: uploaded.ETag, SizeBytes: size,
		ContentType: options.ContentType, ChecksumSHA256: checksum, ChecksumVerified: true,
		LastModified: time.Now().UTC(),
	}, nil
}

func (s *Store) PublicURL(ctx context.Context, key, versionID string) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	if s.publicBaseURL != nil {
		location := *s.publicBaseURL
		escapedPath := strings.TrimRight(location.EscapedPath(), "/") + "/" + escapeObjectKey(key)
		location.Path, _ = url.PathUnescape(escapedPath)
		location.RawPath = escapedPath
		parameters := url.Values{}
		if versionID != "" {
			parameters.Set("versionId", versionID)
		}
		location.RawQuery = parameters.Encode()
		return location.String(), nil
	}
	parameters := url.Values{}
	if versionID != "" {
		parameters.Set("versionId", versionID)
	}
	location, err := s.client.PresignedGetObject(ctx, s.bucket, key, s.presignExpiry, parameters)
	if err != nil {
		return "", mapError("presign", key, err)
	}
	return location.String(), nil
}

func (s *Store) BucketExists(ctx context.Context) (bool, error) {
	if err := s.validate(); err != nil {
		return false, err
	}
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return false, fmt.Errorf("%w: check minio bucket %q: %w", appfont.ErrObjectStorageUnavailable, s.bucket, err)
	}
	return exists, nil
}

func (s *Store) BucketVersioningEnabled(ctx context.Context) (bool, error) {
	if err := s.validate(); err != nil {
		return false, err
	}
	configuration, err := s.client.GetBucketVersioning(ctx, s.bucket)
	if err != nil {
		return false, fmt.Errorf("%w: check MinIO bucket versioning for %q: %w", appfont.ErrObjectStorageUnavailable, s.bucket, err)
	}
	return configuration.Enabled(), nil
}

func (s *Store) ListObjects(
	ctx context.Context,
	prefix, cursor string,
	limit int,
) (appcleanup.ObjectPage, error) {
	if err := s.validate(); err != nil {
		return appcleanup.ObjectPage{}, err
	}
	if ctx == nil {
		return appcleanup.ObjectPage{}, errors.New("list minio objects: context is required")
	}
	if limit <= 0 || limit > 1_000 {
		return appcleanup.ObjectPage{}, errors.New("minio object page size must be between 1 and 1000")
	}

	requestLimit := limit + 1
	if requestLimit > 1_000 {
		requestLimit = 1_000
	}
	listCtx, cancel := context.WithCancel(ctx)
	stream := s.client.ListObjects(listCtx, s.bucket, miniogo.ListObjectsOptions{
		Prefix: prefix, Recursive: true, MaxKeys: requestLimit, StartAfter: cursor,
	})
	objects := make([]appcleanup.Object, 0, limit)
	truncated := false
	canceledForPage := false
	var streamErr error
	for info := range stream {
		if info.Err != nil {
			if canceledForPage && ctx.Err() == nil && errors.Is(info.Err, context.Canceled) {
				continue
			}
			if streamErr == nil {
				streamErr = info.Err
			}
			continue
		}
		if len(objects) < limit {
			objects = append(objects, appcleanup.Object{
				Key: info.Key, ETag: info.ETag, SizeBytes: info.Size, LastModified: info.LastModified,
			})
			if len(objects) == limit && limit == 1_000 {
				truncated = true
				canceledForPage = true
				cancel()
			}
			continue
		}
		truncated = true
		canceledForPage = true
		cancel()
	}
	cancel()
	if streamErr != nil {
		return appcleanup.ObjectPage{}, mapError("list", prefix, streamErr)
	}
	if err := ctx.Err(); err != nil {
		return appcleanup.ObjectPage{}, mapError("list", prefix, err)
	}

	page := appcleanup.ObjectPage{Objects: objects, HasMore: truncated}
	if truncated && len(objects) > 0 {
		page.NextCursor = objects[len(objects)-1].Key
	}
	return page, nil
}

func (s *Store) DeleteObject(ctx context.Context, object appcleanup.Object) error {
	if err := s.validate(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("delete minio object: context is required")
	}
	key := strings.TrimSpace(object.Key)
	if key == "" {
		return errors.New("delete minio object: key is required")
	}
	current, err := s.client.StatObject(ctx, s.bucket, key, miniogo.StatObjectOptions{Checksum: true})
	if err != nil {
		mapped := mapError("stat before delete", key, err)
		if errors.Is(mapped, appfont.ErrObjectNotFound) {
			return appcleanup.ErrObjectNotFound
		}
		return mapped
	}
	// HEAD timestamps are second-precision; orphan candidates are old enough that
	// a replacement cannot legitimately share their truncated timestamp.
	listedModified := object.LastModified.UTC().Truncate(time.Second)
	currentModified := current.LastModified.UTC().Truncate(time.Second)
	if current.ETag != object.ETag || current.Size != object.SizeBytes || !currentModified.Equal(listedModified) {
		return fmt.Errorf(
			"%w: listed etag=%q size=%d modified=%s; current etag=%q size=%d modified=%s",
			appcleanup.ErrObjectChanged,
			object.ETag,
			object.SizeBytes,
			object.LastModified.UTC().Format(time.RFC3339Nano),
			current.ETag,
			current.Size,
			current.LastModified.UTC().Format(time.RFC3339Nano),
		)
	}
	if current.VersionID == "" || current.VersionID == "null" {
		return fmt.Errorf("delete generated object %q safely: bucket versioning is required", key)
	}
	// Keep a delete marker current before removing the validated version. A
	// pre-versioning null version may still exist and must never become visible.
	if err := s.client.RemoveObject(ctx, s.bucket, key, miniogo.RemoveObjectOptions{}); err != nil {
		return mapError("create delete marker", key, err)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, miniogo.RemoveObjectOptions{VersionID: current.VersionID}); err != nil {
		mapped := mapError("delete validated version", key, err)
		if errors.Is(mapped, appfont.ErrObjectNotFound) {
			return nil
		}
		return mapped
	}
	return nil
}

func (s *Store) validate() error {
	if s == nil || s.client == nil || s.bucket == "" {
		return appfont.ErrObjectStorageUnavailable
	}
	return nil
}

func objectInfo(key string, info miniogo.ObjectInfo) appfont.ObjectInfo {
	serverChecksum := normalizeS3SHA256(info.ChecksumSHA256)
	checksum := serverChecksum
	if checksum == "" {
		checksum = metadataSHA256(info.UserMetadata)
	}
	return appfont.ObjectInfo{
		Key: key, VersionID: info.VersionID, ETag: info.ETag, SizeBytes: info.Size,
		ContentType: info.ContentType, ChecksumSHA256: checksum, ChecksumVerified: serverChecksum != "",
		LastModified: info.LastModified,
	}
}

func isPreconditionFailed(err error) bool {
	response, ok := minioErrorResponse(err)
	return ok && (response.Code == "PreconditionFailed" || response.StatusCode == 412)
}

func mapError(operation, key string, err error) error {
	if err == nil {
		return nil
	}
	classification := appfont.ErrObjectStorageUnavailable
	if response, ok := minioErrorResponse(err); ok {
		switch response.Code {
		case "NoSuchKey", "NoSuchObject", "NoSuchVersion":
			classification = appfont.ErrObjectNotFound
		}
	}
	return fmt.Errorf("%w: %s minio object %q: %w", classification, operation, key, err)
}

func minioErrorResponse(err error) (miniogo.ErrorResponse, bool) {
	var response miniogo.ErrorResponse
	if errors.As(err, &response) {
		return response, true
	}
	var responsePointer *miniogo.ErrorResponse
	if errors.As(err, &responsePointer) && responsePointer != nil {
		return *responsePointer, true
	}
	return miniogo.ErrorResponse{}, false
}

func normalizeS3SHA256(value string) string {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return ""
	}
	return hex.EncodeToString(decoded)
}

func metadataSHA256(metadata miniogo.StringMap) string {
	for _, key := range []string{"sha256", "x-amz-meta-sha256"} {
		if checksum := normalizeCustomSHA256(metadata[key]); checksum != "" {
			return checksum
		}
	}

	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		if strings.EqualFold(key, "sha256") || strings.EqualFold(key, "x-amz-meta-sha256") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if checksum := normalizeCustomSHA256(metadata[key]); checksum != "" {
			return checksum
		}
	}
	return ""
}

func normalizeCustomSHA256(value string) string {
	value = strings.TrimSpace(value)
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return ""
	}
	return hex.EncodeToString(decoded)
}

func requiredSHA256(value string) (string, error) {
	checksum := normalizeCustomSHA256(value)
	if checksum != "" {
		return checksum, nil
	}
	if strings.TrimSpace(value) == "" {
		return "", errors.New("object checksum is required")
	}
	return "", errors.New("object checksum must be a 64-character SHA-256 hex value")
}

func parsePublicBaseURL(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("parse minio public base URL: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, errors.New("minio public base URL must use HTTP or HTTPS")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, errors.New("minio public base URL must include a host")
	}
	if parsed.User != nil {
		return nil, errors.New("minio public base URL must not include user info")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return nil, errors.New("minio public base URL must not include a query string")
	}
	if parsed.Fragment != "" || strings.Contains(value, "#") {
		return nil, errors.New("minio public base URL must not include a fragment")
	}
	return parsed, nil
}

func escapeObjectKey(key string) string {
	parts := strings.Split(strings.TrimLeft(key, "/"), "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
