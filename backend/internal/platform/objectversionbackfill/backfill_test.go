package objectversionbackfill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	errInjectedCopy         = errors.New("injected copy failure")
	errInjectedVerification = errors.New("injected verification failure")
)

func TestRunEmptyBucket(t *testing.T) {
	store := newMemoryStore()
	result, err := Run(context.Background(), store, 4)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != (Result{}) {
		t.Fatalf("result = %#v, want zero", result)
	}
}

func TestRunRewritesNullVersionAndPreservesMetadataAndTags(t *testing.T) {
	store := newMemoryStore()
	metadata := Metadata{
		Headers: map[string]string{
			"Content-Type":        "font/ttf",
			"Cache-Control":       "public, max-age=3600",
			"Content-Encoding":    "identity",
			"Content-Language":    "en",
			"Content-Disposition": `attachment; filename="legacy.ttf"`,
			"Expires":             "Tue, 14 Nov 2023 23:13:20 GMT",
		},
		// This deliberately overlaps a standard header name. The real adapter
		// emits it as x-amz-meta-content-type while retaining Content-Type.
		User: map[string]string{"content-type": "legacy-label", "owner": "fonts"},
	}
	tags := map[string]string{"family": "Inter", "source": "legacy"}
	store.addNull("original-fonts/inter.ttf", []byte("legacy-font-bytes"), metadata, tags)

	result, err := Run(context.Background(), store, 2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	wantResult := Result{Scanned: 1, NullVersions: 1, Rewritten: 1}
	if result != wantResult {
		t.Fatalf("result = %#v, want %#v", result, wantResult)
	}
	current := store.current(t, "original-fonts/inter.ttf")
	if isNullVersion(current.object.VersionID) {
		t.Fatalf("current version = %q, want immutable version", current.object.VersionID)
	}
	if !hasReservedMarker(current.object.Metadata) {
		t.Fatal("rewritten object has no durable verification markers")
	}
	_, preservedMetadata, markerErr := parseMarkers(current.object.Metadata)
	if markerErr != nil {
		t.Fatalf("parse markers: %v", markerErr)
	}
	if !equalMetadata(preservedMetadata, metadata) {
		t.Fatalf("metadata = %#v, want %#v", preservedMetadata, metadata)
	}
	if !equalMap(current.tags, tags) {
		t.Fatalf("tags = %#v, want %#v", current.tags, tags)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	store := newMemoryStore()
	store.addNull("font.ttf", []byte("font"), Metadata{}, nil)
	if _, err := Run(context.Background(), store, 1); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	versionsAfterFirst := store.versionCount("font.ttf")

	result, err := Run(context.Background(), store, 1)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	wantResult := Result{Scanned: 1, AlreadyVersioned: 1}
	if result != wantResult {
		t.Fatalf("second result = %#v, want %#v", result, wantResult)
	}
	if got := store.versionCount("font.ttf"); got != versionsAfterFirst {
		t.Fatalf("versions after second run = %d, want %d", got, versionsAfterFirst)
	}
}

func TestRunIsIdempotentAfterLifecycleRemovesNullVersion(t *testing.T) {
	store := newMemoryStore()
	store.addNull("font.ttf", []byte("font"), Metadata{}, nil)
	if _, err := Run(context.Background(), store, 1); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	store.removeVersion(t, "font.ttf", NullVersionID)

	result, err := Run(context.Background(), store, 1)
	if err != nil {
		t.Fatalf("Run after null-version lifecycle expiry: %v", err)
	}
	if result != (Result{Scanned: 1, AlreadyVersioned: 1}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunCompletesLegacyMarkersBeforeNullVersionExpires(t *testing.T) {
	store := newMemoryStore()
	store.addNull("font.ttf", []byte("font"), Metadata{}, nil)
	if _, err := Run(context.Background(), store, 1); err != nil {
		t.Fatalf("initial Run: %v", err)
	}
	store.mu.Lock()
	entry := store.entries["font.ttf"]
	marked := entry.versions[entry.current]
	marked.object.Metadata.User[markerSchemaKey] = markerLegacySchema
	delete(marked.object.Metadata.User, markerVersionSetKey)
	store.completions = make(map[[sha256.Size]byte]bool)
	store.mu.Unlock()

	if _, err := Run(context.Background(), store, 1); err != nil {
		t.Fatalf("complete legacy marker: %v", err)
	}
	store.removeVersion(t, "font.ttf", NullVersionID)
	if _, err := Run(context.Background(), store, 1); err != nil {
		t.Fatalf("Run legacy marker after null-version expiry: %v", err)
	}
}

func TestRunDoesNotBlessIncompleteRewriteAfterNullVersionExpires(t *testing.T) {
	store := newMemoryStore()
	store.addNull("font.ttf", []byte("font"), Metadata{}, nil)
	store.failVersionedSHAOnce = true
	if _, err := Run(context.Background(), store, 1); !errors.Is(err, errInjectedVerification) {
		t.Fatalf("first Run error = %v", err)
	}
	store.removeVersion(t, "font.ttf", NullVersionID)

	if _, err := Run(context.Background(), store, 1); err == nil || !strings.Contains(err.Error(), "null source") {
		t.Fatalf("Run after incomplete source expiry error = %v", err)
	}
	if got := store.completionCount(); got != 0 {
		t.Fatalf("completion records = %d, want zero", got)
	}
}

func TestRunResumesVerificationAfterPostCommitFailure(t *testing.T) {
	store := newMemoryStore()
	store.addNull("font.ttf", []byte("font"), Metadata{}, map[string]string{"family": "Inter"})
	store.failVersionedSHAOnce = true

	first, err := Run(context.Background(), store, 1)
	if !errors.Is(err, errInjectedVerification) {
		t.Fatalf("first Run error = %v, want injected verification failure", err)
	}
	if first != (Result{Scanned: 1, NullVersions: 1}) {
		t.Fatalf("first result = %#v", first)
	}
	if current := store.current(t, "font.ttf"); isNullVersion(current.object.VersionID) || !hasReservedMarker(current.object.Metadata) {
		t.Fatalf("committed object = %#v, want marked real version", current.object)
	}
	versionsAfterCommit := store.versionCount("font.ttf")
	rewritesAfterCommit, _, _ := store.copyStats()

	second, err := Run(context.Background(), store, 1)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second != (Result{Scanned: 1, AlreadyVersioned: 1}) {
		t.Fatalf("second result = %#v", second)
	}
	if got := store.versionCount("font.ttf"); got != versionsAfterCommit {
		t.Fatalf("versions after resume = %d, want %d", got, versionsAfterCommit)
	}
	if rewrites, _, _ := store.copyStats(); rewrites != rewritesAfterCommit {
		t.Fatalf("rewrites after resume = %d, want %d", rewrites, rewritesAfterCommit)
	}
}

func TestRunResumesVerificationAfterPostCommitCancellation(t *testing.T) {
	store := newMemoryStore()
	store.addNull("font.ttf", []byte("font"), Metadata{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	store.afterCommit = cancel

	first, err := Run(ctx, store, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run error = %v, want cancellation", err)
	}
	if first != (Result{Scanned: 1, NullVersions: 1}) {
		t.Fatalf("first result = %#v", first)
	}
	versionsAfterCommit := store.versionCount("font.ttf")
	if current := store.current(t, "font.ttf"); isNullVersion(current.object.VersionID) || !hasReservedMarker(current.object.Metadata) {
		t.Fatalf("committed object = %#v, want marked real version", current.object)
	}

	second, err := Run(context.Background(), store, 1)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second != (Result{Scanned: 1, AlreadyVersioned: 1}) {
		t.Fatalf("second result = %#v", second)
	}
	if got := store.versionCount("font.ttf"); got != versionsAfterCommit {
		t.Fatalf("versions after resume = %d, want %d", got, versionsAfterCommit)
	}
}

func TestRunNeverSkipsCorruptMarkedVersion(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*storedObject)
		want   string
	}{
		{
			name: "partial marker",
			mutate: func(current *storedObject) {
				delete(current.object.Metadata.User, markerContentSHA256Key)
			},
			want: "incomplete",
		},
		{
			name: "malformed marker",
			mutate: func(current *storedObject) {
				current.object.Metadata.User[markerContentSHA256Key] = "not-a-digest"
			},
			want: "malformed",
		},
		{
			name: "unknown marker",
			mutate: func(current *storedObject) {
				current.object.Metadata.User[markerPrefix+"future"] = "1"
			},
			want: "unknown",
		},
		{
			name: "bytes",
			mutate: func(current *storedObject) {
				current.body[0] ^= 0xff
			},
			want: "SHA-256",
		},
		{
			name: "metadata",
			mutate: func(current *storedObject) {
				current.object.Metadata.User["owner"] = "changed"
			},
			want: "metadata and tags",
		},
		{
			name: "tags",
			mutate: func(current *storedObject) {
				current.tags["family"] = "Changed"
			},
			want: "metadata and tags",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			store.addNull(
				"sensitive-object-name.ttf",
				[]byte("font"),
				Metadata{User: map[string]string{"owner": "fonts"}},
				map[string]string{"family": "Inter"},
			)
			store.failVersionedSHAOnce = true
			if _, err := Run(context.Background(), store, 1); !errors.Is(err, errInjectedVerification) {
				t.Fatalf("first Run error = %v", err)
			}

			store.mu.Lock()
			entry := store.entries["sensitive-object-name.ttf"]
			test.mutate(entry.versions[entry.current])
			store.mu.Unlock()
			versionsBeforeRerun := store.versionCount("sensitive-object-name.ttf")
			rewritesBeforeRerun, _, _ := store.copyStats()

			result, err := Run(context.Background(), store, 1)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("second Run error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "sensitive-object-name.ttf") {
				t.Fatalf("error leaked object key: %v", err)
			}
			if result.AlreadyVersioned != 0 || result.Rewritten != 0 {
				t.Fatalf("second result = %#v", result)
			}
			if got := store.versionCount("sensitive-object-name.ttf"); got != versionsBeforeRerun {
				t.Fatalf("versions after corrupt rerun = %d, want %d", got, versionsBeforeRerun)
			}
			if rewrites, _, _ := store.copyStats(); rewrites != rewritesBeforeRerun {
				t.Fatalf("rewrites after corrupt rerun = %d, want %d", rewrites, rewritesBeforeRerun)
			}
		})
	}
}

func TestRunDetectsSourceTagChangesAroundGuardedPut(t *testing.T) {
	t.Run("before PUT", func(t *testing.T) {
		store := newMemoryStore()
		store.addNull("font.ttf", []byte("font"), Metadata{}, map[string]string{"state": "old"})
		store.tagMutationKey = "font.ttf"
		store.tagMutationRead = 2
		store.tagMutation = map[string]string{"state": "new"}

		result, err := Run(context.Background(), store, 1)
		if err == nil || !strings.Contains(err.Error(), "tags changed before rewrite") {
			t.Fatalf("Run error = %v", err)
		}
		if result.Rewritten != 0 {
			t.Fatalf("result = %#v", result)
		}
		if rewrites, _, _ := store.copyStats(); rewrites != 0 {
			t.Fatalf("rewrites = %d, want zero", rewrites)
		}
	})

	t.Run("after PUT remains detectable on rerun", func(t *testing.T) {
		store := newMemoryStore()
		store.addNull("font.ttf", []byte("font"), Metadata{}, map[string]string{"state": "old"})
		store.tagMutationKey = "font.ttf"
		store.tagMutationRead = 3
		store.tagMutation = map[string]string{"state": "new"}

		first, err := Run(context.Background(), store, 1)
		if err == nil || !strings.Contains(err.Error(), "tags changed during rewrite") {
			t.Fatalf("first Run error = %v", err)
		}
		if first.Rewritten != 0 || store.versionCount("font.ttf") != 2 {
			t.Fatalf("first result = %#v, versions = %d", first, store.versionCount("font.ttf"))
		}

		second, err := Run(context.Background(), store, 1)
		if err == nil || !strings.Contains(err.Error(), "pinned null source") {
			t.Fatalf("second Run error = %v, want source mismatch", err)
		}
		if second.AlreadyVersioned != 0 || second.Rewritten != 0 {
			t.Fatalf("second result = %#v", second)
		}
		if rewrites, _, _ := store.copyStats(); rewrites != 1 {
			t.Fatalf("rewrites after rerun = %d, want one committed rewrite", rewrites)
		}
	})
}

func TestRunRejectsUnsafeBucketBeforeListing(t *testing.T) {
	tests := []struct {
		name     string
		security BucketSecurity
		err      error
		want     string
	}{
		{name: "object lock", security: BucketSecurity{ObjectLockConfigured: true}, want: "object lock"},
		{name: "default encryption", security: BucketSecurity{EncryptionConfigured: true}, want: "encryption"},
		{name: "inspection failure", err: errors.New("inspection unavailable"), want: "inspection unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			store.addNull("must-not-leak.ttf", []byte("font"), Metadata{}, nil)
			store.security = test.security
			store.securityErr = test.err
			result, err := Run(context.Background(), store, 1)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "must-not-leak.ttf") {
				t.Fatalf("error leaked object key: %v", err)
			}
			if result != (Result{}) {
				t.Fatalf("result = %#v, want zero", result)
			}
			if rewrites, _, _ := store.copyStats(); rewrites != 0 {
				t.Fatalf("rewrites = %d, want zero", rewrites)
			}
		})
	}
}

func TestRunRejectsUnsafeNullVersionWithoutLeakingKey(t *testing.T) {
	tests := []struct {
		name     string
		security SecurityState
	}{
		{name: "encryption", security: SecurityState{Encryption: true}},
		{name: "object lock", security: SecurityState{ObjectLock: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const key = "private/object-name.ttf"
			store := newMemoryStore()
			store.addNull(key, []byte("font"), Metadata{}, nil)
			store.mu.Lock()
			store.entries[key].versions[NullVersionID].object.Security = test.security
			store.mu.Unlock()

			result, err := Run(context.Background(), store, 1)
			if err == nil || !strings.Contains(err.Error(), "unsupported encryption or object-lock state") {
				t.Fatalf("Run error = %v", err)
			}
			if strings.Contains(err.Error(), key) {
				t.Fatalf("error leaked object key: %v", err)
			}
			if result.Rewritten != 0 || result.AlreadyVersioned != 0 {
				t.Fatalf("result = %#v", result)
			}
			if rewrites, _, _ := store.copyStats(); rewrites != 0 {
				t.Fatalf("rewrites = %d, want zero", rewrites)
			}
		})
	}
}

func TestRunFailsOnByteMismatch(t *testing.T) {
	store := newMemoryStore()
	store.addNull("font.ttf", []byte("font-data"), Metadata{}, nil)
	store.corruptCopy = true

	result, err := Run(context.Background(), store, 1)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("Run error = %v, want SHA-256 mismatch", err)
	}
	if result.Rewritten != 0 || result.NullVersions != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunCopyFailureCancelsOutstandingWork(t *testing.T) {
	store := newMemoryStore()
	for index := range 20 {
		store.addNull(fmt.Sprintf("%02d.ttf", index), []byte("font"), Metadata{}, nil)
	}
	store.copyFailureKey = "00.ttf"
	store.copyGate = make(chan struct{})
	store.failureGate = make(chan struct{})
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := Run(context.Background(), store, 4)
		done <- outcome{result: result, err: err}
	}()
	waitFor(t, func() bool {
		_, active, _ := store.copyStats()
		return active == 4
	})
	close(store.failureGate)
	completed := <-done
	result, err := completed.result, completed.err
	if !errors.Is(err, errInjectedCopy) {
		t.Fatalf("Run error = %v, want injected failure", err)
	}
	copyCalls, _, canceledCopies := store.copyStats()
	if copyCalls > 4 {
		t.Fatalf("copy calls = %d, want at most worker bound 4", copyCalls)
	}
	if canceledCopies == 0 {
		t.Fatal("outstanding copies did not observe cancellation")
	}
	if result.Rewritten != 0 {
		t.Fatalf("result = %#v, want no completed rewrites", result)
	}
}

func TestRunHonorsCallerCancellation(t *testing.T) {
	store := newMemoryStore()
	for index := range 4 {
		store.addNull(fmt.Sprintf("%02d.ttf", index), []byte("font"), Metadata{}, nil)
	}
	store.copyGate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, store, 2)
		done <- err
	}()
	waitFor(t, func() bool {
		_, active, _ := store.copyStats()
		return active == 2
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunDetectsConcurrentVersionEvenWhenCopyRemainsCurrent(t *testing.T) {
	store := newMemoryStore()
	store.addNull("font.ttf", []byte("font"), Metadata{}, nil)
	store.injectConcurrentVersion = true

	_, err := Run(context.Background(), store, 1)
	if err == nil || !strings.Contains(err.Error(), "version set changed concurrently") {
		t.Fatalf("Run error = %v, want concurrent version failure", err)
	}
	for retry := 1; retry <= 2; retry++ {
		_, retryErr := Run(context.Background(), store, 1)
		if retryErr == nil || !strings.Contains(retryErr.Error(), "version set changed concurrently") {
			t.Fatalf("retry %d error = %v, want persistent concurrent version failure", retry, retryErr)
		}
	}
	if got := store.completionCount(); got != 0 {
		t.Fatalf("completion records = %d, want zero", got)
	}
}

func TestRunDetectsLatestIdentityChangeAfterEnumeration(t *testing.T) {
	store := newMemoryStore()
	store.addNull("font.ttf", []byte("font"), Metadata{}, nil)
	store.changeOnFirstStat = true

	_, err := Run(context.Background(), store, 1)
	if err == nil || !strings.Contains(err.Error(), "identity changed after enumeration") {
		t.Fatalf("Run error = %v, want latest identity failure", err)
	}
	if calls, _, _ := store.copyStats(); calls != 0 {
		t.Fatalf("copy calls = %d, want zero", calls)
	}
}

func TestRunBoundsConcurrency(t *testing.T) {
	store := newMemoryStore()
	for index := range 12 {
		store.addNull(fmt.Sprintf("%02d.ttf", index), []byte("font"), Metadata{}, nil)
	}
	gate := make(chan struct{})
	store.copyGate = gate
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := Run(context.Background(), store, 3)
		done <- outcome{result: result, err: err}
	}()
	waitFor(t, func() bool {
		_, active, _ := store.copyStats()
		return active == 3
	})
	if _, maximum, _ := store.maximumStats(); maximum != 3 {
		t.Fatalf("maximum concurrency = %d, want 3", maximum)
	}
	close(gate)
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("Run: %v", outcome.err)
		}
		if outcome.result.Rewritten != 12 {
			t.Fatalf("result = %#v", outcome.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not finish after releasing copies")
	}
	if _, maximum, _ := store.maximumStats(); maximum > 3 {
		t.Fatalf("maximum concurrency = %d, exceeds 3", maximum)
	}
}

func TestRunRequiresVersioning(t *testing.T) {
	store := newMemoryStore()
	store.versioning = false
	if _, err := Run(context.Background(), store, 1); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("Run error = %v, want versioning failure", err)
	}
}

func TestRunValidatesConcurrency(t *testing.T) {
	store := newMemoryStore()
	for _, concurrency := range []int{0, MaxConcurrency + 1} {
		if _, err := Run(context.Background(), store, concurrency); err == nil {
			t.Fatalf("Run concurrency %d succeeded", concurrency)
		}
	}
}

type storedObject struct {
	object Object
	body   []byte
	tags   map[string]string
}

type memoryEntry struct {
	current  string
	versions map[string]*storedObject
}

type memoryStore struct {
	mu sync.Mutex

	versioning  bool
	security    BucketSecurity
	securityErr error
	entries     map[string]*memoryEntry
	completions map[[sha256.Size]byte]bool
	next        int

	copyGate                chan struct{}
	failureGate             chan struct{}
	copyFailureKey          string
	corruptCopy             bool
	injectConcurrentVersion bool
	changeOnFirstStat       bool
	failVersionedSHAOnce    bool
	afterCommit             func()
	tagMutationKey          string
	tagMutationRead         int
	tagMutation             map[string]string
	tagReads                map[string]int

	copyCalls      int
	activeCopies   int
	maximumCopies  int
	canceledCopies int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		versioning:  true,
		entries:     make(map[string]*memoryEntry),
		completions: make(map[[sha256.Size]byte]bool),
		tagReads:    make(map[string]int),
	}
}

func (store *memoryStore) addNull(key string, body []byte, metadata Metadata, tags map[string]string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	object := Object{
		Key: key, VersionID: NullVersionID, ETag: memoryETag(body), Size: int64(len(body)),
		LastModified: time.Unix(1_700_000_000, 0).UTC(),
		Metadata:     Metadata{Headers: cloneMap(metadata.Headers), User: cloneMap(metadata.User)},
	}
	store.entries[key] = &memoryEntry{
		current: NullVersionID,
		versions: map[string]*storedObject{
			NullVersionID: {object: object, body: append([]byte(nil), body...), tags: cloneMap(tags)},
		},
	}
}

func (store *memoryStore) BucketVersioningEnabled(context.Context) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.versioning, nil
}

func (store *memoryStore) BucketSecurity(context.Context) (BucketSecurity, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.securityErr != nil {
		return BucketSecurity{}, store.securityErr
	}
	return store.security, nil
}

func (store *memoryStore) ListCurrent(ctx context.Context) <-chan ListedObject {
	store.mu.Lock()
	keys := make([]string, 0, len(store.entries))
	objects := make(map[string]Object, len(store.entries))
	for key, entry := range store.entries {
		keys = append(keys, key)
		objects[key] = cloneObject(entry.versions[entry.current].object)
	}
	store.mu.Unlock()
	sort.Strings(keys)
	results := make(chan ListedObject)
	go func() {
		defer close(results)
		for _, key := range keys {
			select {
			case results <- ListedObject{Object: objects[key]}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return results
}

func (store *memoryStore) StatCurrent(_ context.Context, key string) (Object, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, exists := store.entries[key]
	if !exists {
		return Object{}, errors.New("object not found")
	}
	if store.changeOnFirstStat {
		store.changeOnFirstStat = false
		legacy := entry.versions[entry.current]
		store.createVersionLocked(key, entry, legacy.body, legacy.object.Metadata, legacy.tags)
	}
	return cloneObject(entry.versions[entry.current].object), nil
}

func (store *memoryStore) StatVersion(_ context.Context, key, versionID string) (Object, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	stored, err := store.versionLocked(key, versionID)
	if err != nil {
		return Object{}, err
	}
	return cloneObject(stored.object), nil
}

func (store *memoryStore) ListVersions(_ context.Context, key string) ([]Version, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, exists := store.entries[key]
	if !exists {
		return nil, errors.New("object not found")
	}
	ids := make([]string, 0, len(entry.versions))
	for versionID := range entry.versions {
		ids = append(ids, versionID)
	}
	sort.Strings(ids)
	versions := make([]Version, 0, len(ids))
	for _, versionID := range ids {
		object := entry.versions[versionID].object
		versions = append(versions, Version{
			VersionID: object.VersionID, ETag: object.ETag, Size: object.Size,
			LastModified: object.LastModified,
		})
	}
	return versions, nil
}

func (store *memoryStore) GetTags(ctx context.Context, key, versionID string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	stored, err := store.versionLocked(key, versionID)
	if err != nil {
		return nil, err
	}
	if isNullVersion(versionID) {
		store.tagReads[key]++
		if key == store.tagMutationKey && store.tagReads[key] == store.tagMutationRead {
			stored.tags = cloneMap(store.tagMutation)
		}
	}
	return cloneMap(stored.tags), nil
}

func (store *memoryStore) SHA256(ctx context.Context, key, versionID, etag string) (Digest, error) {
	if err := ctx.Err(); err != nil {
		return Digest{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if !isNullVersion(versionID) && store.failVersionedSHAOnce {
		store.failVersionedSHAOnce = false
		return Digest{}, errInjectedVerification
	}
	stored, err := store.versionLocked(key, versionID)
	if err != nil {
		return Digest{}, err
	}
	if strings.Trim(etag, `"`) != stored.object.ETag {
		return Digest{}, errors.New("ETag precondition failed")
	}
	return Digest{Size: int64(len(stored.body)), SHA256: sha256.Sum256(stored.body)}, nil
}

func (store *memoryStore) Rewrite(ctx context.Context, request RewriteRequest) (RewriteResult, error) {
	store.mu.Lock()
	store.copyCalls++
	store.activeCopies++
	if store.activeCopies > store.maximumCopies {
		store.maximumCopies = store.activeCopies
	}
	failure := request.Source.Key == store.copyFailureKey
	gate := store.copyGate
	failureGate := store.failureGate
	store.mu.Unlock()
	defer func() {
		store.mu.Lock()
		store.activeCopies--
		store.mu.Unlock()
	}()

	if failure {
		if failureGate != nil {
			select {
			case <-failureGate:
			case <-ctx.Done():
				return RewriteResult{}, ctx.Err()
			}
		}
		return RewriteResult{}, errInjectedCopy
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			store.mu.Lock()
			store.canceledCopies++
			store.mu.Unlock()
			return RewriteResult{}, ctx.Err()
		}
	}

	store.mu.Lock()
	entry, exists := store.entries[request.Source.Key]
	if !exists {
		store.mu.Unlock()
		return RewriteResult{}, errors.New("object not found")
	}
	source, exists := entry.versions[normalizedVersionID(request.SourceVersionID)]
	if !exists || source.object.ETag != request.SourceETag || entry.versions[entry.current].object.ETag != request.CurrentETag {
		store.mu.Unlock()
		return RewriteResult{}, errors.New("rewrite precondition failed")
	}
	if store.injectConcurrentVersion {
		store.createVersionLocked(request.Source.Key, entry, source.body, source.object.Metadata, source.tags)
	}
	body := append([]byte(nil), source.body...)
	if store.corruptCopy && len(body) > 0 {
		body[0] ^= 0xff
	}
	created := store.createVersionLocked(
		request.Source.Key,
		entry,
		body,
		request.Metadata,
		request.Tags,
	)
	created.object.Checksum = request.Source.Checksum
	result := RewriteResult{
		Object: cloneObject(created.object),
		Streamed: Digest{
			Size: int64(len(source.body)), SHA256: sha256.Sum256(source.body),
			Checksum: request.Source.Checksum,
		},
	}
	afterCommit := store.afterCommit
	store.afterCommit = nil
	store.mu.Unlock()
	if afterCommit != nil {
		afterCommit()
	}
	return result, nil
}

func (store *memoryStore) CompletionRecordExists(
	ctx context.Context,
	proof [sha256.Size]byte,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.completions[proof], nil
}

func (store *memoryStore) PutCompletionRecord(
	ctx context.Context,
	proof [sha256.Size]byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.completions[proof] = true
	return nil
}

func (store *memoryStore) createVersionLocked(
	key string,
	entry *memoryEntry,
	body []byte,
	metadata Metadata,
	tags map[string]string,
) *storedObject {
	store.next++
	versionID := fmt.Sprintf("version-%03d", store.next)
	stored := &storedObject{
		object: Object{
			Key: key, VersionID: versionID, ETag: memoryETag(body), Size: int64(len(body)),
			LastModified: time.Unix(1_700_000_000+int64(store.next), 0).UTC(),
			Metadata:     Metadata{Headers: cloneMap(metadata.Headers), User: cloneMap(metadata.User)},
		},
		body: append([]byte(nil), body...), tags: cloneMap(tags),
	}
	entry.versions[versionID] = stored
	entry.current = versionID
	return stored
}

func (store *memoryStore) versionLocked(key, versionID string) (*storedObject, error) {
	entry, exists := store.entries[key]
	if !exists {
		return nil, errors.New("object not found")
	}
	stored, exists := entry.versions[normalizedVersionID(versionID)]
	if !exists {
		return nil, errors.New("version not found")
	}
	return stored, nil
}

func (store *memoryStore) current(t *testing.T, key string) storedObject {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, exists := store.entries[key]
	if !exists {
		t.Fatalf("missing object %q", key)
	}
	stored := entry.versions[entry.current]
	return storedObject{
		object: cloneObject(stored.object), body: append([]byte(nil), stored.body...), tags: cloneMap(stored.tags),
	}
}

func (store *memoryStore) versionCount(key string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.entries[key].versions)
}

func (store *memoryStore) removeVersion(t *testing.T, key, versionID string) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, exists := store.entries[key]
	if !exists {
		t.Fatalf("missing object %q", key)
	}
	versionID = normalizedVersionID(versionID)
	if entry.current == versionID {
		t.Fatalf("cannot remove current version %q", versionID)
	}
	if _, exists := entry.versions[versionID]; !exists {
		t.Fatalf("missing version %q", versionID)
	}
	delete(entry.versions, versionID)
}

func (store *memoryStore) completionCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.completions)
}

func (store *memoryStore) copyStats() (calls, active, canceled int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.copyCalls, store.activeCopies, store.canceledCopies
}

func (store *memoryStore) maximumStats() (calls, maximum, canceled int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.copyCalls, store.maximumCopies, store.canceledCopies
}

func memoryETag(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:16])
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}
