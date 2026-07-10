package minio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
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
	publicBaseURL string
	presignExpiry time.Duration
}

func New(cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("minio endpoint is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("minio bucket is required")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("minio access key and secret key are required")
	}
	client, err := miniogo.New(strings.TrimSpace(cfg.Endpoint), &miniogo.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken),
		Secure: cfg.Secure,
		Region: strings.TrimSpace(cfg.Region),
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}
	if cfg.PresignExpiry <= 0 {
		cfg.PresignExpiry = time.Hour
	}
	return &Store{
		client: client, bucket: cfg.Bucket,
		publicBaseURL: strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/"),
		presignExpiry: cfg.PresignExpiry,
	}, nil
}

func (s *Store) StatObject(ctx context.Context, key string) (appfont.ObjectInfo, error) {
	if err := s.validate(); err != nil {
		return appfont.ObjectInfo{}, err
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, miniogo.StatObjectOptions{})
	if err != nil {
		return appfont.ObjectInfo{}, mapError("stat", key, err)
	}
	return objectInfo(key, info), nil
}

func (s *Store) OpenObject(ctx context.Context, key string) (io.ReadCloser, appfont.ObjectInfo, error) {
	info, err := s.StatObject(ctx, key)
	if err != nil {
		return nil, appfont.ObjectInfo{}, err
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, miniogo.GetObjectOptions{})
	if err != nil {
		return nil, appfont.ObjectInfo{}, mapError("open", key, err)
	}
	return object, info, nil
}

func (s *Store) PutObject(ctx context.Context, key string, reader io.Reader, size int64, options appfont.PutObjectOptions) (appfont.ObjectInfo, error) {
	if err := s.validate(); err != nil {
		return appfont.ObjectInfo{}, err
	}
	if size <= 0 {
		return appfont.ObjectInfo{}, errors.New("object size must be greater than zero")
	}
	metadata := make(map[string]string, 1)
	if options.ChecksumSHA256 != "" {
		metadata["sha256"] = options.ChecksumSHA256
	}
	uploaded, err := s.client.PutObject(ctx, s.bucket, key, reader, size, miniogo.PutObjectOptions{
		ContentType:  options.ContentType,
		UserMetadata: metadata,
	})
	if err != nil {
		return appfont.ObjectInfo{}, mapError("put", key, err)
	}
	return appfont.ObjectInfo{
		Key: key, ETag: uploaded.ETag, SizeBytes: size,
		ContentType: options.ContentType, ChecksumSHA256: options.ChecksumSHA256,
		LastModified: time.Now().UTC(),
	}, nil
}

func (s *Store) PublicURL(ctx context.Context, key string) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	if s.publicBaseURL != "" {
		return s.publicBaseURL + "/" + escapeObjectKey(key), nil
	}
	location, err := s.client.PresignedGetObject(ctx, s.bucket, key, s.presignExpiry, url.Values{})
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
		return false, fmt.Errorf("check minio bucket %q: %w", s.bucket, err)
	}
	return exists, nil
}

func (s *Store) validate() error {
	if s == nil || s.client == nil || s.bucket == "" {
		return appfont.ErrObjectStorageUnavailable
	}
	return nil
}

func objectInfo(key string, info miniogo.ObjectInfo) appfont.ObjectInfo {
	checksum := info.ChecksumSHA256
	if checksum == "" {
		for key, value := range info.UserMetadata {
			if strings.EqualFold(key, "sha256") {
				checksum = value
				break
			}
		}
	}
	return appfont.ObjectInfo{
		Key: key, ETag: info.ETag, SizeBytes: info.Size,
		ContentType: info.ContentType, ChecksumSHA256: checksum, LastModified: info.LastModified,
	}
}

func mapError(operation, key string, err error) error {
	response := miniogo.ToErrorResponse(err)
	switch response.Code {
	case "NoSuchKey", "NoSuchObject", "NoSuchBucket", "NotFound":
		return fmt.Errorf("%w: %s", appfont.ErrObjectNotFound, key)
	default:
		return fmt.Errorf("%s minio object %q: %w", operation, key, err)
	}
}

func escapeObjectKey(key string) string {
	parts := strings.Split(strings.TrimLeft(key, "/"), "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
