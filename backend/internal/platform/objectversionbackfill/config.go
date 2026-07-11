package objectversionbackfill

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/minio/minio-go/v7/pkg/s3utils"
)

const (
	EnvEndpoint    = "EMFONT_MINIO_BOOTSTRAP_ENDPOINT"
	EnvBucket      = "EMFONT_MINIO_BUCKET"
	EnvAccessKey   = "MINIO_ROOT_USER"
	EnvSecretKey   = "MINIO_ROOT_PASSWORD"
	EnvSecure      = "EMFONT_MINIO_SECURE"
	EnvRegion      = "EMFONT_MINIO_REGION"
	EnvConcurrency = "EMFONT_MINIO_BACKFILL_CONCURRENCY"
)

// Config contains all environment-derived settings used by the helper.
type Config struct {
	Endpoint    string
	Bucket      string
	AccessKey   string
	SecretKey   string
	Secure      bool
	Region      string
	Concurrency int
}

// LoadConfig reads and validates the helper's complete environment contract.
// Credential values are never included in returned errors.
func LoadConfig(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("environment lookup is required")
	}
	required := func(name string, trim bool) (string, error) {
		value, exists := lookup(name)
		if trim {
			value = strings.TrimSpace(value)
		}
		if !exists || value == "" {
			return "", fmt.Errorf("%s is required", name)
		}
		return value, nil
	}

	rawEndpoint, err := required(EnvEndpoint, true)
	if err != nil {
		return Config{}, err
	}
	bucket, err := required(EnvBucket, true)
	if err != nil {
		return Config{}, err
	}
	if err := s3utils.CheckValidBucketName(bucket); err != nil {
		return Config{}, fmt.Errorf("%s is invalid", EnvBucket)
	}
	accessKey, err := required(EnvAccessKey, false)
	if err != nil {
		return Config{}, err
	}
	secretKey, err := required(EnvSecretKey, false)
	if err != nil {
		return Config{}, err
	}
	rawSecure, err := required(EnvSecure, true)
	if err != nil {
		return Config{}, err
	}
	secure, err := strconv.ParseBool(rawSecure)
	if err != nil {
		return Config{}, fmt.Errorf("%s must be true or false", EnvSecure)
	}
	region, exists := lookup(EnvRegion)
	if !exists {
		return Config{}, fmt.Errorf("%s must be set (it may be empty)", EnvRegion)
	}
	region = strings.TrimSpace(region)
	rawConcurrency, err := required(EnvConcurrency, true)
	if err != nil {
		return Config{}, err
	}
	concurrency, err := strconv.Atoi(rawConcurrency)
	if err != nil || concurrency < 1 || concurrency > MaxConcurrency {
		return Config{}, fmt.Errorf("%s must be an integer between 1 and %d", EnvConcurrency, MaxConcurrency)
	}

	endpoint, err := normalizeEndpoint(rawEndpoint)
	if err != nil {
		return Config{}, fmt.Errorf("%s is invalid: %w", EnvEndpoint, err)
	}
	return Config{
		Endpoint: endpoint, Bucket: bucket, AccessKey: accessKey, SecretKey: secretKey,
		Secure: secure, Region: region, Concurrency: concurrency,
	}, nil
}

func normalizeEndpoint(raw string) (string, error) {
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, "/?#@") ||
		strings.ContainsAny(raw, " \t\r\n") {
		return "", errors.New("endpoint must contain only a host and optional port")
	}
	return raw, nil
}
