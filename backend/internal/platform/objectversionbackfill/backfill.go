package objectversionbackfill

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// NullVersionID is the S3 version identifier assigned to objects that
	// predate bucket versioning.
	NullVersionID = "null"
	// MaxConcurrency bounds memory, connections, and simultaneous object rewrites.
	MaxConcurrency = 32

	markerPrefix            = "emfont-object-version-backfill-"
	markerSchemaKey         = markerPrefix + "schema"
	markerContentSHA256Key  = markerPrefix + "content-sha256"
	markerMetadataSHA256Key = markerPrefix + "metadata-tags-sha256"
	markerVersionSetKey     = markerPrefix + "version-set-sha256"
	markerLegacySchema      = "1"
	markerSchemaVersion     = "2"
)

// Metadata is the object metadata that must survive the same-key rewrite.
type Metadata struct {
	Headers map[string]string
	User    map[string]string
}

// SecurityState records security-sensitive state without retaining or exposing
// any associated key, context, retention, or legal-hold values.
type SecurityState struct {
	Encryption bool
	ObjectLock bool
}

func (state SecurityState) unsafe() bool {
	return state.Encryption || state.ObjectLock
}

// BucketSecurity records bucket configuration that would make a rewrite unsafe.
type BucketSecurity struct {
	EncryptionConfigured bool
	ObjectLockConfigured bool
}

// Object identifies one immutable object version and its metadata.
type Object struct {
	Key          string
	VersionID    string
	ETag         string
	Size         int64
	LastModified time.Time
	Metadata     Metadata
	Security     SecurityState
	// Checksum is the single full-object checksum reported by S3. A zero value
	// means the source version has no stored checksum.
	Checksum Checksum
}

// ChecksumAlgorithm identifies one supported full-object S3 checksum.
type ChecksumAlgorithm string

const (
	ChecksumCRC32     ChecksumAlgorithm = "CRC32"
	ChecksumCRC32C    ChecksumAlgorithm = "CRC32C"
	ChecksumSHA1      ChecksumAlgorithm = "SHA1"
	ChecksumSHA256    ChecksumAlgorithm = "SHA256"
	ChecksumCRC64NVME ChecksumAlgorithm = "CRC64NVME"
)

// Checksum contains the algorithm and canonical S3 base64 value. Backfill
// preserves this state exactly instead of silently changing algorithms.
type Checksum struct {
	Algorithm ChecksumAlgorithm
	Value     string
}

func (checksum Checksum) empty() bool {
	return checksum.Algorithm == "" && checksum.Value == ""
}

// Version is the identity returned by S3's object-version listing.
type Version struct {
	VersionID    string
	ETag         string
	Size         int64
	LastModified time.Time
	DeleteMarker bool
}

// Digest is an exact byte count and SHA-256 digest of a pinned object version.
type Digest struct {
	Size     int64
	SHA256   [sha256.Size]byte
	Checksum Checksum
}

// ListedObject carries either one current object or a listing error.
type ListedObject struct {
	Object Object
	Err    error
}

// RewriteRequest describes the pinned GET and guarded same-key PUT used by the
// backfill. Source is the unmodified null-version identity; Metadata includes
// the durable verification markers to commit with the new version.
type RewriteRequest struct {
	SourceVersionID string
	SourceETag      string
	CurrentETag     string
	Source          Object
	Metadata        Metadata
	Tags            map[string]string
	ExpectedDigest  Digest
}

// RewriteResult includes both the committed identity and the digest observed
// while the pinned source GET was streamed into the destination PUT.
type RewriteResult struct {
	Object   Object
	Streamed Digest
}

// Store is the narrow S3 surface needed by the backfill engine.
type Store interface {
	BucketVersioningEnabled(context.Context) (bool, error)
	BucketSecurity(context.Context) (BucketSecurity, error)
	ListCurrent(context.Context) <-chan ListedObject
	StatCurrent(context.Context, string) (Object, error)
	StatVersion(context.Context, string, string) (Object, error)
	ListVersions(context.Context, string) ([]Version, error)
	GetTags(context.Context, string, string) (map[string]string, error)
	SHA256(context.Context, string, string, string) (Digest, error)
	Rewrite(context.Context, RewriteRequest) (RewriteResult, error)
	CompletionRecordExists(context.Context, [sha256.Size]byte) (bool, error)
	PutCompletionRecord(context.Context, [sha256.Size]byte) error
}

// Result contains key-free counters suitable for production output.
type Result struct {
	Scanned          int64
	NullVersions     int64
	Rewritten        int64
	AlreadyVersioned int64
}

// Run rewrites every current null-version object and verifies the resulting
// immutable version. It cancels outstanding work on the first failure.
func Run(ctx context.Context, store Store, concurrency int) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("backfill context is required")
	}
	if store == nil {
		return Result{}, errors.New("backfill store is required")
	}
	if concurrency < 1 || concurrency > MaxConcurrency {
		return Result{}, fmt.Errorf("backfill concurrency must be between 1 and %d", MaxConcurrency)
	}

	if err := requireVersioning(ctx, store, "before backfill"); err != nil {
		return Result{}, err
	}
	if err := requireSafeBucket(ctx, store, "before backfill"); err != nil {
		return Result{}, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var counters struct {
		scanned          atomic.Int64
		nullVersions     atomic.Int64
		rewritten        atomic.Int64
		alreadyVersioned atomic.Int64
	}
	result := func() Result {
		return Result{
			Scanned:          counters.scanned.Load(),
			NullVersions:     counters.nullVersions.Load(),
			Rewritten:        counters.rewritten.Load(),
			AlreadyVersioned: counters.alreadyVersioned.Load(),
		}
	}

	var (
		firstErr error
		errOnce  sync.Once
	)
	fail := func(err error) {
		if err == nil {
			return
		}
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	jobs := make(chan Object)
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			for object := range jobs {
				if runCtx.Err() != nil {
					return
				}
				counters.scanned.Add(1)
				candidate, rewritten, processErr := processObject(runCtx, store, object)
				if candidate {
					counters.nullVersions.Add(1)
				}
				if processErr != nil {
					fail(processErr)
					return
				}
				if rewritten {
					counters.rewritten.Add(1)
				} else {
					counters.alreadyVersioned.Add(1)
				}
			}
		}()
	}

	listing := store.ListCurrent(runCtx)
	if listing == nil {
		fail(errors.New("list current objects: store returned no result stream"))
	} else {
		for item := range listing {
			if item.Err != nil {
				fail(fmt.Errorf("list current objects: %w", item.Err))
				continue
			}
			if runCtx.Err() != nil {
				continue
			}
			select {
			case jobs <- item.Object:
			case <-runCtx.Done():
			}
		}
	}
	close(jobs)
	workers.Wait()

	if firstErr != nil {
		return result(), firstErr
	}
	if err := ctx.Err(); err != nil {
		return result(), fmt.Errorf("backfill canceled: %w", err)
	}
	if err := requireVersioning(ctx, store, "after backfill"); err != nil {
		return result(), err
	}
	if err := requireSafeBucket(ctx, store, "after backfill"); err != nil {
		return result(), err
	}
	return result(), nil
}

func processObject(ctx context.Context, store Store, listed Object) (candidate, rewritten bool, err error) {
	current, err := store.StatCurrent(ctx, listed.Key)
	if err != nil {
		return false, false, fmt.Errorf("stat current object: %w", err)
	}
	if !sameObjectIdentity(listed, current) {
		return false, false, errors.New("current object identity changed after enumeration")
	}
	if !isNullVersion(current.VersionID) {
		if hasReservedMarker(current.Metadata) {
			verified, verifyErr := verifyMarkedVersion(ctx, store, current, nil)
			if verifyErr != nil {
				return false, false, fmt.Errorf("verify marked current version: %w", verifyErr)
			}
			if err := requireCompletionRecord(ctx, store, verified); err != nil {
				return false, false, fmt.Errorf("verify marked current version completion: %w", err)
			}
		}
		return false, false, nil
	}
	candidate = true
	if current.ETag == "" {
		return true, false, errors.New("current null-version object has no ETag")
	}
	if current.Security.unsafe() {
		return true, false, errors.New("null-version object has unsupported encryption or object-lock state")
	}
	if hasReservedMarker(current.Metadata) {
		return true, false, errors.New("null-version object contains reserved backfill metadata")
	}

	source, err := store.StatVersion(ctx, listed.Key, NullVersionID)
	if err != nil {
		return true, false, fmt.Errorf("stat pinned null version: %w", err)
	}
	if !isNullVersion(source.VersionID) || !sameObjectIdentity(current, source) {
		return true, false, errors.New("pinned null-version identity does not match current object")
	}
	if source.Security.unsafe() {
		return true, false, errors.New("pinned null version has unsupported encryption or object-lock state")
	}
	if hasReservedMarker(source.Metadata) {
		return true, false, errors.New("pinned null version contains reserved backfill metadata")
	}

	beforeVersions, err := store.ListVersions(ctx, listed.Key)
	if err != nil {
		return true, false, fmt.Errorf("snapshot object versions before rewrite: %w", err)
	}
	if err := requireSourceVersion(beforeVersions, source); err != nil {
		return true, false, err
	}
	versionSetDigest, err := canonicalVersionSetDigest(beforeVersions)
	if err != nil {
		return true, false, fmt.Errorf("digest object versions before rewrite: %w", err)
	}

	tags, err := store.GetTags(ctx, listed.Key, NullVersionID)
	if err != nil {
		return true, false, fmt.Errorf("read pinned null-version tags: %w", err)
	}
	oldDigest, err := store.SHA256(ctx, listed.Key, NullVersionID, source.ETag)
	if err != nil {
		return true, false, fmt.Errorf("hash pinned null version: %w", err)
	}
	if oldDigest.Size != source.Size {
		return true, false, errors.New("pinned null-version byte count does not match object size")
	}
	if err := requireStoredChecksum(source, oldDigest); err != nil {
		return true, false, err
	}
	stateDigest, err := canonicalStateDigest(source.Metadata, tags)
	if err != nil {
		return true, false, fmt.Errorf("digest pinned null-version metadata and tags: %w", err)
	}
	markedMetadata, err := metadataWithMarkers(source.Metadata, oldDigest.SHA256, stateDigest, versionSetDigest)
	if err != nil {
		return true, false, err
	}

	current, err = store.StatCurrent(ctx, listed.Key)
	if err != nil {
		return true, false, fmt.Errorf("restat current object before rewrite: %w", err)
	}
	if !sameObjectIdentity(source, current) {
		return true, false, errors.New("current object identity changed before rewrite")
	}
	if current.Security.unsafe() {
		return true, false, errors.New("current object acquired unsupported encryption or object-lock state before rewrite")
	}
	if err := requireVersioning(ctx, store, "before rewrite"); err != nil {
		return true, false, err
	}
	if err := requireSafeBucket(ctx, store, "before rewrite"); err != nil {
		return true, false, err
	}
	if err := ctx.Err(); err != nil {
		return true, false, fmt.Errorf("backfill canceled before rewrite: %w", err)
	}

	// Tagging is independently mutable and has no ETag precondition. Re-read it
	// immediately around the guarded PUT; the maintenance gate remains required
	// for the final timing window after the second read.
	tagsBeforePut, err := store.GetTags(ctx, listed.Key, NullVersionID)
	if err != nil {
		return true, false, fmt.Errorf("re-read pinned null-version tags before rewrite: %w", err)
	}
	if !equalMap(tags, tagsBeforePut) {
		return true, false, errors.New("null-version tags changed before rewrite")
	}

	rewriteResult, rewriteErr := store.Rewrite(ctx, RewriteRequest{
		SourceVersionID: NullVersionID,
		SourceETag:      source.ETag,
		CurrentETag:     current.ETag,
		Source:          cloneObject(source),
		Metadata:        markedMetadata,
		Tags:            cloneMap(tagsBeforePut),
		ExpectedDigest:  oldDigest,
	})
	tagsAfterPut, tagsAfterErr := store.GetTags(ctx, listed.Key, NullVersionID)
	if tagsAfterErr != nil {
		if rewriteErr != nil && isNullVersion(rewriteResult.Object.VersionID) {
			return true, false, fmt.Errorf("rewrite pinned null version: %w", rewriteErr)
		}
		return true, false, fmt.Errorf("re-read pinned null-version tags after rewrite: %w", tagsAfterErr)
	}
	if !equalMap(tagsBeforePut, tagsAfterPut) {
		return true, false, errors.New("null-version tags changed during rewrite")
	}
	if rewriteErr != nil {
		return true, false, fmt.Errorf("rewrite pinned null version: %w", rewriteErr)
	}

	copied := rewriteResult.Object
	if isNullVersion(copied.VersionID) {
		return true, false, errors.New("rewrite did not create a real object version")
	}
	if rewriteResult.Streamed.Size != oldDigest.Size {
		return true, false, errors.New("streamed rewrite byte count does not match pinned null version")
	}
	if rewriteResult.Streamed.SHA256 != oldDigest.SHA256 {
		return true, false, errors.New("streamed rewrite SHA-256 does not match pinned null version")
	}

	currentAfterRewrite, err := store.StatCurrent(ctx, listed.Key)
	if err != nil {
		return true, false, fmt.Errorf("stat current object after rewrite: %w", err)
	}
	if !sameObjectIdentity(copied, currentAfterRewrite) {
		return true, false, errors.New("rewritten version is not current immediately after rewrite")
	}

	verified, err := verifyMarkedVersion(ctx, store, copied, &sourceExpectation{
		Object: source, Metadata: source.Metadata, Tags: tagsAfterPut,
		Digest: oldDigest, MetadataDigest: stateDigest,
	})
	if err != nil {
		return true, false, fmt.Errorf("verify rewritten version: %w", err)
	}
	if verified.Marker.ContentSHA256 != oldDigest.SHA256 || verified.Marker.MetadataSHA256 != stateDigest {
		return true, false, errors.New("rewritten-version markers do not match pinned null version")
	}
	if !equalMetadata(source.Metadata, verified.Metadata) {
		return true, false, errors.New("rewritten-version metadata does not match null version")
	}
	if !equalMap(tagsBeforePut, verified.Tags) {
		return true, false, errors.New("rewritten-version tags do not match null version")
	}
	if verified.Digest.Size != oldDigest.Size || verified.Digest.SHA256 != oldDigest.SHA256 {
		return true, false, errors.New("rewritten-version bytes do not match null version")
	}

	afterVersions, err := store.ListVersions(ctx, listed.Key)
	if err != nil {
		return true, false, fmt.Errorf("snapshot object versions after rewrite: %w", err)
	}
	if err := requireSingleVersionAdded(beforeVersions, afterVersions, verified.Object); err != nil {
		return true, false, err
	}

	finalCurrent, err := store.StatCurrent(ctx, listed.Key)
	if err != nil {
		return true, false, fmt.Errorf("verify current object after rewrite: %w", err)
	}
	if !sameObjectIdentity(verified.Object, finalCurrent) {
		return true, false, errors.New("latest object identity changed during rewrite verification")
	}
	if err := store.PutCompletionRecord(ctx, completionRecordDigest(verified)); err != nil {
		return true, false, fmt.Errorf("write backfill completion record: %w", err)
	}
	finalCurrent, err = store.StatCurrent(ctx, listed.Key)
	if err != nil {
		return true, false, fmt.Errorf("verify current object after completion record: %w", err)
	}
	if !sameObjectIdentity(verified.Object, finalCurrent) {
		return true, false, errors.New("latest object identity changed while recording rewrite completion")
	}
	return true, true, nil
}

func requireVersioning(ctx context.Context, store Store, when string) error {
	enabled, err := store.BucketVersioningEnabled(ctx)
	if err != nil {
		return fmt.Errorf("verify bucket versioning %s: %w", when, err)
	}
	if !enabled {
		if when == "after backfill" {
			return errors.New("bucket versioning was disabled during backfill")
		}
		return fmt.Errorf("bucket versioning is not enabled %s", when)
	}
	return nil
}

func requireSafeBucket(ctx context.Context, store Store, when string) error {
	security, err := store.BucketSecurity(ctx)
	if err != nil {
		return fmt.Errorf("verify bucket security %s: %w", when, err)
	}
	if security.ObjectLockConfigured {
		return fmt.Errorf("bucket object lock is enabled or configured %s", when)
	}
	if security.EncryptionConfigured {
		return fmt.Errorf("bucket encryption is configured %s", when)
	}
	return nil
}

type marker struct {
	Schema           string
	ContentSHA256    [sha256.Size]byte
	MetadataSHA256   [sha256.Size]byte
	VersionSetSHA256 [sha256.Size]byte
}

type verifiedVersion struct {
	Object   Object
	Metadata Metadata
	Tags     map[string]string
	Digest   Digest
	Marker   marker
}

type sourceExpectation struct {
	Object         Object
	Metadata       Metadata
	Tags           map[string]string
	Digest         Digest
	MetadataDigest [sha256.Size]byte
}

func verifyMarkedVersion(
	ctx context.Context,
	store Store,
	candidate Object,
	expectedSource *sourceExpectation,
) (verifiedVersion, error) {
	if isNullVersion(candidate.VersionID) {
		return verifiedVersion{}, errors.New("marked object does not have a real version identifier")
	}
	pinned, err := store.StatVersion(ctx, candidate.Key, candidate.VersionID)
	if err != nil {
		return verifiedVersion{}, fmt.Errorf("stat pinned marked version: %w", err)
	}
	if isNullVersion(pinned.VersionID) || !sameObjectIdentity(candidate, pinned) {
		return verifiedVersion{}, errors.New("pinned marked-version identity does not match current object")
	}
	if pinned.Security.unsafe() {
		return verifiedVersion{}, errors.New("marked version has unsupported encryption or object-lock state")
	}

	parsedMarker, metadata, err := parseMarkers(pinned.Metadata)
	if err != nil {
		return verifiedVersion{}, err
	}
	tags, err := store.GetTags(ctx, candidate.Key, candidate.VersionID)
	if err != nil {
		return verifiedVersion{}, fmt.Errorf("read pinned marked-version tags: %w", err)
	}
	metadataDigest, err := canonicalStateDigest(metadata, tags)
	if err != nil {
		return verifiedVersion{}, fmt.Errorf("digest pinned marked-version metadata and tags: %w", err)
	}
	if metadataDigest != parsedMarker.MetadataSHA256 {
		return verifiedVersion{}, errors.New("marked-version metadata and tags do not match marker digest")
	}

	digest, err := store.SHA256(ctx, candidate.Key, candidate.VersionID, pinned.ETag)
	if err != nil {
		return verifiedVersion{}, fmt.Errorf("hash pinned marked version: %w", err)
	}
	if digest.Size != pinned.Size {
		return verifiedVersion{}, errors.New("marked-version byte count does not match object size")
	}
	if digest.SHA256 != parsedMarker.ContentSHA256 {
		return verifiedVersion{}, errors.New("marked-version SHA-256 does not match marker digest")
	}
	if err := requireStoredChecksum(pinned, digest); err != nil {
		return verifiedVersion{}, err
	}
	if expectedSource != nil {
		if err := requireVerifiedSource(parsedMarker, metadata, tags, digest, expectedSource); err != nil {
			return verifiedVersion{}, err
		}
	}

	finalCurrent, err := store.StatCurrent(ctx, candidate.Key)
	if err != nil {
		return verifiedVersion{}, fmt.Errorf("restat current marked version: %w", err)
	}
	if !sameObjectIdentity(pinned, finalCurrent) {
		return verifiedVersion{}, errors.New("marked version stopped being current during verification")
	}
	return verifiedVersion{
		Object: pinned, Metadata: metadata, Tags: tags, Digest: digest, Marker: parsedMarker,
	}, nil
}

func requireCompletionRecord(ctx context.Context, store Store, verified verifiedVersion) error {
	proof := completionRecordDigest(verified)
	exists, err := store.CompletionRecordExists(ctx, proof)
	if err != nil {
		return fmt.Errorf("read completion record: %w", err)
	}
	if exists {
		return nil
	}

	source, err := readSourceExpectation(ctx, store, verified.Object.Key)
	if err != nil {
		return err
	}
	if err := requireVerifiedSource(
		verified.Marker, verified.Metadata, verified.Tags, verified.Digest, source,
	); err != nil {
		return err
	}
	versions, err := store.ListVersions(ctx, verified.Object.Key)
	if err != nil {
		return fmt.Errorf("snapshot object versions before completing marked version: %w", err)
	}
	withoutMarked, err := versionSetWithout(versions, verified.Object)
	if err != nil {
		return err
	}
	if err := requireSourceVersion(withoutMarked, source.Object); err != nil {
		return err
	}
	switch verified.Marker.Schema {
	case markerSchemaVersion:
		versionSetDigest, digestErr := canonicalVersionSetDigest(withoutMarked)
		if digestErr != nil {
			return fmt.Errorf("digest pending pre-rewrite version set: %w", digestErr)
		}
		if versionSetDigest != verified.Marker.VersionSetSHA256 {
			return errors.New("object version set changed concurrently during rewrite")
		}
	case markerLegacySchema:
		if len(withoutMarked) != 1 {
			return errors.New("legacy marked object has an unverifiable version set")
		}
	default:
		return errors.New("marked version has unsupported marker schema")
	}
	if err := store.PutCompletionRecord(ctx, proof); err != nil {
		return fmt.Errorf("write backfill completion record: %w", err)
	}
	finalCurrent, err := store.StatCurrent(ctx, verified.Object.Key)
	if err != nil {
		return fmt.Errorf("restat current marked version after completion: %w", err)
	}
	if !sameObjectIdentity(verified.Object, finalCurrent) {
		return errors.New("marked version stopped being current while recording completion")
	}
	return nil
}

func requireVerifiedSource(
	parsedMarker marker,
	metadata Metadata,
	tags map[string]string,
	digest Digest,
	expectedSource *sourceExpectation,
) error {
	if parsedMarker.ContentSHA256 != expectedSource.Digest.SHA256 ||
		parsedMarker.MetadataSHA256 != expectedSource.MetadataDigest {
		return errors.New("marked-version markers do not match pinned null source")
	}
	if digest.Size != expectedSource.Digest.Size || digest.SHA256 != expectedSource.Digest.SHA256 {
		return errors.New("marked-version bytes do not match pinned null source")
	}
	if !equalMetadata(metadata, expectedSource.Metadata) || !equalMap(tags, expectedSource.Tags) {
		return errors.New("marked-version metadata or tags do not match pinned null source")
	}
	return nil
}

func readSourceExpectation(ctx context.Context, store Store, key string) (*sourceExpectation, error) {
	source, err := store.StatVersion(ctx, key, NullVersionID)
	if err != nil {
		return nil, fmt.Errorf("stat pinned null source for marked version: %w", err)
	}
	if !isNullVersion(source.VersionID) {
		return nil, errors.New("marked version has no pinned null source")
	}
	if source.Security.unsafe() {
		return nil, errors.New("pinned null source has unsupported encryption or object-lock state")
	}
	if hasReservedMarker(source.Metadata) {
		return nil, errors.New("pinned null source contains reserved backfill metadata")
	}
	tags, err := store.GetTags(ctx, key, NullVersionID)
	if err != nil {
		return nil, fmt.Errorf("read pinned null-source tags for marked version: %w", err)
	}
	metadataDigest, err := canonicalStateDigest(source.Metadata, tags)
	if err != nil {
		return nil, fmt.Errorf("digest pinned null-source metadata and tags: %w", err)
	}
	digest, err := store.SHA256(ctx, key, NullVersionID, source.ETag)
	if err != nil {
		return nil, fmt.Errorf("hash pinned null source for marked version: %w", err)
	}
	if digest.Size != source.Size {
		return nil, errors.New("pinned null-source byte count does not match object size")
	}
	if err := requireStoredChecksum(source, digest); err != nil {
		return nil, err
	}
	return &sourceExpectation{
		Object: source, Metadata: source.Metadata, Tags: tags,
		Digest: digest, MetadataDigest: metadataDigest,
	}, nil
}

func metadataWithMarkers(
	metadata Metadata,
	contentDigest, metadataDigest, versionSetDigest [sha256.Size]byte,
) (Metadata, error) {
	if hasReservedMarker(metadata) {
		return Metadata{}, errors.New("source metadata uses reserved backfill marker names")
	}
	marked := Metadata{Headers: cloneMap(metadata.Headers), User: cloneMap(metadata.User)}
	marked.User[markerSchemaKey] = markerSchemaVersion
	marked.User[markerContentSHA256Key] = hex.EncodeToString(contentDigest[:])
	marked.User[markerMetadataSHA256Key] = hex.EncodeToString(metadataDigest[:])
	marked.User[markerVersionSetKey] = hex.EncodeToString(versionSetDigest[:])
	return marked, nil
}

func hasReservedMarker(metadata Metadata) bool {
	for name := range metadata.User {
		if strings.HasPrefix(strings.ToLower(name), markerPrefix) {
			return true
		}
	}
	return false
}

func parseMarkers(metadata Metadata) (marker, Metadata, error) {
	clean := Metadata{Headers: cloneMap(metadata.Headers), User: make(map[string]string, len(metadata.User))}
	values := make(map[string]string, 4)
	for name, value := range metadata.User {
		normalized := strings.ToLower(name)
		if !strings.HasPrefix(normalized, markerPrefix) {
			clean.User[name] = value
			continue
		}
		switch normalized {
		case markerSchemaKey, markerContentSHA256Key, markerMetadataSHA256Key, markerVersionSetKey:
		default:
			return marker{}, Metadata{}, errors.New("marked version contains an unknown reserved marker")
		}
		if _, duplicate := values[normalized]; duplicate {
			return marker{}, Metadata{}, errors.New("marked version contains duplicate reserved markers")
		}
		values[normalized] = value
	}
	schema := values[markerSchemaKey]
	expectedValues := 4
	if schema == markerLegacySchema {
		expectedValues = 3
	}
	if len(values) != expectedValues || (schema != markerSchemaVersion && schema != markerLegacySchema) {
		return marker{}, Metadata{}, errors.New("marked version has incomplete or unsupported markers")
	}
	contentDigest, err := parseMarkerDigest(values[markerContentSHA256Key])
	if err != nil {
		return marker{}, Metadata{}, errors.New("marked version has malformed content digest marker")
	}
	metadataDigest, err := parseMarkerDigest(values[markerMetadataSHA256Key])
	if err != nil {
		return marker{}, Metadata{}, errors.New("marked version has malformed metadata digest marker")
	}
	var versionSetDigest [sha256.Size]byte
	if schema == markerSchemaVersion {
		versionSetDigest, err = parseMarkerDigest(values[markerVersionSetKey])
		if err != nil {
			return marker{}, Metadata{}, errors.New("marked version has malformed version-set digest marker")
		}
	}
	return marker{
		Schema: schema, ContentSHA256: contentDigest, MetadataSHA256: metadataDigest,
		VersionSetSHA256: versionSetDigest,
	}, clean, nil
}

func parseMarkerDigest(value string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return digest, errors.New("invalid digest encoding")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(digest) {
		return digest, errors.New("invalid digest encoding")
	}
	copy(digest[:], decoded)
	return digest, nil
}

func canonicalStateDigest(metadata Metadata, tags map[string]string) ([sha256.Size]byte, error) {
	digest := sha256.New()
	digest.Write([]byte("emfont-object-version-backfill-state-v1\x00"))
	if err := writeCanonicalMap(digest, 'h', metadata.Headers, true); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("invalid preserved header names: %w", err)
	}
	if err := writeCanonicalMap(digest, 'u', metadata.User, true); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("invalid user metadata names: %w", err)
	}
	if err := writeCanonicalMap(digest, 't', tags, false); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("invalid tag names: %w", err)
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func canonicalVersionSetDigest(versions []Version) ([sha256.Size]byte, error) {
	indexed, err := indexVersions(versions)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	versionIDs := make([]string, 0, len(indexed))
	for versionID := range indexed {
		versionIDs = append(versionIDs, versionID)
	}
	sort.Strings(versionIDs)
	digest := sha256.New()
	digest.Write([]byte("emfont-object-version-backfill-version-set-v1\x00"))
	writeCanonicalLength(digest, len(versionIDs))
	for _, versionID := range versionIDs {
		version := indexed[versionID]
		writeCanonicalString(digest, versionID)
		writeCanonicalString(digest, strings.Trim(version.ETag, `"`))
		writeCanonicalInt64(digest, version.Size)
		writeCanonicalInt64(digest, version.LastModified.UTC().UnixNano())
		if version.DeleteMarker {
			digest.Write([]byte{1})
		} else {
			digest.Write([]byte{0})
		}
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func completionRecordDigest(verified verifiedVersion) [sha256.Size]byte {
	digest := sha256.New()
	digest.Write([]byte("emfont-object-version-backfill-completion-v1\x00"))
	writeCanonicalString(digest, verified.Object.Key)
	writeCanonicalString(digest, normalizedVersionID(verified.Object.VersionID))
	writeCanonicalString(digest, strings.Trim(verified.Object.ETag, `"`))
	writeCanonicalInt64(digest, verified.Object.Size)
	writeCanonicalString(digest, verified.Marker.Schema)
	digest.Write(verified.Marker.ContentSHA256[:])
	digest.Write(verified.Marker.MetadataSHA256[:])
	digest.Write(verified.Marker.VersionSetSHA256[:])
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func requireStoredChecksum(object Object, digest Digest) error {
	if object.Checksum.empty() {
		if !digest.Checksum.empty() {
			return errors.New("object bytes unexpectedly produced a stored checksum")
		}
		return nil
	}
	if digest.Checksum != object.Checksum {
		return errors.New("stored checksum does not match object bytes")
	}
	return nil
}

func writeCanonicalMap(destination hash.Hash, section byte, source map[string]string, foldNames bool) error {
	normalized := make(map[string]string, len(source))
	for name, value := range source {
		if foldNames {
			name = strings.ToLower(name)
		}
		if _, duplicate := normalized[name]; duplicate {
			return errors.New("duplicate normalized name")
		}
		normalized[name] = value
	}
	names := make([]string, 0, len(normalized))
	for name := range normalized {
		names = append(names, name)
	}
	sort.Strings(names)
	destination.Write([]byte{section})
	writeCanonicalLength(destination, len(names))
	for _, name := range names {
		writeCanonicalString(destination, name)
		writeCanonicalString(destination, normalized[name])
	}
	return nil
}

func writeCanonicalString(destination hash.Hash, value string) {
	writeCanonicalLength(destination, len(value))
	destination.Write([]byte(value))
}

func writeCanonicalLength(destination hash.Hash, length int) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(length))
	destination.Write(encoded[:])
}

func writeCanonicalInt64(destination hash.Hash, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	destination.Write(encoded[:])
}

func isNullVersion(versionID string) bool {
	versionID = strings.TrimSpace(versionID)
	return versionID == "" || versionID == NullVersionID
}

func normalizedVersionID(versionID string) string {
	if isNullVersion(versionID) {
		return NullVersionID
	}
	return versionID
}

func sameObjectIdentity(left, right Object) bool {
	if normalizedVersionID(left.VersionID) != normalizedVersionID(right.VersionID) ||
		strings.Trim(left.ETag, "\"") != strings.Trim(right.ETag, "\"") ||
		left.Size != right.Size {
		return false
	}
	return sameKnownTimestamp(left.LastModified, right.LastModified)
}

func sameVersionIdentity(left, right Version) bool {
	if normalizedVersionID(left.VersionID) != normalizedVersionID(right.VersionID) ||
		left.DeleteMarker != right.DeleteMarker ||
		strings.Trim(left.ETag, "\"") != strings.Trim(right.ETag, "\"") ||
		left.Size != right.Size {
		return false
	}
	return sameKnownTimestamp(left.LastModified, right.LastModified)
}

func sameKnownTimestamp(left, right time.Time) bool {
	if left.IsZero() || right.IsZero() {
		return true
	}
	// S3 HEAD Last-Modified values have second precision while version-list
	// timestamps can contain milliseconds.
	return left.UTC().Truncate(time.Second).Equal(right.UTC().Truncate(time.Second))
}

func requireSourceVersion(versions []Version, source Object) error {
	for _, version := range versions {
		if version.DeleteMarker || !isNullVersion(version.VersionID) {
			continue
		}
		if sameVersionIdentity(version, Version{
			VersionID: source.VersionID, ETag: source.ETag, Size: source.Size,
			LastModified: source.LastModified,
		}) {
			return nil
		}
	}
	return errors.New("version snapshot does not contain the pinned null version")
}

func requireSingleVersionAdded(before, after []Version, added Object) error {
	beforeByID, err := indexVersions(before)
	if err != nil {
		return fmt.Errorf("invalid version snapshot before rewrite: %w", err)
	}
	afterByID, err := indexVersions(after)
	if err != nil {
		return fmt.Errorf("invalid version snapshot after rewrite: %w", err)
	}
	addedID := normalizedVersionID(added.VersionID)
	if _, exists := beforeByID[addedID]; exists {
		return errors.New("rewrite reused an existing object version identifier")
	}
	if len(afterByID) != len(beforeByID)+1 {
		return errors.New("object version set changed concurrently during rewrite")
	}
	for versionID, oldVersion := range beforeByID {
		newVersion, exists := afterByID[versionID]
		if !exists || !sameVersionIdentity(oldVersion, newVersion) {
			return errors.New("existing object version changed concurrently during rewrite")
		}
	}
	addedVersion, exists := afterByID[addedID]
	if !exists || addedVersion.DeleteMarker || !sameVersionIdentity(addedVersion, Version{
		VersionID: added.VersionID, ETag: added.ETag, Size: added.Size,
		LastModified: added.LastModified,
	}) {
		return errors.New("version snapshot does not contain exactly the rewritten version")
	}
	return nil
}

func versionSetWithout(versions []Version, removed Object) ([]Version, error) {
	removedVersion := Version{
		VersionID: removed.VersionID, ETag: removed.ETag, Size: removed.Size,
		LastModified: removed.LastModified,
	}
	remaining := make([]Version, 0, len(versions)-1)
	found := false
	for _, version := range versions {
		if normalizedVersionID(version.VersionID) != normalizedVersionID(removed.VersionID) {
			remaining = append(remaining, version)
			continue
		}
		if found || !sameVersionIdentity(version, removedVersion) {
			return nil, errors.New("version snapshot does not contain exactly the marked version")
		}
		found = true
	}
	if !found {
		return nil, errors.New("version snapshot does not contain the marked version")
	}
	return remaining, nil
}

func indexVersions(versions []Version) (map[string]Version, error) {
	indexed := make(map[string]Version, len(versions))
	for _, version := range versions {
		versionID := normalizedVersionID(version.VersionID)
		if _, exists := indexed[versionID]; exists {
			return nil, errors.New("duplicate object version identifier")
		}
		indexed[versionID] = version
	}
	return indexed, nil
}

func cloneObject(object Object) Object {
	object.Metadata = Metadata{
		Headers: cloneMap(object.Metadata.Headers),
		User:    cloneMap(object.Metadata.User),
	}
	return object
}

func cloneMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func equalMetadata(left, right Metadata) bool {
	return equalMap(left.Headers, right.Headers) && equalMap(left.User, right.User)
}

func equalMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
