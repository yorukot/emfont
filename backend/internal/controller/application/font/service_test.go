package font

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/emfont/emfont/backend/internal/domain/font"
)

func TestGenerateUsesReadyArtifactWithoutRebuilding(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := &fakeBuilder{}
	source := repository.source
	hash := domain.DynamicWordHash(repository.family.ID, 400, "AB")
	key := domain.DynamicArtifactKey(hash, repository.family.ID, 400, domain.SourceFingerprint(source), domain.DefaultBuilderVersion)
	objectKey := domain.DynamicObjectKey(hash, repository.family.ID, 400, domain.BuildRevision(domain.SourceFingerprint(source), domain.DefaultBuilderVersion))
	checksum := sha256.Sum256([]byte("wOF2"))
	checksumHex := hex.EncodeToString(checksum[:])
	repository.artifacts[key] = domain.Artifact{
		Key: key, Kind: domain.BuildModeDynamic, Status: "ready", FamilyID: repository.family.ID, Weight: 400,
		WordHash: hash, NormalizedWordSet: "AB", SourceChecksum: domain.SourceFingerprint(source),
		BuilderVersion: domain.DefaultBuilderVersion, ProtocolVersion: domain.ArtifactProtocolVersion,
		ObjectKey: objectKey, ObjectVersionID: "version-ready", ContentType: domain.ContentTypeWOFF2,
		SizeBytes: 4, ChecksumSHA256: checksumHex, Generation: 1,
	}
	objects.objects[objectKey] = []byte("wOF2")
	objects.metadata[objectKey] = ObjectInfo{
		VersionID: "version-ready", ChecksumSHA256: checksumHex, ChecksumVerified: true,
	}

	service, err := NewService(repository, objects, builder, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	response, err := service.Generate(context.Background(), GenerateRequest{
		FontID: repository.family.ID, Words: "BA", Min: true, Weight: "400",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(response.Location) != 1 || response.Location[0] != "https://fonts.example/"+objectKey+"?versionId=version-ready" {
		t.Fatalf("locations = %#v", response.Location)
	}
	if builder.calls.Load() != 0 {
		t.Fatalf("builder calls = %d, want 0", builder.calls.Load())
	}
}

func TestGenerateRejectsReadyArtifactIdentityMismatch(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	hash := domain.DynamicWordHash(repository.family.ID, 400, "AB")
	key := domain.DynamicArtifactKey(
		hash, repository.family.ID, 400, domain.SourceFingerprint(repository.source), domain.DefaultBuilderVersion,
	)
	repository.artifacts[key] = domain.Artifact{
		Key: key, Kind: domain.BuildModeDynamic, Status: "ready", FamilyID: "DifferentFont", Weight: 400,
		WordHash: hash, NormalizedWordSet: "AB", SourceChecksum: domain.SourceFingerprint(repository.source),
		BuilderVersion: domain.DefaultBuilderVersion, ProtocolVersion: domain.ArtifactProtocolVersion,
		ObjectKey: "_generated/wrong.woff2", ContentType: domain.ContentTypeWOFF2,
	}
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB"))
	if !errors.Is(err, domain.ErrArtifactConflict) {
		t.Fatalf("Generate error = %v, want ErrArtifactConflict", err)
	}
	if repository.acquireCalls.Load() != 0 {
		t.Fatalf("lease acquisitions = %d, want 0", repository.acquireCalls.Load())
	}
}

func TestGenerateMapsMissingArtifactReservationCapacity(t *testing.T) {
	repository := newFakeRepository()
	repository.markMissingErr = domain.ErrArtifactCapacity
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	hash := domain.DynamicWordHash(repository.family.ID, 400, "AB")
	key := domain.DynamicArtifactKey(
		hash, repository.family.ID, 400, domain.SourceFingerprint(repository.source), domain.DefaultBuilderVersion,
	)
	repository.artifacts[key] = domain.Artifact{
		Key: key, Kind: domain.BuildModeDynamic, Status: "ready", FamilyID: repository.family.ID, Weight: 400,
		WordHash: hash, NormalizedWordSet: "AB", SourceChecksum: domain.SourceFingerprint(repository.source),
		BuilderVersion: domain.DefaultBuilderVersion, ProtocolVersion: domain.ArtifactProtocolVersion,
		ObjectKey: "_generated/missing.woff2", ObjectVersionID: "missing-version",
		ContentType: domain.ContentTypeWOFF2, SizeBytes: 1, Generation: 7,
	}
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB"))
	if !errors.Is(err, ErrArtifactCapacity) {
		t.Fatalf("Generate error = %v, want ErrArtifactCapacity", err)
	}
	if RetryAfterDuration(err) != time.Second {
		t.Fatalf("retry after = %s, want 1s", RetryAfterDuration(err))
	}
	if repository.acquireCalls.Load() != 0 {
		t.Fatalf("lease acquisitions = %d, want 0", repository.acquireCalls.Load())
	}
}

func TestGenerateBuildsCompleteStaticPacks(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := &fakeBuilder{}
	service, err := NewService(repository, objects, builder, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	response, err := service.Generate(context.Background(), GenerateRequest{
		FontID: repository.family.ID, Words: "AB", Min: false, Weight: "400", Format: "woff2",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if response.BuildMode != domain.BuildModeStatic || len(response.Location) != 1 || response.Location[0] == "" {
		t.Fatalf("static response = %#v", response)
	}
	if builder.calls.Load() != 1 {
		t.Fatalf("builder calls = %d, want 1", builder.calls.Load())
	}
}

func TestCSSUsesSeparateUnicodeRangedFaceForEachStaticPack(t *testing.T) {
	repository := newFakeRepository()
	repository.staticPacks = []domain.StaticPackSnapshot{
		{Version: 100, Number: 1, Characters: "AC", CoverageComplete: true},
		{Version: 100, Number: 2, Characters: "BDE", CoverageComplete: true},
	}
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := &fakeBuilder{}
	service, err := NewService(repository, objects, builder, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	stylesheet, err := service.CSS(context.Background(), GenerateRequest{
		FontID: repository.family.ID, Words: "AB", Weight: "400", Format: "woff2",
	})
	if err != nil {
		t.Fatalf("CSS: %v", err)
	}
	blocks := strings.Split(strings.TrimSpace(stylesheet), "\n\n")
	if len(blocks) != 2 {
		t.Fatalf("font-face blocks = %d, want 2; stylesheet = %q", len(blocks), stylesheet)
	}
	expected := []struct {
		pack         string
		unicodeRange string
	}{
		{pack: "/001-", unicodeRange: "unicode-range: U+41, U+43;"},
		{pack: "/002-", unicodeRange: "unicode-range: U+42, U+44-45;"},
	}
	for index, block := range blocks {
		if strings.Count(block, "src: url(") != 1 {
			t.Fatalf("block %d has fallback or missing sources: %q", index, block)
		}
		if !strings.Contains(block, expected[index].pack) || !strings.Contains(block, expected[index].unicodeRange) {
			t.Fatalf("block %d does not map pack URL to its glyph range: %q", index, block)
		}
	}
	if builder.calls.Load() != 2 {
		t.Fatalf("builder calls = %d, want 2", builder.calls.Load())
	}
}

func TestGenerateFallsBackToDynamicWhenStaticCoverageIsIncomplete(t *testing.T) {
	tests := []struct {
		name  string
		packs []domain.StaticPackSnapshot
	}{
		{name: "no matching packs"},
		{name: "partial coverage", packs: []domain.StaticPackSnapshot{{
			Version: 100, Number: 1, Characters: "A", CoverageComplete: false,
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newFakeRepository()
			repository.staticPacks = test.packs
			objects := newFakeObjectStore()
			objects.objects[repository.source.ObjectKey] = []byte("source-font")
			builder := &fakeBuilder{}
			service, err := NewService(repository, objects, builder, Config{})
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}

			response, err := service.Generate(context.Background(), GenerateRequest{
				FontID: repository.family.ID, Words: "AB", Min: false, Weight: "400",
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if response.BuildMode != domain.BuildModeDynamic || len(response.Location) != 1 || response.Location[0] == "" {
				t.Fatalf("fallback response = %#v", response)
			}
			if builder.calls.Load() != 1 {
				t.Fatalf("builder calls = %d, want 1", builder.calls.Load())
			}
		})
	}
}

func TestGenerateFallsBackToDynamicWhenStaticPlanExceedsLimits(t *testing.T) {
	tooManyPacks := make([]domain.StaticPackSnapshot, maxStaticPacks+1)
	for index := range tooManyPacks {
		tooManyPacks[index] = domain.StaticPackSnapshot{
			Version: 100, Number: index, Characters: "A", CoverageComplete: true,
		}
	}
	oversizedCharacters := make([]rune, maxStaticPackCodepoints+1)
	oversizedCharacters[0] = 'A'
	for index := 1; index < len(oversizedCharacters); index++ {
		oversizedCharacters[index] = rune(0x1000 + index)
	}

	tests := []struct {
		name  string
		packs []domain.StaticPackSnapshot
	}{
		{name: "too many packs", packs: tooManyPacks},
		{name: "too many codepoints in one pack", packs: []domain.StaticPackSnapshot{{
			Version: 100, Number: 1, Characters: string(oversizedCharacters), CoverageComplete: true,
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newFakeRepository()
			repository.staticPacks = test.packs
			objects := newFakeObjectStore()
			objects.objects[repository.source.ObjectKey] = []byte("source-font")
			builder := &fakeBuilder{}
			service, err := NewService(repository, objects, builder, Config{})
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}

			response, err := service.Generate(context.Background(), GenerateRequest{
				FontID: repository.family.ID, Words: "A", Weight: "400",
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if response.BuildMode != domain.BuildModeDynamic || len(response.Location) != 1 {
				t.Fatalf("fallback response = %#v", response)
			}
			if builder.calls.Load() != 1 || repository.acquireCalls.Load() != 1 {
				t.Fatalf("builder calls = %d, lease acquisitions = %d; want one dynamic build",
					builder.calls.Load(), repository.acquireCalls.Load())
			}
			if !repository.hasArtifact(dynamicArtifactKey(repository, "A")) || repository.artifactCount() != 1 {
				t.Fatalf("artifacts = %d, want only the dynamic fallback artifact", repository.artifactCount())
			}
		})
	}
}

func TestGenerateRejectsUnsupportedTargetFormat(t *testing.T) {
	repository := newFakeRepository()
	service, err := NewService(repository, newFakeObjectStore(), &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, err = service.Generate(context.Background(), GenerateRequest{
		FontID: repository.family.ID, Words: "AB", Min: true, Weight: "400", Format: "ttf",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Generate error = %v, want ErrInvalidInput", err)
	}
}

func TestGenerateCachesUnsupportedCodepointFailure(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := &errorBuilder{err: ErrUnsupportedCodepoints}
	service, err := NewService(repository, objects, builder, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	for request := 0; request < 2; request++ {
		_, err := service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB"))
		if !errors.Is(err, ErrUnsupportedCodepoints) {
			t.Fatalf("Generate %d error = %v, want ErrUnsupportedCodepoints", request, err)
		}
	}
	if builder.calls.Load() != 1 {
		t.Fatalf("builder calls = %d, want 1", builder.calls.Load())
	}
	if repository.acquireCalls.Load() != 1 {
		t.Fatalf("lease acquisitions = %d, want 1", repository.acquireCalls.Load())
	}
	artifact, ok := repository.artifact(dynamicArtifactKey(repository, "AB"))
	if !ok || artifact.Status != "failed" || artifact.FailureCode != domain.FailureCodeUnsupportedCodepoints {
		t.Fatalf("terminal artifact = %#v, exists = %t", artifact, ok)
	}
}

func TestGenerateMapsTerminalFailureCachedDuringArtifactCreation(t *testing.T) {
	repository := newFakeRepository()
	repository.createErr = domain.ErrTerminalFailureCached
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := &fakeBuilder{}
	service, err := NewService(repository, objects, builder, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB"))
	if !errors.Is(err, ErrUnsupportedCodepoints) {
		t.Fatalf("Generate error = %v, want ErrUnsupportedCodepoints", err)
	}
	if builder.calls.Load() != 0 || repository.acquireCalls.Load() != 0 {
		t.Fatalf("builder calls = %d, lease acquisitions = %d; want zero", builder.calls.Load(), repository.acquireCalls.Load())
	}
}

func TestCSSVerifiesOriginalSourceExists(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if _, err := service.CSS(context.Background(), GenerateRequest{FontID: repository.family.ID}); !errors.Is(err, ErrFontSourceNotFound) {
		t.Fatalf("CSS missing source error = %v, want ErrFontSourceNotFound", err)
	}
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	stylesheet, err := service.CSS(context.Background(), GenerateRequest{FontID: repository.family.ID})
	if err != nil {
		t.Fatalf("CSS: %v", err)
	}
	if !bytes.Contains([]byte(stylesheet), []byte("https://fonts.example/"+repository.source.ObjectKey+"?versionId=source-version-1")) {
		t.Fatalf("stylesheet = %q", stylesheet)
	}
}

func TestCSSPinsSourceVersionAcrossLatestObjectReplacement(t *testing.T) {
	repository := newFakeRepository()
	objects := newReplacingSourceStore(repository.source.ObjectKey, []byte("source-font"), []byte("replacement-font"))
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	stylesheet, err := service.CSS(context.Background(), GenerateRequest{FontID: repository.family.ID})
	if err != nil {
		t.Fatalf("CSS: %v", err)
	}
	if !strings.Contains(stylesheet, "?versionId=source-version-1") {
		t.Fatalf("stylesheet = %q, want pinned source version", stylesheet)
	}
	stats, _, urls := objects.calls()
	if len(stats) != 2 || stats[0] != "" || stats[1] != "source-version-1" {
		t.Fatalf("source stat versions = %#v", stats)
	}
	if len(urls) != 1 || urls[0] != "source-version-1" {
		t.Fatalf("source URL versions = %#v", urls)
	}
}

func TestGenerateOpensPinnedSourceVersionAcrossLatestObjectReplacement(t *testing.T) {
	repository := newFakeRepository()
	objects := newReplacingSourceStore(repository.source.ObjectKey, []byte("source-font"), []byte("replacement-font"))
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if _, err := service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB")); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	stats, opens, _ := objects.calls()
	if len(stats) != 2 || stats[0] != "" || stats[1] != "source-version-1" {
		t.Fatalf("source stat versions = %#v", stats)
	}
	if len(opens) != 1 || opens[0] != "source-version-1" {
		t.Fatalf("source open versions = %#v", opens)
	}
}

func TestGenerateMapsSourceStreamFailureToObjectStorageUnavailable(t *testing.T) {
	repository := newFakeRepository()
	objects := &readFailingObjectStore{fakeObjectStore: newFakeObjectStore()}
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := &fakeBuilder{}
	service, err := NewService(repository, objects, builder, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB"))
	if !errors.Is(err, ErrObjectStorageUnavailable) {
		t.Fatalf("Generate error = %v, want ErrObjectStorageUnavailable", err)
	}
	if builder.calls.Load() != 0 {
		t.Fatalf("builder calls = %d, want 0", builder.calls.Load())
	}
}

func TestGenerateCacheIdentityChangesForVersionOnlySourceReplacement(t *testing.T) {
	repository := newFakeRepository()
	sourceData := []byte("source-font")
	objects := newReplacingSourceStore(repository.source.ObjectKey, sourceData, sourceData)
	builder := &fakeBuilder{}
	service, err := NewService(repository, objects, builder, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	for request := 0; request < 2; request++ {
		if _, err := service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB")); err != nil {
			t.Fatalf("Generate %d: %v", request, err)
		}
	}
	if builder.calls.Load() != 2 {
		t.Fatalf("builder calls = %d, want 2 for two exact source versions", builder.calls.Load())
	}
	repository.mu.Lock()
	artifactCount := len(repository.artifacts)
	repository.mu.Unlock()
	if artifactCount != 2 {
		t.Fatalf("artifact identities = %d, want 2", artifactCount)
	}
}

func TestGenerateDeduplicatesConcurrentBuilds(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := &fakeBuilder{delay: 25 * time.Millisecond}
	service, err := NewService(repository, objects, builder, Config{BuildTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	const requests = 12
	var group sync.WaitGroup
	errorsSeen := make(chan error, requests)
	for index := 0; index < requests; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, generateErr := service.Generate(context.Background(), GenerateRequest{
				FontID: repository.family.ID, Words: "AB", Min: true, Weight: "400",
			})
			errorsSeen <- generateErr
		}()
	}
	group.Wait()
	close(errorsSeen)
	for generateErr := range errorsSeen {
		if generateErr != nil {
			t.Fatalf("Generate: %v", generateErr)
		}
	}
	if builder.calls.Load() != 1 {
		t.Fatalf("builder calls = %d, want 1", builder.calls.Load())
	}
	if repository.acquireCalls.Load() != 1 {
		t.Fatalf("lease acquisitions = %d, want 1", repository.acquireCalls.Load())
	}
}

func TestGeneratePublishesContentAddressedArtifact(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	response, err := service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	key := dynamicArtifactKey(repository, "AB")
	artifact, ok := repository.artifact(key)
	if !ok {
		t.Fatalf("artifact %q was not created", key)
	}
	checksum := sha256.Sum256([]byte("wOF2-data"))
	checksumHex := hex.EncodeToString(checksum[:])
	baseKey := domain.DynamicObjectKey(
		domain.DynamicWordHash(repository.family.ID, 400, "AB"),
		repository.family.ID,
		400,
		domain.BuildRevision(domain.SourceFingerprint(repository.source), domain.DefaultBuilderVersion),
	)
	expectedKey := domain.FencedContentAddressedObjectKey(baseKey, 1, checksumHex)
	if artifact.Status != "ready" || artifact.ObjectKey != expectedKey || artifact.ObjectVersionID != "version-1" || artifact.Generation != 1 {
		t.Fatalf("published artifact = %#v", artifact)
	}
	if len(response.Location) != 1 || response.Location[0] != "https://fonts.example/"+expectedKey+"?versionId=version-1" {
		t.Fatalf("locations = %#v", response.Location)
	}
	objects.mu.Lock()
	_, baseExists := objects.objects[baseKey]
	_, immutableExists := objects.objects[expectedKey]
	objects.mu.Unlock()
	if baseExists || !immutableExists {
		t.Fatalf("base exists = %v, immutable exists = %v", baseExists, immutableExists)
	}
}

func TestGenerateReportsBoundedBuildAndCacheEvents(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	observer := &recordingObserver{}
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{}, WithObserver(observer))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	for iteration := 0; iteration < 2; iteration++ {
		if _, err := service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB")); err != nil {
			t.Fatalf("Generate %d: %v", iteration, err)
		}
	}

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.cache["miss"] != 1 || observer.cache["hit"] != 1 {
		t.Fatalf("cache events = %#v", observer.cache)
	}
	if observer.admissions["accepted"] != 1 || observer.leases["acquired"] != 1 {
		t.Fatalf("admission events = %#v; lease events = %#v", observer.admissions, observer.leases)
	}
	if observer.builds["dynamic:success"] != 1 {
		t.Fatalf("build events = %#v", observer.builds)
	}
	if observer.active != 0 || observer.queued != 0 {
		t.Fatalf("final queue = active %d queued %d", observer.active, observer.queued)
	}
}

func TestGenerateRejectsSourceMutationDuringRead(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	store := &sourceChangingStore{fakeObjectStore: objects, sourceKey: repository.source.ObjectKey}
	builder := &fakeBuilder{}
	service, err := NewService(repository, store, builder, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB"))
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("Generate error = %v, want ErrBuildFailed", err)
	}
	if builder.calls.Load() != 0 {
		t.Fatalf("builder calls = %d, want 0", builder.calls.Load())
	}
}

func TestReadyArtifactRereadsAfterMissingCASLosesToFreshPublication(t *testing.T) {
	baseRepository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[baseRepository.source.ObjectKey] = []byte("source-font")
	builder := &fakeBuilder{}
	key := dynamicArtifactKey(baseRepository, "AB")
	freshData := []byte("wOF2-fresh")
	freshSum := sha256.Sum256(freshData)
	freshChecksum := hex.EncodeToString(freshSum[:])
	freshKey := "_generated/fresh-" + freshChecksum + ".woff2"
	fresh := domain.Artifact{
		Key: key, Kind: domain.BuildModeDynamic, Status: "ready",
		FamilyID: baseRepository.family.ID, Weight: 400,
		WordHash:          domain.DynamicWordHash(baseRepository.family.ID, 400, "AB"),
		NormalizedWordSet: "AB", SourceChecksum: domain.SourceFingerprint(baseRepository.source),
		BuilderVersion: domain.DefaultBuilderVersion, ProtocolVersion: domain.ArtifactProtocolVersion,
		ObjectKey: freshKey, ObjectVersionID: "fresh-version", ContentType: domain.ContentTypeWOFF2,
		SizeBytes: int64(len(freshData)), ChecksumSHA256: freshChecksum, Generation: 2,
	}
	baseRepository.artifacts[key] = domain.Artifact{
		Key: key, Kind: domain.BuildModeDynamic, Status: "ready",
		FamilyID: baseRepository.family.ID, Weight: 400,
		WordHash:          domain.DynamicWordHash(baseRepository.family.ID, 400, "AB"),
		NormalizedWordSet: "AB", SourceChecksum: domain.SourceFingerprint(baseRepository.source),
		BuilderVersion: domain.DefaultBuilderVersion, ProtocolVersion: domain.ArtifactProtocolVersion,
		ObjectKey: "_generated/missing-old.woff2", ObjectVersionID: "old-version",
		ContentType: domain.ContentTypeWOFF2, SizeBytes: 4, Generation: 1,
	}
	objects.objects[freshKey] = freshData
	objects.metadata[freshKey] = ObjectInfo{
		VersionID: "fresh-version", ChecksumSHA256: freshChecksum, ChecksumVerified: true,
	}
	repository := &casRaceRepository{fakeRepository: baseRepository, replacement: fresh}
	service, err := NewService(repository, objects, builder, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	response, err := service.Generate(context.Background(), dynamicRequest(baseRepository.family.ID, "AB"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(response.Location) != 1 || response.Location[0] != "https://fonts.example/"+freshKey+"?versionId=fresh-version" {
		t.Fatalf("locations = %#v", response.Location)
	}
	if builder.calls.Load() != 0 {
		t.Fatalf("builder calls = %d, want 0", builder.calls.Load())
	}
}

func TestNewServiceBoundsMaxPendingBuilds(t *testing.T) {
	repository := newFakeRepository()
	service, err := NewService(repository, newFakeObjectStore(), &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if service.config.MaxPendingBuilds != defaultMaxPendingBuilds {
		t.Fatalf("MaxPendingBuilds = %d, want %d", service.config.MaxPendingBuilds, defaultMaxPendingBuilds)
	}

	service, err = NewService(repository, newFakeObjectStore(), &fakeBuilder{}, Config{
		StaticBuildConcurrency: 5,
		MaxPendingBuilds:       1,
	})
	if err != nil {
		t.Fatalf("NewService with bounded config: %v", err)
	}
	if service.config.MaxPendingBuilds != 5 {
		t.Fatalf("MaxPendingBuilds = %d, want StaticBuildConcurrency 5", service.config.MaxPendingBuilds)
	}
}

func TestListUsesBoundedKeysetPagination(t *testing.T) {
	repository := newFakeRepository()
	repository.families = []domain.Family{
		{ID: "AFont", Name: "A", Weights: []int{400}},
		{ID: "BFont", Name: "B", Weights: []int{400}},
		{ID: "CFont", Name: "C", Weights: []int{400}},
	}
	service, err := NewService(repository, newFakeObjectStore(), &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	first, err := service.List(context.Background(), ListRequest{Limit: 2})
	if err != nil {
		t.Fatalf("first List: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "AFont" || first.Items[1].ID != "BFont" || first.NextCursor != "BFont" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := service.List(context.Background(), ListRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "CFont" || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
	if _, err := service.List(context.Background(), ListRequest{Limit: maxListLimit + 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversized List error = %v, want ErrInvalidInput", err)
	}
}

func TestGenerateRejectsDistinctBuildWhenQueueFullWithoutCreatingArtifact(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := newControlledBuilder(false)
	service, err := NewService(repository, objects, builder, Config{
		BuildTimeout:           5 * time.Second,
		StaticBuildConcurrency: 1,
		MaxPendingBuilds:       2,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	firstResult := generateAsync(service, context.Background(), repository.family.ID, "A")
	waitForSignal(t, builder.started, "first build to start")
	secondResult := generateAsync(service, context.Background(), repository.family.ID, "B")
	secondKey := dynamicArtifactKey(repository, "B")
	waitForCondition(t, func() bool {
		return repository.hasArtifact(secondKey) && len(service.admission) == 2
	}, "second build to enter the local queue")
	if attempts := repository.acquireAttemptCount(secondKey); attempts != 0 {
		t.Fatalf("queued build lease attempts = %d, want 0 before a local build slot is available", attempts)
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "C"))
	if !errors.Is(err, ErrBuildQueueFull) {
		t.Fatalf("Generate third key error = %v, want ErrBuildQueueFull", err)
	}
	if repository.hasArtifact(dynamicArtifactKey(repository, "C")) {
		t.Fatal("queue-rejected build created an artifact row")
	}

	builder.Release()
	if err := waitForError(t, firstResult, "first build to finish"); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if err := waitForError(t, secondResult, "queued build to finish"); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if attempts := repository.acquireAttemptCount(secondKey); attempts != 1 {
		t.Fatalf("queued build lease attempts = %d, want 1 after it acquired a local build slot", attempts)
	}
}

func TestQueuedBuildDoesNotReserveLeaseAheadOfAnotherService(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	slowBuilder := newControlledBuilder(false)
	t.Cleanup(slowBuilder.Release)
	serviceA, err := NewService(repository, objects, slowBuilder, Config{
		BuildTimeout: 5 * time.Second, StaticBuildConcurrency: 1, MaxPendingBuilds: 2, WorkerID: "service-a",
	})
	if err != nil {
		t.Fatalf("NewService A: %v", err)
	}
	serviceBBuilder := &fakeBuilder{}
	serviceB, err := NewService(repository, objects, serviceBBuilder, Config{
		BuildTimeout: 5 * time.Second, StaticBuildConcurrency: 1, MaxPendingBuilds: 2, WorkerID: "service-b",
	})
	if err != nil {
		t.Fatalf("NewService B: %v", err)
	}

	firstResult := generateAsync(serviceA, context.Background(), repository.family.ID, "A")
	waitForSignal(t, slowBuilder.started, "service A slow build to start")
	queuedResult := generateAsync(serviceA, context.Background(), repository.family.ID, "B")
	queuedKey := dynamicArtifactKey(repository, "B")
	waitForCondition(t, func() bool {
		return repository.hasArtifact(queuedKey) && len(serviceA.admission) == 2
	}, "service A second build to enter its local queue")
	if attempts := repository.acquireAttemptCount(queuedKey); attempts != 0 {
		t.Fatalf("service A queued build lease attempts = %d, want 0", attempts)
	}

	response, err := serviceB.Generate(context.Background(), dynamicRequest(repository.family.ID, "B"))
	if err != nil {
		t.Fatalf("service B Generate: %v", err)
	}
	if len(response.Location) != 1 || serviceBBuilder.calls.Load() != 1 {
		t.Fatalf("service B response = %#v, builder calls = %d", response, serviceBBuilder.calls.Load())
	}
	owners := repository.acquireOwnersFor(queuedKey)
	if len(owners) != 1 || !strings.HasPrefix(owners[0], "service-b:") {
		t.Fatalf("queued artifact lease owners = %#v, want only service B", owners)
	}

	slowBuilder.Release()
	if err := waitForError(t, firstResult, "service A slow build to finish"); err != nil {
		t.Fatalf("service A first Generate: %v", err)
	}
	if err := waitForError(t, queuedResult, "service A queued request to use service B artifact"); err != nil {
		t.Fatalf("service A queued Generate: %v", err)
	}
	if slowBuilder.calls.Load() != 1 {
		t.Fatalf("service A builder calls = %d, want only its first build", slowBuilder.calls.Load())
	}
	if attempts := repository.acquireAttemptCount(queuedKey); attempts != 1 {
		t.Fatalf("queued artifact lease attempts = %d, want only service B's attempt", attempts)
	}
}

func TestGenerateReturnsDatabaseBackoffHintOnLeaseContention(t *testing.T) {
	repository := newFakeRepository()
	repository.retryAfter = 17 * time.Second
	key := dynamicArtifactKey(repository, "AB")
	repository.leased[key] = true
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB"))
	if !errors.Is(err, ErrBuildNotReady) {
		t.Fatalf("Generate error = %v, want ErrBuildNotReady", err)
	}
	if got := RetryAfterDuration(err); got != repository.retryAfter {
		t.Fatalf("RetryAfterDuration = %s, want %s", got, repository.retryAfter)
	}
}

func TestGenerateRequestCancellationDoesNotCancelAdmittedBuild(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := newControlledBuilder(false)
	service, err := NewService(repository, objects, builder, Config{BuildTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	result := generateAsync(service, requestCtx, repository.family.ID, "A")
	waitForSignal(t, builder.started, "admitted build to start")
	cancelRequest()
	if err := waitForError(t, result, "canceled request to return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate error = %v, want context.Canceled", err)
	}
	select {
	case <-builder.finished:
		t.Fatal("request cancellation stopped the admitted build")
	default:
	}

	builder.Release()
	response, err := service.Generate(context.Background(), dynamicRequest(repository.family.ID, "A"))
	if err != nil {
		t.Fatalf("Generate after releasing admitted build: %v", err)
	}
	if len(response.Location) != 1 {
		t.Fatalf("locations = %#v, want one built artifact", response.Location)
	}
	if builder.calls.Load() != 1 {
		t.Fatalf("builder calls = %d, want 1", builder.calls.Load())
	}
}

func TestShutdownCancelsAndWaitsForBuildsAndRejectsNewBuilds(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := newControlledBuilder(true)
	service, err := NewService(repository, objects, builder, Config{BuildTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	buildResult := generateAsync(service, context.Background(), repository.family.ID, "A")
	waitForSignal(t, builder.started, "build to start")
	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- service.Shutdown(context.Background())
	}()
	waitForSignal(t, builder.canceled, "build context cancellation")
	select {
	case err := <-shutdownResult:
		t.Fatalf("Shutdown returned before the build exited: %v", err)
	default:
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "B"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate during shutdown error = %v, want context.Canceled", err)
	}
	if repository.hasArtifact(dynamicArtifactKey(repository, "B")) {
		t.Fatal("build submitted during shutdown created an artifact row")
	}

	builder.Release()
	if err := waitForError(t, shutdownResult, "Shutdown to finish"); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := waitForError(t, buildResult, "canceled build to return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build error = %v, want context.Canceled", err)
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "C"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate after shutdown error = %v, want context.Canceled", err)
	}
	if builder.calls.Load() != 1 {
		t.Fatalf("builder calls = %d, want 1", builder.calls.Load())
	}
}

func TestShutdownStopsWaitingWhenContextIsCanceled(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	builder := newControlledBuilder(true)
	service, err := NewService(repository, objects, builder, Config{BuildTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	buildResult := generateAsync(service, context.Background(), repository.family.ID, "A")
	waitForSignal(t, builder.started, "build to start")
	shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- service.Shutdown(shutdownCtx)
	}()
	waitForSignal(t, builder.canceled, "build context cancellation")
	cancelShutdown()
	if err := waitForError(t, shutdownResult, "Shutdown context cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown error = %v, want context.Canceled", err)
	}
	select {
	case <-builder.finished:
		t.Fatal("build finished before its release")
	default:
	}

	builder.Release()
	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if err := waitForError(t, buildResult, "canceled build to return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build error = %v, want context.Canceled", err)
	}
}

func TestBuildTimeoutIncludesArtifactCreation(t *testing.T) {
	repository := newFakeRepository()
	repository.createStarted = make(chan struct{})
	repository.blockCreate = true
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{BuildTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	result := generateAsync(service, context.Background(), repository.family.ID, "A")
	waitForSignal(t, repository.createStarted, "artifact creation to start")
	if err := waitForError(t, result, "artifact creation timeout"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Generate error = %v, want context.DeadlineExceeded", err)
	}
}

func TestGenerateMapsRepositoryArtifactCapacity(t *testing.T) {
	repository := newFakeRepository()
	repository.createErr = domain.ErrArtifactCapacity
	objects := newFakeObjectStore()
	objects.objects[repository.source.ObjectKey] = []byte("source-font")
	service, err := NewService(repository, objects, &fakeBuilder{}, Config{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	_, err = service.Generate(context.Background(), dynamicRequest(repository.family.ID, "AB"))
	if !errors.Is(err, ErrArtifactCapacity) {
		t.Fatalf("Generate error = %v, want ErrArtifactCapacity", err)
	}
	if RetryAfterDuration(err) < time.Second {
		t.Fatalf("RetryAfterDuration = %s, want at least one second", RetryAfterDuration(err))
	}
	if repository.acquireCalls.Load() != 0 {
		t.Fatalf("lease acquisitions = %d, want 0", repository.acquireCalls.Load())
	}
}

type fakeRepository struct {
	mu              sync.Mutex
	family          domain.Family
	families        []domain.Family
	source          domain.Source
	artifacts       map[string]domain.Artifact
	leased          map[string]bool
	fences          map[string]int64
	acquireAttempts map[string]int
	acquireOwners   map[string][]string
	acquireCalls    atomic.Int32
	acquireEvents   chan string
	createStarted   chan struct{}
	createOnce      sync.Once
	blockCreate     bool
	createErr       error
	markMissingErr  error
	retryAfter      time.Duration
	staticPacks     []domain.StaticPackSnapshot
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		family: domain.Family{ID: "DemoFont", Name: "Demo Font", Weights: []int{400}, Format: "ttf", Version: "1"},
		source: domain.Source{
			FamilyID: "DemoFont", Weight: 400, Format: "ttf", ObjectKey: "original-fonts/DemoFont/400.ttf",
			ObjectVersionID: "source-version-1",
			ChecksumSHA256:  "5d35da932a1537eefd161a24f03bbb1148b488fedc5cdd6325cb3859c9c66467",
		},
		artifacts: make(map[string]domain.Artifact), leased: make(map[string]bool), fences: make(map[string]int64),
		acquireAttempts: make(map[string]int), acquireOwners: make(map[string][]string),
		staticPacks: []domain.StaticPackSnapshot{{
			Version: 100, Number: 1, Characters: "AB", CoverageComplete: true,
		}},
	}
}

func (r *fakeRepository) GetFontFamily(context.Context, string) (domain.Family, error) {
	return r.family, nil
}
func (r *fakeRepository) ListFontFamilies(_ context.Context, _, after string, limit int) ([]domain.Family, error) {
	families := r.families
	if families == nil {
		families = []domain.Family{r.family}
	}
	result := make([]domain.Family, 0, limit)
	for _, family := range families {
		if family.ID <= after {
			continue
		}
		result = append(result, family)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}
func (r *fakeRepository) GetFontSource(context.Context, string, int) (domain.Source, error) {
	return r.source, nil
}
func (r *fakeRepository) GetFontArtifact(_ context.Context, key string) (domain.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	artifact, ok := r.artifacts[key]
	if !ok {
		return domain.Artifact{}, domain.ErrArtifactNotFound
	}
	return artifact, nil
}
func (r *fakeRepository) CreateFontArtifact(ctx context.Context, artifact domain.Artifact) error {
	if r.createStarted != nil {
		r.createOnce.Do(func() { close(r.createStarted) })
	}
	if r.blockCreate {
		<-ctx.Done()
		return ctx.Err()
	}
	if r.createErr != nil {
		return r.createErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.artifacts[artifact.Key]; !ok {
		r.artifacts[artifact.Key] = artifact
	}
	return nil
}
func (r *fakeRepository) MarkFontArtifactReady(
	_ context.Context,
	key string,
	claim domain.BuildClaim,
	object domain.ArtifactObject,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	artifact := r.artifacts[key]
	artifact.Status = "ready"
	artifact.ObjectKey = object.ObjectKey
	artifact.ObjectVersionID = object.VersionID
	artifact.SizeBytes = object.SizeBytes
	artifact.ETag = object.ETag
	artifact.ChecksumSHA256 = object.ChecksumSHA256
	artifact.Generation = claim.Fence
	r.artifacts[key] = artifact
	return nil
}
func (r *fakeRepository) MarkFontArtifactMissing(
	_ context.Context,
	key, objectKey string,
	generation int64,
	_ string,
) (bool, error) {
	if r.markMissingErr != nil {
		return false, r.markMissingErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	artifact, ok := r.artifacts[key]
	if !ok || artifact.Status != "ready" || artifact.ObjectKey != objectKey || artifact.Generation != generation {
		return false, nil
	}
	artifact.Status = "missing"
	r.artifacts[key] = artifact
	return true, nil
}
func (r *fakeRepository) TouchFontArtifact(
	_ context.Context,
	key, objectKey string,
	generation int64,
	_ time.Duration,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	artifact, ok := r.artifacts[key]
	return ok && artifact.Status == "ready" && artifact.ObjectKey == objectKey && artifact.Generation == generation, nil
}
func (r *fakeRepository) GetStaticPackSnapshot(context.Context, string, []string) ([]domain.StaticPackSnapshot, error) {
	return append([]domain.StaticPackSnapshot(nil), r.staticPacks...), nil
}
func (r *fakeRepository) AcquireBuildJob(
	_ context.Context,
	key, owner string,
	_ time.Duration,
) (domain.BuildClaim, bool, error) {
	r.mu.Lock()
	r.acquireAttempts[key]++
	r.acquireOwners[key] = append(r.acquireOwners[key], owner)
	if r.leased[key] {
		r.mu.Unlock()
		return domain.BuildClaim{}, false, nil
	}
	r.leased[key] = true
	r.fences[key]++
	claim := domain.BuildClaim{Owner: owner, Fence: r.fences[key]}
	r.acquireCalls.Add(1)
	artifact := r.artifacts[key]
	artifact.Status = "running"
	r.artifacts[key] = artifact
	r.mu.Unlock()
	if r.acquireEvents != nil {
		r.acquireEvents <- key
	}
	return claim, true, nil
}
func (r *fakeRepository) BuildRetryAfter(context.Context, string) (time.Duration, error) {
	if r.retryAfter > 0 {
		return r.retryAfter, nil
	}
	return time.Second, nil
}
func (r *fakeRepository) FailBuildJob(context.Context, string, domain.BuildClaim, string) error {
	return nil
}
func (r *fakeRepository) FailBuildJobTerminal(
	_ context.Context,
	key string,
	claim domain.BuildClaim,
	failureCode, _ string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	artifact, ok := r.artifacts[key]
	if !ok || r.fences[key] != claim.Fence {
		return domain.ErrBuildNotReady
	}
	artifact.Status = "failed"
	artifact.FailureCode = failureCode
	r.artifacts[key] = artifact
	r.leased[key] = false
	return nil
}

func (r *fakeRepository) hasArtifact(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.artifacts[key]
	return ok
}

func (r *fakeRepository) artifact(key string) (domain.Artifact, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	artifact, ok := r.artifacts[key]
	return artifact, ok
}

func (r *fakeRepository) artifactCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.artifacts)
}

func (r *fakeRepository) acquireAttemptCount(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acquireAttempts[key]
}

func (r *fakeRepository) acquireOwnersFor(key string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.acquireOwners[key]...)
}

type fakeObjectStore struct {
	mu       sync.Mutex
	objects  map[string][]byte
	metadata map[string]ObjectInfo
}

type readFailingObjectStore struct {
	*fakeObjectStore
}

func (s *readFailingObjectStore) OpenObject(
	ctx context.Context,
	key, versionID string,
) (io.ReadCloser, ObjectInfo, error) {
	info, err := s.StatObject(ctx, key, versionID)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	s.mu.Lock()
	data := append([]byte(nil), s.objects[key]...)
	s.mu.Unlock()
	return io.NopCloser(io.MultiReader(
		bytes.NewReader(data[:len(data)/2]),
		&alwaysErrorReader{err: errors.New("source stream interrupted")},
	)), info, nil
}

type alwaysErrorReader struct {
	err error
}

func (r *alwaysErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: make(map[string][]byte), metadata: make(map[string]ObjectInfo)}
}
func (s *fakeObjectStore) StatObject(_ context.Context, key, versionID string) (ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return ObjectInfo{}, ErrObjectNotFound
	}
	info := s.metadata[key]
	info.Key = key
	if info.VersionID == "" {
		info.VersionID = "source-version-1"
	}
	if versionID != "" && info.VersionID != versionID {
		return ObjectInfo{}, ErrObjectNotFound
	}
	info.SizeBytes = int64(len(data))
	checksum := sha256.Sum256(data)
	if info.ChecksumSHA256 == "" {
		info.ChecksumSHA256 = hex.EncodeToString(checksum[:])
	}
	if info.ETag == "" {
		info.ETag = "etag-" + hex.EncodeToString(checksum[:8])
	}
	return info, nil
}
func (s *fakeObjectStore) OpenObject(ctx context.Context, key, versionID string) (io.ReadCloser, ObjectInfo, error) {
	info, err := s.StatObject(ctx, key, versionID)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	s.mu.Lock()
	data := append([]byte(nil), s.objects[key]...)
	s.mu.Unlock()
	return io.NopCloser(bytes.NewReader(data)), info, nil
}
func (s *fakeObjectStore) PutObject(_ context.Context, key string, reader io.Reader, _ int64, options PutObjectOptions) (ObjectInfo, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return ObjectInfo{}, err
	}
	s.mu.Lock()
	s.objects[key] = data
	info := ObjectInfo{
		Key: key, VersionID: "version-1", SizeBytes: int64(len(data)), ETag: "etag",
		ContentType: options.ContentType, ChecksumSHA256: options.ChecksumSHA256, ChecksumVerified: true,
	}
	s.metadata[key] = info
	s.mu.Unlock()
	return info, nil
}
func (s *fakeObjectStore) PublicURL(_ context.Context, key, versionID string) (string, error) {
	location := "https://fonts.example/" + key
	if versionID != "" {
		location += "?versionId=" + versionID
	}
	return location, nil
}

type sourceChangingStore struct {
	*fakeObjectStore
	sourceKey string
}

type replacingSourceStore struct {
	*fakeObjectStore
	sourceKey       string
	initialData     []byte
	replacementData []byte
	stateMu         sync.Mutex
	statVersions    []string
	openVersions    []string
	urlVersions     []string
	latestReplaced  bool
}

func newReplacingSourceStore(sourceKey string, initialData, replacementData []byte) *replacingSourceStore {
	return &replacingSourceStore{
		fakeObjectStore: newFakeObjectStore(), sourceKey: sourceKey,
		initialData: append([]byte(nil), initialData...), replacementData: append([]byte(nil), replacementData...),
	}
}

func (s *replacingSourceStore) StatObject(ctx context.Context, key, versionID string) (ObjectInfo, error) {
	if key != s.sourceKey {
		return s.fakeObjectStore.StatObject(ctx, key, versionID)
	}
	s.stateMu.Lock()
	s.statVersions = append(s.statVersions, versionID)
	resolvedVersion := versionID
	if versionID == "" {
		if s.latestReplaced {
			resolvedVersion = "source-version-2"
		} else {
			resolvedVersion = "source-version-1"
			s.latestReplaced = true
		}
	}
	s.stateMu.Unlock()
	return s.sourceVersion(resolvedVersion)
}

func (s *replacingSourceStore) OpenObject(ctx context.Context, key, versionID string) (io.ReadCloser, ObjectInfo, error) {
	if key != s.sourceKey {
		return s.fakeObjectStore.OpenObject(ctx, key, versionID)
	}
	s.stateMu.Lock()
	s.openVersions = append(s.openVersions, versionID)
	s.stateMu.Unlock()
	info, err := s.sourceVersion(versionID)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	return io.NopCloser(bytes.NewReader(s.initialData)), info, nil
}

func (s *replacingSourceStore) PublicURL(ctx context.Context, key, versionID string) (string, error) {
	if key == s.sourceKey {
		s.stateMu.Lock()
		s.urlVersions = append(s.urlVersions, versionID)
		s.stateMu.Unlock()
	}
	return s.fakeObjectStore.PublicURL(ctx, key, versionID)
}

func (s *replacingSourceStore) sourceVersion(versionID string) (ObjectInfo, error) {
	data := s.initialData
	resolvedVersion := "source-version-1"
	if versionID == "source-version-2" {
		data = s.replacementData
		resolvedVersion = versionID
	} else if versionID != "" && versionID != resolvedVersion {
		return ObjectInfo{}, ErrObjectNotFound
	}
	checksum := sha256.Sum256(data)
	return ObjectInfo{
		Key: s.sourceKey, VersionID: resolvedVersion, ETag: "etag-" + resolvedVersion,
		SizeBytes: int64(len(data)), ChecksumSHA256: hex.EncodeToString(checksum[:]),
	}, nil
}

func (s *replacingSourceStore) calls() ([]string, []string, []string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return append([]string(nil), s.statVersions...), append([]string(nil), s.openVersions...), append([]string(nil), s.urlVersions...)
}

type casRaceRepository struct {
	*fakeRepository
	replacement domain.Artifact
	once        sync.Once
}

func (r *casRaceRepository) MarkFontArtifactMissing(
	ctx context.Context,
	key, objectKey string,
	generation int64,
	reason string,
) (bool, error) {
	lostCAS := false
	r.once.Do(func() {
		r.mu.Lock()
		r.artifacts[key] = r.replacement
		r.mu.Unlock()
		lostCAS = true
	})
	if lostCAS {
		return false, nil
	}
	return r.fakeRepository.MarkFontArtifactMissing(ctx, key, objectKey, generation, reason)
}

func (s *sourceChangingStore) StatObject(ctx context.Context, key, versionID string) (ObjectInfo, error) {
	info, err := s.fakeObjectStore.StatObject(ctx, key, versionID)
	if err == nil && key == s.sourceKey {
		info.ETag = "changed-after-open"
	}
	return info, err
}

type fakeBuilder struct {
	calls atomic.Int32
	delay time.Duration
}

type errorBuilder struct {
	calls atomic.Int32
	err   error
}

type recordingObserver struct {
	mu         sync.Mutex
	cache      map[string]int
	admissions map[string]int
	leases     map[string]int
	builds     map[string]int
	active     int
	queued     int
}

func (o *recordingObserver) ObserveFontCache(result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cache == nil {
		o.cache = make(map[string]int)
	}
	o.cache[result]++
}

func (o *recordingObserver) ObserveFontBuildAdmission(result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.admissions == nil {
		o.admissions = make(map[string]int)
	}
	o.admissions[result]++
}

func (o *recordingObserver) ObserveFontBuildQueue(active, queued int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.active = active
	o.queued = queued
}

func (o *recordingObserver) ObserveFontBuildLease(result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.leases == nil {
		o.leases = make(map[string]int)
	}
	o.leases[result]++
}

func (o *recordingObserver) ObserveFontBuild(kind, outcome string, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.builds == nil {
		o.builds = make(map[string]int)
	}
	o.builds[kind+":"+outcome]++
}

type controlledBuilder struct {
	calls           atomic.Int32
	started         chan struct{}
	canceled        chan struct{}
	finished        chan struct{}
	release         chan struct{}
	holdAfterCancel bool
	startOnce       sync.Once
	cancelOnce      sync.Once
	finishOnce      sync.Once
	releaseOnce     sync.Once
}

func newControlledBuilder(holdAfterCancel bool) *controlledBuilder {
	return &controlledBuilder{
		started:         make(chan struct{}),
		canceled:        make(chan struct{}),
		finished:        make(chan struct{}),
		release:         make(chan struct{}),
		holdAfterCancel: holdAfterCancel,
	}
}

func (b *controlledBuilder) BuildSubset(ctx context.Context, _ BuildInput) (BuildOutput, error) {
	b.calls.Add(1)
	b.startOnce.Do(func() { close(b.started) })
	if b.holdAfterCancel {
		<-ctx.Done()
		b.cancelOnce.Do(func() { close(b.canceled) })
		<-b.release
		b.finishOnce.Do(func() { close(b.finished) })
		return BuildOutput{}, ctx.Err()
	}

	select {
	case <-b.release:
		b.finishOnce.Do(func() { close(b.finished) })
		return BuildOutput{
			Data: []byte("wOF2-data"), ContentType: domain.ContentTypeWOFF2,
			Format: domain.OutputFormatWOFF2, GlyphCount: 2,
		}, nil
	case <-ctx.Done():
		b.cancelOnce.Do(func() { close(b.canceled) })
		b.finishOnce.Do(func() { close(b.finished) })
		return BuildOutput{}, ctx.Err()
	}
}

func (b *controlledBuilder) Release() {
	b.releaseOnce.Do(func() { close(b.release) })
}

func (b *fakeBuilder) BuildSubset(context.Context, BuildInput) (BuildOutput, error) {
	b.calls.Add(1)
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	return BuildOutput{Data: []byte("wOF2-data"), ContentType: domain.ContentTypeWOFF2, Format: domain.OutputFormatWOFF2, GlyphCount: 2}, nil
}

func (b *errorBuilder) BuildSubset(context.Context, BuildInput) (BuildOutput, error) {
	b.calls.Add(1)
	return BuildOutput{}, b.err
}

func dynamicRequest(fontID, words string) GenerateRequest {
	return GenerateRequest{FontID: fontID, Words: words, Min: true, Weight: "400"}
}

func dynamicArtifactKey(repository *fakeRepository, normalizedWords string) string {
	wordHash := domain.DynamicWordHash(repository.family.ID, 400, normalizedWords)
	return domain.DynamicArtifactKey(
		wordHash,
		repository.family.ID,
		400,
		domain.SourceFingerprint(repository.source),
		domain.DefaultBuilderVersion,
	)
}

func generateAsync(service *Service, ctx context.Context, fontID, words string) <-chan error {
	result := make(chan error, 1)
	go func() {
		_, err := service.Generate(ctx, dynamicRequest(fontID, words))
		result <- err
	}()
	return result
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForError(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func waitForCondition(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		}
	}
}
