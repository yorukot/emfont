package minio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	appcleanup "github.com/emfont/emfont/backend/internal/controller/application/fontcleanup"
	miniogo "github.com/minio/minio-go/v7"
)

func TestObjectInfoNormalizesChecksumSHA256(t *testing.T) {
	standardBytes := bytes.Repeat([]byte{0xab}, 32)
	standardBase64 := base64.StdEncoding.EncodeToString(standardBytes)
	standardHex := strings.Repeat("ab", 32)
	customHex := strings.Repeat("cd", 32)

	tests := []struct {
		name         string
		info         miniogo.ObjectInfo
		want         string
		wantVerified bool
	}{
		{
			name: "S3 base64 checksum",
			info: miniogo.ObjectInfo{ChecksumSHA256: standardBase64},
			want: standardHex, wantVerified: true,
		},
		{
			name: "custom metadata hex",
			info: miniogo.ObjectInfo{UserMetadata: miniogo.StringMap{"Sha256": strings.ToUpper(customHex)}},
			want: customHex,
		},
		{
			name: "S3 checksum takes precedence",
			info: miniogo.ObjectInfo{
				ChecksumSHA256: standardBase64,
				UserMetadata:   miniogo.StringMap{"sha256": customHex},
			},
			want:         standardHex,
			wantVerified: true,
		},
		{
			name: "invalid S3 checksum falls back to metadata",
			info: miniogo.ObjectInfo{
				ChecksumSHA256: "not-base64",
				UserMetadata:   miniogo.StringMap{"SHA256": strings.ToUpper(customHex)},
			},
			want: customHex,
		},
		{
			name: "exact metadata key has deterministic precedence",
			info: miniogo.ObjectInfo{UserMetadata: miniogo.StringMap{
				"sha256": standardHex,
				"SHA256": customHex,
			}},
			want: standardHex,
		},
		{
			name: "full metadata header key",
			info: miniogo.ObjectInfo{UserMetadata: miniogo.StringMap{
				"X-Amz-Meta-Sha256": strings.ToUpper(customHex),
			}},
			want: customHex,
		},
		{
			name: "invalid custom metadata is rejected",
			info: miniogo.ObjectInfo{UserMetadata: miniogo.StringMap{"sha256": "not-a-sha256"}},
			want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := objectInfo("fonts/test.woff2", test.info)
			if got.ChecksumSHA256 != test.want {
				t.Fatalf("ChecksumSHA256 = %q, want %q", got.ChecksumSHA256, test.want)
			}
			if got.ChecksumVerified != test.wantVerified {
				t.Fatalf("ChecksumVerified = %t, want %t", got.ChecksumVerified, test.wantVerified)
			}
		})
	}
}

func TestPutObjectRejectsMissingOrMalformedChecksumBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	store := newHTTPTestStore(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))

	for _, test := range []struct {
		name     string
		checksum string
		message  string
	}{
		{name: "missing", message: "checksum is required"},
		{name: "whitespace", checksum: "  ", message: "checksum is required"},
		{name: "malformed", checksum: "invalid", message: "64-character SHA-256"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.PutObject(
				context.Background(),
				"fonts/test.woff2",
				strings.NewReader("wOF2"),
				4,
				appfont.PutObjectOptions{ChecksumSHA256: test.checksum},
			)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("PutObject error = %v, want checksum validation error containing %q", err, test.message)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("storage requests = %d, want 0", requests.Load())
	}
}

func TestPublicBaseURLValidationAndJoining(t *testing.T) {
	for _, test := range []struct {
		name    string
		baseURL string
		want    string
		wantErr string
	}{
		{
			name:    "HTTP base path and escaped key",
			baseURL: "http://cdn.example.test/fonts/base/",
			want:    "http://cdn.example.test/fonts/base/family%20name/weight%3F400.woff2?versionId=version%2F1%3Fpreview",
		},
		{name: "missing scheme", baseURL: "cdn.example.test/fonts", wantErr: "must use HTTP or HTTPS"},
		{name: "unsupported scheme", baseURL: "ftp://cdn.example.test/fonts", wantErr: "must use HTTP or HTTPS"},
		{name: "missing host", baseURL: "https:/fonts", wantErr: "must include a host"},
		{name: "user info", baseURL: "https://user:secret@cdn.example.test/fonts", wantErr: "must not include user info"},
		{name: "query", baseURL: "https://cdn.example.test/fonts?token=secret", wantErr: "must not include a query string"},
		{name: "empty query", baseURL: "https://cdn.example.test/fonts?", wantErr: "must not include a query string"},
		{name: "fragment", baseURL: "https://cdn.example.test/fonts#fragment", wantErr: "must not include a fragment"},
		{name: "empty fragment", baseURL: "https://cdn.example.test/fonts#", wantErr: "must not include a fragment"},
		{name: "malformed", baseURL: "https://cdn.example.test/%", wantErr: "parse minio public base URL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := New(Config{
				Endpoint: "minio.example.test:9000", AccessKey: "test-access-key", SecretKey: "test-secret-key",
				Bucket: "test-bucket", PublicBaseURL: test.baseURL,
			})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("New error = %v, want error containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, err := store.PublicURL(context.Background(), "family name/weight?400.woff2", "version/1?preview")
			if err != nil {
				t.Fatalf("PublicURL: %v", err)
			}
			if got != test.want {
				t.Fatalf("PublicURL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNewRejectsURLFormEndpoint(t *testing.T) {
	_, err := New(Config{
		Endpoint: "https://minio.example.test:9000", AccessKey: "test-access-key",
		SecretKey: "test-secret-key", Bucket: "test-bucket",
	})
	if err == nil || !strings.Contains(err.Error(), "host and optional port") {
		t.Fatalf("New error = %v, want host-and-port validation error", err)
	}
}

func TestMapErrorClassifiesOnlyMissingObjectsAsNotFound(t *testing.T) {
	tests := []struct {
		code         string
		wantNotFound bool
	}{
		{code: "NoSuchKey", wantNotFound: true},
		{code: "NoSuchObject", wantNotFound: true},
		{code: "NoSuchVersion", wantNotFound: true},
		{code: "NoSuchBucket"},
		{code: "NotFound"},
		{code: "AccessDenied"},
		{code: "InvalidAccessKeyId"},
		{code: "ServiceUnavailable"},
	}

	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			response := miniogo.ErrorResponse{Code: test.code, Message: "storage response"}
			cause := fmt.Errorf("request failed: %w", response)
			err := mapError("stat", "fonts/test.woff2", cause)

			if got := errors.Is(err, appfont.ErrObjectNotFound); got != test.wantNotFound {
				t.Fatalf("errors.Is(ErrObjectNotFound) = %t, want %t: %v", got, test.wantNotFound, err)
			}
			if got := errors.Is(err, appfont.ErrObjectStorageUnavailable); got == test.wantNotFound {
				t.Fatalf("errors.Is(ErrObjectStorageUnavailable) = %t, want %t: %v", got, !test.wantNotFound, err)
			}
			if !errors.Is(err, cause) || !errors.Is(err, response) {
				t.Fatalf("mapped error does not wrap its cause: %v", err)
			}
			if !strings.Contains(err.Error(), `stat minio object "fonts/test.woff2"`) {
				t.Fatalf("mapped error lacks operation and key context: %v", err)
			}
		})
	}
}

func TestMapErrorPreservesContextError(t *testing.T) {
	cause := fmt.Errorf("GET request: %w", context.DeadlineExceeded)
	err := mapError("open", "fonts/test.woff2", cause)

	if !errors.Is(err, appfont.ErrObjectStorageUnavailable) {
		t.Fatalf("error = %v, want ErrObjectStorageUnavailable", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want wrapped context deadline", err)
	}
}

func TestOpenObjectStatsSameHandleBeforeReturning(t *testing.T) {
	data := []byte("woff2-test-data")
	etag := "etag-v1"
	lastModified := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	checksumBytes := bytes.Repeat([]byte{0xab}, 32)
	checksumBase64 := base64.StdEncoding.EncodeToString(checksumBytes)
	checksumHex := strings.Repeat("ab", 32)
	var headCalls atomic.Int32
	var getCalls atomic.Int32

	store := newHTTPTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test-bucket/fonts/test.woff2" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		setObjectHeaders(w.Header(), len(data), etag, lastModified, checksumBase64)
		switch r.Method {
		case http.MethodHead:
			headCalls.Add(1)
			if got := r.Header.Get("X-Amz-Checksum-Mode"); got != "ENABLED" {
				t.Errorf("HEAD checksum mode = %q, want ENABLED", got)
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			getCalls.Add(1)
			if got := r.Header.Get("If-Match"); got != `"etag-v1"` {
				t.Errorf("GET If-Match = %q, want %q", got, `"etag-v1"`)
			}
			if got := r.Header.Get("X-Amz-Checksum-Mode"); got != "ENABLED" {
				t.Errorf("GET checksum mode = %q, want ENABLED", got)
			}
			_, _ = w.Write(data)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))

	reader, info, err := store.OpenObject(context.Background(), "fonts/test.woff2", "")
	if err != nil {
		t.Fatalf("OpenObject: %v", err)
	}
	if headCalls.Load() != 1 || getCalls.Load() != 0 {
		t.Fatalf("calls after OpenObject: HEAD=%d GET=%d, want HEAD=1 GET=0", headCalls.Load(), getCalls.Load())
	}
	if info.Key != "fonts/test.woff2" || info.ETag != etag || info.SizeBytes != int64(len(data)) {
		t.Fatalf("object info = %#v", info)
	}
	if info.ContentType != "font/woff2" || info.ChecksumSHA256 != checksumHex || !info.LastModified.Equal(lastModified) {
		t.Fatalf("object metadata = %#v", info)
	}

	readBack, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		t.Fatalf("read object: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close object: %v", closeErr)
	}
	if !bytes.Equal(readBack, data) {
		t.Fatalf("read object = %q, want %q", readBack, data)
	}
	if getCalls.Load() != 1 {
		t.Fatalf("GET calls = %d, want 1", getCalls.Load())
	}
}

func TestOpenObjectSurfacesLazyStatErrors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		code         string
		wantNotFound bool
	}{
		{name: "missing object", status: http.StatusNotFound, code: "NoSuchKey", wantNotFound: true},
		{name: "missing bucket", status: http.StatusNotFound, code: "NoSuchBucket"},
		{name: "access denied", status: http.StatusForbidden, code: "AccessDenied"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var headCalls atomic.Int32
			var getCalls atomic.Int32
			store := newHTTPTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodHead:
					headCalls.Add(1)
					w.Header().Set("X-Minio-Error-Code", test.code)
					w.Header().Set("X-Minio-Error-Desc", test.name)
					w.WriteHeader(test.status)
				case http.MethodGet:
					getCalls.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				default:
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))

			reader, info, err := store.OpenObject(context.Background(), "fonts/missing.woff2", "")
			if err == nil {
				t.Fatal("OpenObject returned nil error")
			}
			if reader != nil || info != (appfont.ObjectInfo{}) {
				t.Fatalf("OpenObject returned reader=%v info=%#v on failure", reader, info)
			}
			if got := errors.Is(err, appfont.ErrObjectNotFound); got != test.wantNotFound {
				t.Fatalf("errors.Is(ErrObjectNotFound) = %t, want %t: %v", got, test.wantNotFound, err)
			}
			if got := errors.Is(err, appfont.ErrObjectStorageUnavailable); got == test.wantNotFound {
				t.Fatalf("errors.Is(ErrObjectStorageUnavailable) = %t, want %t: %v", got, !test.wantNotFound, err)
			}
			if headCalls.Load() != 1 || getCalls.Load() != 0 {
				t.Fatalf("calls: HEAD=%d GET=%d, want HEAD=1 GET=0", headCalls.Load(), getCalls.Load())
			}
		})
	}
}

func TestListObjectsReturnsOneBoundedPage(t *testing.T) {
	lastModified := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	allObjects := []testListObject{
		{Key: "_generated/a.woff2", LastModified: lastModified, ETag: "etag-a", Size: 1},
		{Key: "_generated/b.woff2", LastModified: lastModified, ETag: "etag-b", Size: 2},
		{Key: "_generated/c.woff2", LastModified: lastModified, ETag: "etag-c", Size: 3},
	}
	var requests atomic.Int32
	store := newHTTPTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/test-bucket/" || r.URL.Query().Get("list-type") != "2" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		if got := r.URL.Query().Get("prefix"); got != appcleanup.GeneratedObjectPrefix {
			t.Errorf("prefix = %q, want %q", got, appcleanup.GeneratedObjectPrefix)
		}
		if got := r.URL.Query().Get("max-keys"); got != "3" {
			t.Errorf("max-keys = %q, want 3", got)
		}
		startAfter := r.URL.Query().Get("start-after")
		listed := make([]testListObject, 0, len(allObjects))
		for _, object := range allObjects {
			if object.Key > startAfter {
				listed = append(listed, object)
			}
		}
		writeTestObjectList(t, w, listed, 3)
	}))

	first, err := store.ListObjects(context.Background(), appcleanup.GeneratedObjectPrefix, "", 2)
	if err != nil {
		t.Fatalf("first ListObjects: %v", err)
	}
	if !first.HasMore || first.NextCursor != "_generated/b.woff2" {
		t.Fatalf("first page = %#v", first)
	}
	if want := []appcleanup.Object{
		{Key: "_generated/a.woff2", ETag: "etag-a", SizeBytes: 1, LastModified: lastModified},
		{Key: "_generated/b.woff2", ETag: "etag-b", SizeBytes: 2, LastModified: lastModified},
	}; !reflect.DeepEqual(first.Objects, want) {
		t.Fatalf("first page objects = %#v, want %#v", first.Objects, want)
	}

	second, err := store.ListObjects(context.Background(), appcleanup.GeneratedObjectPrefix, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("second ListObjects: %v", err)
	}
	if second.HasMore || second.NextCursor != "" || len(second.Objects) != 1 || second.Objects[0].Key != "_generated/c.woff2" {
		t.Fatalf("second page = %#v", second)
	}
	if requests.Load() != 2 {
		t.Fatalf("list requests = %d, want 2", requests.Load())
	}
}

func TestListObjectsPreservesCallerCancellation(t *testing.T) {
	store := newHTTPTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.ListObjects(ctx, appcleanup.GeneratedObjectPrefix, "", 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListObjects error = %v, want context cancellation", err)
	}
}

func TestDeleteObjectReportsMissingSnapshot(t *testing.T) {
	var requests atomic.Int32
	store := newHTTPTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodHead || r.URL.Path != "/test-bucket/_generated/missing.woff2" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
	}))

	err := store.DeleteObject(context.Background(), appcleanup.Object{Key: "_generated/missing.woff2"})
	if !errors.Is(err, appcleanup.ErrObjectNotFound) {
		t.Fatalf("DeleteObject error = %v, want ErrObjectNotFound", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("delete requests = %d, want 1", requests.Load())
	}
}

func TestDeleteObjectRemovesOnlyListedVersion(t *testing.T) {
	const key = "_generated/old.woff2"
	lastModified := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	var deletes atomic.Int32
	store := newHTTPTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			setObjectHeaders(w.Header(), 4, "etag-v1", lastModified, "")
			w.Header().Set("X-Amz-Version-Id", "version-v1")
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			sequence := deletes.Add(1)
			got := r.URL.Query().Get("versionId")
			if sequence == 1 && got != "" {
				t.Errorf("delete-marker request versionId = %q, want empty", got)
			}
			if sequence == 2 && got != "version-v1" {
				t.Errorf("validated-version request versionId = %q, want version-v1", got)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	err := store.DeleteObject(context.Background(), appcleanup.Object{
		Key: key, ETag: "etag-v1", SizeBytes: 4, LastModified: lastModified,
	})
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if deletes.Load() != 2 {
		t.Fatalf("delete requests = %d, want 2", deletes.Load())
	}
}

func TestDeleteObjectLeavesMarkerWhenValidatedVersionRemovalFails(t *testing.T) {
	const key = "_generated/old.woff2"
	lastModified := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	var deletes atomic.Int32
	store := newHTTPTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			setObjectHeaders(w.Header(), 4, "etag-v1", lastModified, "")
			w.Header().Set("X-Amz-Version-Id", "version-v1")
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			sequence := deletes.Add(1)
			if sequence == 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code></Error>`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	err := store.DeleteObject(context.Background(), appcleanup.Object{
		Key: key, ETag: "etag-v1", SizeBytes: 4, LastModified: lastModified,
	})
	if !errors.Is(err, appfont.ErrObjectStorageUnavailable) {
		t.Fatalf("DeleteObject error = %v, want ErrObjectStorageUnavailable", err)
	}
	if deletes.Load() != 2 {
		t.Fatalf("delete requests = %d, want marker then exact version", deletes.Load())
	}
}

func TestDeleteObjectSucceedsWhenValidatedVersionWasAlreadyRemoved(t *testing.T) {
	const key = "_generated/old.woff2"
	lastModified := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	var deletes atomic.Int32
	store := newHTTPTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			setObjectHeaders(w.Header(), 4, "etag-v1", lastModified, "")
			w.Header().Set("X-Amz-Version-Id", "version-v1")
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if deletes.Add(1) == 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchVersion</Code></Error>`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	err := store.DeleteObject(context.Background(), appcleanup.Object{
		Key: key, ETag: "etag-v1", SizeBytes: 4, LastModified: lastModified,
	})
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if deletes.Load() != 2 {
		t.Fatalf("delete requests = %d, want marker then exact version", deletes.Load())
	}
}

func TestDeleteObjectRejectsObjectReplacedAfterListing(t *testing.T) {
	listedAt := time.Date(2026, time.July, 10, 10, 0, 0, 0, time.UTC)
	currentAt := listedAt.Add(2 * time.Hour)
	var deletes atomic.Int32
	store := newHTTPTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			setObjectHeaders(w.Header(), 4, "same-content-etag", currentAt, "")
			w.Header().Set("X-Amz-Version-Id", "new-version")
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	err := store.DeleteObject(context.Background(), appcleanup.Object{
		Key: "_generated/rebuilt.woff2", ETag: "same-content-etag", SizeBytes: 4, LastModified: listedAt,
	})
	if !errors.Is(err, appcleanup.ErrObjectChanged) {
		t.Fatalf("DeleteObject error = %v, want ErrObjectChanged", err)
	}
	if deletes.Load() != 0 {
		t.Fatalf("delete requests = %d, want 0", deletes.Load())
	}
}

func newHTTPTestStore(t *testing.T, handler http.Handler) *Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	store, err := New(Config{
		Endpoint:  strings.TrimPrefix(server.URL, "http://"),
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
		Bucket:    "test-bucket",
		Region:    "us-east-1",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

func setObjectHeaders(header http.Header, size int, etag string, lastModified time.Time, checksum string) {
	header.Set("Content-Length", fmt.Sprintf("%d", size))
	header.Set("Content-Type", "font/woff2")
	header.Set("ETag", `"`+etag+`"`)
	header.Set("Last-Modified", lastModified.Format(http.TimeFormat))
	header.Set("X-Amz-Checksum-Sha256", checksum)
	header.Set("X-Amz-Meta-Sha256", strings.Repeat("CD", 32))
}

type testListBucketResult struct {
	XMLName     xml.Name         `xml:"ListBucketResult"`
	Name        string           `xml:"Name"`
	Prefix      string           `xml:"Prefix"`
	KeyCount    int              `xml:"KeyCount"`
	MaxKeys     int              `xml:"MaxKeys"`
	IsTruncated bool             `xml:"IsTruncated"`
	Contents    []testListObject `xml:"Contents"`
}

type testListObject struct {
	Key          string    `xml:"Key"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
}

func writeTestObjectList(t *testing.T, w http.ResponseWriter, objects []testListObject, maxKeys int) {
	t.Helper()
	w.Header().Set("Content-Type", "application/xml")
	result := testListBucketResult{
		Name: "test-bucket", Prefix: appcleanup.GeneratedObjectPrefix,
		KeyCount: len(objects), MaxKeys: maxKeys, Contents: objects,
	}
	if err := xml.NewEncoder(w).Encode(result); err != nil {
		t.Errorf("encode object list: %v", err)
	}
}
