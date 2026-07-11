package objectversionbackfill

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	miniogo "github.com/minio/minio-go/v7"
)

func TestChecksumFromInfo(t *testing.T) {
	tests := []struct {
		name      string
		algorithm ChecksumAlgorithm
		rawLength int
		set       func(*miniogo.ObjectInfo, string)
	}{
		{name: "CRC32", algorithm: ChecksumCRC32, rawLength: 4, set: func(info *miniogo.ObjectInfo, value string) { info.ChecksumCRC32 = value }},
		{name: "CRC32C", algorithm: ChecksumCRC32C, rawLength: 4, set: func(info *miniogo.ObjectInfo, value string) { info.ChecksumCRC32C = value }},
		{name: "SHA1", algorithm: ChecksumSHA1, rawLength: 20, set: func(info *miniogo.ObjectInfo, value string) { info.ChecksumSHA1 = value }},
		{name: "SHA256", algorithm: ChecksumSHA256, rawLength: 32, set: func(info *miniogo.ObjectInfo, value string) { info.ChecksumSHA256 = value }},
		{name: "CRC64NVME", algorithm: ChecksumCRC64NVME, rawLength: 8, set: func(info *miniogo.ObjectInfo, value string) { info.ChecksumCRC64NVME = value }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(strings.Repeat("a", test.rawLength))
			encoded := base64.StdEncoding.EncodeToString(raw)
			info := miniogo.ObjectInfo{Metadata: make(http.Header)}
			test.set(&info, encoded)
			checksumType, err := minioChecksumType(test.algorithm)
			if err != nil {
				t.Fatalf("minioChecksumType: %v", err)
			}
			info.Metadata.Set(checksumType.Key(), encoded)
			got, err := checksumFromInfo(info)
			if err != nil {
				t.Fatalf("checksumFromInfo: %v", err)
			}
			if got != (Checksum{Algorithm: test.algorithm, Value: encoded}) {
				t.Fatalf("checksum = %#v", got)
			}
		})
	}

	if got, err := checksumFromInfo(miniogo.ObjectInfo{Metadata: make(http.Header)}); err != nil || !got.empty() {
		t.Fatalf("empty checksum = %#v, %v", got, err)
	}
	invalid := miniogo.ObjectInfo{ChecksumSHA256: "not-base64", Metadata: make(http.Header)}
	if _, err := checksumFromInfo(invalid); err == nil {
		t.Fatal("invalid checksum was accepted")
	}
	multiple := miniogo.ObjectInfo{
		ChecksumCRC32: base64.StdEncoding.EncodeToString(make([]byte, 4)),
		ChecksumSHA1:  base64.StdEncoding.EncodeToString(make([]byte, 20)),
		Metadata:      make(http.Header),
	}
	if _, err := checksumFromInfo(multiple); err == nil {
		t.Fatal("multiple checksum algorithms were accepted")
	}
	unknown := miniogo.ObjectInfo{Metadata: http.Header{"X-Amz-Checksum-Future": {"value"}}}
	if _, err := checksumFromInfo(unknown); err == nil {
		t.Fatal("unknown checksum header was accepted")
	}
	composite := miniogo.ObjectInfo{Metadata: http.Header{"X-Amz-Checksum-Type": {"COMPOSITE"}}}
	if _, err := checksumFromInfo(composite); err == nil {
		t.Fatal("composite checksum state was accepted")
	}
}

func TestNormalizeETag(t *testing.T) {
	for _, input := range []string{"abc123", `"abc123"`} {
		normalized, err := normalizeETag(input)
		if err != nil {
			t.Fatalf("normalizeETag(%q): %v", input, err)
		}
		if normalized != "abc123" {
			t.Fatalf("normalizeETag(%q) = %q", input, normalized)
		}
	}
	for _, input := range []string{"", "bad\r\netag", `bad"etag`, `"missing-end`, `missing-start"`} {
		if _, err := normalizeETag(input); err == nil {
			t.Fatalf("normalizeETag(%q) succeeded", input)
		}
	}
}

func TestSecurityFromHeadersRejectsEncryptionAndObjectLockIndicators(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   SecurityState
	}{
		{name: "SSE-S3", header: "X-Amz-Server-Side-Encryption", want: SecurityState{Encryption: true}},
		{name: "SSE-KMS key", header: "X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id", want: SecurityState{Encryption: true}},
		{name: "KMS context", header: "X-Amz-Server-Side-Encryption-Context", want: SecurityState{Encryption: true}},
		{name: "SSE-C algorithm", header: "X-Amz-Server-Side-Encryption-Customer-Algorithm", want: SecurityState{Encryption: true}},
		{name: "SSE-C key digest", header: "X-Amz-Server-Side-Encryption-Customer-Key-Md5", want: SecurityState{Encryption: true}},
		{name: "bucket key", header: "X-Amz-Server-Side-Encryption-Bucket-Key-Enabled", want: SecurityState{Encryption: true}},
		{name: "retention mode", header: "X-Amz-Object-Lock-Mode", want: SecurityState{ObjectLock: true}},
		{name: "retention date", header: "X-Amz-Object-Lock-Retain-Until-Date", want: SecurityState{ObjectLock: true}},
		{name: "legal hold", header: "X-Amz-Object-Lock-Legal-Hold", want: SecurityState{ObjectLock: true}},
		{name: "MinIO encryption", header: "X-Minio-Internal-Server-Side-Encryption-Seal-Algorithm", want: SecurityState{Encryption: true}},
		{name: "MinIO object lock", header: "X-Minio-Internal-Object-Lock-State", want: SecurityState{ObjectLock: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set(test.header, "sensitive-value-must-not-be-retained")
			if got := securityFromHeaders(headers); got != test.want {
				t.Fatalf("securityFromHeaders(%q) = %#v, want %#v", test.header, got, test.want)
			}
		})
	}

	benign := make(http.Header)
	benign.Set("X-Amz-Meta-Encryption-Label", "user-defined")
	benign.Set("Cache-Control", "public")
	if got := securityFromHeaders(benign); got != (SecurityState{}) {
		t.Fatalf("benign headers reported unsafe state: %#v", got)
	}
}

func TestPutOptionsPreserveHeadersMetadataAndTags(t *testing.T) {
	expires := "Tue, 14 Nov 2023 23:13:20 GMT"
	metadata := Metadata{
		Headers: map[string]string{
			"Content-Type":                    "font/ttf",
			"Cache-Control":                   "public, max-age=3600",
			"Content-Encoding":                "identity",
			"Content-Language":                "en",
			"Content-Disposition":             `attachment; filename="font.ttf"`,
			"Expires":                         expires,
			"X-Amz-Storage-Class":             "STANDARD",
			"X-Amz-Website-Redirect-Location": "/fonts/current.ttf",
		},
		User: map[string]string{
			"content-type": "legacy-label",
			"owner":        "fonts",
		},
	}
	options, err := putOptionsFromMetadata(metadata, map[string]string{"family": "Inter"})
	if err != nil {
		t.Fatalf("putOptionsFromMetadata: %v", err)
	}
	if options.ContentType != "font/ttf" || options.CacheControl != "public, max-age=3600" ||
		options.ContentEncoding != "identity" || options.ContentLanguage != "en" ||
		options.ContentDisposition != `attachment; filename="font.ttf"` ||
		options.StorageClass != "STANDARD" || options.WebsiteRedirectLocation != "/fonts/current.ttf" {
		t.Fatalf("options = %#v", options)
	}
	wantExpiry, err := time.Parse(time.RFC1123, expires)
	if err != nil {
		t.Fatalf("parse expected expiry: %v", err)
	}
	if !options.Expires.Equal(wantExpiry) {
		t.Fatalf("expiry = %v, want %v", options.Expires, wantExpiry)
	}
	headers := options.Header()
	if headers.Get("X-Amz-Meta-Content-Type") != "legacy-label" || headers.Get("X-Amz-Meta-Owner") != "fonts" {
		t.Fatalf("user metadata headers = %#v", headers)
	}
	if headers.Get("X-Amz-Tagging") != "family=Inter" {
		t.Fatalf("tagging header = %q", headers.Get("X-Amz-Tagging"))
	}
}

func TestMissingSecurityConfigurationErrors(t *testing.T) {
	if !isMissingObjectLockConfiguration(miniogo.ErrorResponse{Code: "NoSuchObjectLockConfiguration"}) {
		t.Fatal("object-lock not-found response was not recognized")
	}
	if !isMissingBucketEncryptionConfiguration(miniogo.ErrorResponse{Code: "ServerSideEncryptionConfigurationNotFoundError"}) {
		t.Fatal("encryption not-found response was not recognized")
	}
	unknown := miniogo.ErrorResponse{Code: "AccessDenied"}
	if isMissingObjectLockConfiguration(unknown) || isMissingBucketEncryptionConfiguration(unknown) {
		t.Fatal("security inspection would treat AccessDenied as an absent configuration")
	}
}
