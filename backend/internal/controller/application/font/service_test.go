package font

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/emfont/emfont/backend/internal/domain/font"
)

func TestGenerateUsesReadyArtifactWithoutRebuilding(t *testing.T) {
	repository := newFakeRepository()
	objects := newFakeObjectStore()
	builder := &fakeBuilder{}
	source := repository.source
	hash := domain.DynamicWordHash(repository.family.ID, 400, "AB")
	key := domain.DynamicArtifactKey(hash, repository.family.ID, 400, domain.SourceFingerprint(source), domain.DefaultBuilderVersion)
	objectKey := domain.DynamicObjectKey(hash, repository.family.ID, 400, domain.BuildRevision(domain.SourceFingerprint(source), domain.DefaultBuilderVersion))
	repository.artifacts[key] = domain.Artifact{
		Key: key, Status: "ready", ObjectKey: objectKey, SizeBytes: 4,
	}
	objects.objects[objectKey] = []byte("wOF2")

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
	if len(response.Location) != 1 || response.Location[0] != "https://fonts.example/"+objectKey {
		t.Fatalf("locations = %#v", response.Location)
	}
	if builder.calls.Load() != 0 {
		t.Fatalf("builder calls = %d, want 0", builder.calls.Load())
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

type fakeRepository struct {
	mu           sync.Mutex
	family       domain.Family
	source       domain.Source
	artifacts    map[string]domain.Artifact
	leased       map[string]bool
	acquireCalls atomic.Int32
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		family: domain.Family{ID: "DemoFont", Name: "Demo Font", Weights: []int{400}, Format: "ttf", Version: "1"},
		source: domain.Source{
			FamilyID: "DemoFont", Weight: 400, Format: "ttf", ObjectKey: "original-fonts/DemoFont/400.ttf",
			ChecksumSHA256: "5d35da932a1537eefd161a24f03bbb1148b488fedc5cdd6325cb3859c9c66467",
		},
		artifacts: make(map[string]domain.Artifact), leased: make(map[string]bool),
	}
}

func (r *fakeRepository) GetFontFamily(context.Context, string) (domain.Family, error) {
	return r.family, nil
}
func (r *fakeRepository) ListFontFamilies(context.Context, string) ([]domain.Family, error) {
	return []domain.Family{r.family}, nil
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
func (r *fakeRepository) CreateFontArtifact(_ context.Context, artifact domain.Artifact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.artifacts[artifact.Key]; !ok {
		r.artifacts[artifact.Key] = artifact
	}
	return nil
}
func (r *fakeRepository) MarkFontArtifactReady(_ context.Context, key, _ string, object domain.ArtifactObject) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	artifact := r.artifacts[key]
	artifact.Status = "ready"
	artifact.SizeBytes = object.SizeBytes
	artifact.ETag = object.ETag
	r.artifacts[key] = artifact
	return nil
}
func (r *fakeRepository) MarkFontArtifactMissing(context.Context, string, string) error { return nil }
func (r *fakeRepository) MarkFontArtifactFailed(context.Context, string, string, string) error {
	return nil
}
func (r *fakeRepository) CurrentStaticVersion(context.Context) (int, error) { return 100, nil }
func (r *fakeRepository) FindStaticPacks(context.Context, string, []string) ([]int, error) {
	return []int{1}, nil
}
func (r *fakeRepository) GetStaticPackCharacters(context.Context, string, int) (string, error) {
	return "AB", nil
}
func (r *fakeRepository) AcquireBuildJob(_ context.Context, key, _ string, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leased[key] {
		return false, nil
	}
	r.leased[key] = true
	r.acquireCalls.Add(1)
	artifact := r.artifacts[key]
	artifact.Status = "running"
	r.artifacts[key] = artifact
	return true, nil
}
func (r *fakeRepository) CompleteBuildJob(context.Context, string, string) error     { return nil }
func (r *fakeRepository) FailBuildJob(context.Context, string, string, string) error { return nil }

type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeObjectStore() *fakeObjectStore { return &fakeObjectStore{objects: make(map[string][]byte)} }
func (s *fakeObjectStore) StatObject(_ context.Context, key string) (ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return ObjectInfo{}, ErrObjectNotFound
	}
	return ObjectInfo{Key: key, SizeBytes: int64(len(data))}, nil
}
func (s *fakeObjectStore) OpenObject(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	info, err := s.StatObject(ctx, key)
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
	s.mu.Unlock()
	return ObjectInfo{Key: key, SizeBytes: int64(len(data)), ETag: "etag", ContentType: options.ContentType, ChecksumSHA256: options.ChecksumSHA256}, nil
}
func (s *fakeObjectStore) PublicURL(_ context.Context, key string) (string, error) {
	return "https://fonts.example/" + key, nil
}

type fakeBuilder struct {
	calls atomic.Int32
	delay time.Duration
}

func (b *fakeBuilder) BuildSubset(context.Context, BuildInput) (BuildOutput, error) {
	b.calls.Add(1)
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	return BuildOutput{Data: []byte("wOF2-data"), ContentType: domain.ContentTypeWOFF2, Format: domain.OutputFormatWOFF2, GlyphCount: 2}, nil
}
