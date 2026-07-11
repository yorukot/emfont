package fontcleanup

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestServiceRunCleansInBoundedPhases(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	deleteFailure := errors.New("delete unavailable")
	repository := &fakeRepository{
		retireResults: []batchResult{{rows: 2}, {rows: 1}},
		deleteResults: []batchResult{{rows: 2}, {rows: 2}, {rows: 1}},
		referenced: map[string]struct{}{
			"_generated/a.woff2": {},
		},
	}
	objects := &fakeObjectStore{
		pages: []ObjectPage{
			{
				Objects: []Object{
					{Key: "_generated/a.woff2", LastModified: now.Add(-2 * time.Hour)},
					{Key: "_generated/b.woff2", LastModified: now.Add(-2 * time.Hour)},
					{Key: "_generated/c.woff2", LastModified: now.Add(-30 * time.Minute)},
				},
				NextCursor: "_generated/c.woff2",
				HasMore:    true,
			},
			{
				Objects: []Object{
					{Key: "_generated/d.woff2"},
					{Key: "_generated/e.woff2", LastModified: now.Add(-3 * time.Hour)},
					{Key: "_generated/f.woff2", LastModified: now.Add(-4 * time.Hour)},
				},
			},
		},
		deleteErrors: map[string]error{
			"_generated/b.woff2": deleteFailure,
			"_generated/f.woff2": ErrObjectNotFound,
		},
	}
	service, err := NewService(repository, objects, Config{
		ArtifactRetention: 30 * 24 * time.Hour,
		RetirementGrace:   2 * time.Hour,
		OrphanGrace:       time.Hour,
		DatabaseBatchSize: 2,
		MaxDatabaseRows:   5,
		ObjectPageSize:    3,
		MaxObjectPages:    3,
	}, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	report, err := service.Run(context.Background())
	if !errors.Is(err, deleteFailure) {
		t.Fatalf("Run error = %v, want delete failure", err)
	}
	if report.StartedAt != now || report.CompletedAt != now {
		t.Fatalf("report timestamps = %s, %s", report.StartedAt, report.CompletedAt)
	}
	if report.ArtifactsRetired != 3 || report.ArtifactRowsDeleted != 5 {
		t.Fatalf("database report = %#v", report)
	}
	if report.RetirementLimitReached || !report.RowDeletionLimitReached {
		t.Fatalf("database limits = retirement %t, deletion %t", report.RetirementLimitReached, report.RowDeletionLimitReached)
	}
	if report.ObjectPagesScanned != 2 || report.ObjectsScanned != 6 || report.ObjectPageLimitReached {
		t.Fatalf("object scan report = %#v", report)
	}
	if report.ObjectsReferenced != 1 || report.ObjectsTooYoung != 1 || report.ObjectsWithUnknownAge != 1 {
		t.Fatalf("object protections = %#v", report)
	}
	if report.ObjectsDeleted != 1 || report.ObjectsAlreadyMissing != 1 || report.ObjectDeletionFailures != 1 {
		t.Fatalf("object deletion report = %#v", report)
	}

	if len(repository.retireCalls) != 2 {
		t.Fatalf("retirement calls = %#v", repository.retireCalls)
	}
	if got, want := repository.retireCalls[0].cutoff, now.Add(-30*24*time.Hour); !got.Equal(want) {
		t.Fatalf("retirement cutoff = %s, want %s", got, want)
	}
	if got := repository.retireCalls[0].at; !got.Equal(now) {
		t.Fatalf("retirement timestamp = %s, want %s", got, now)
	}
	if len(repository.deleteCalls) != 3 || !repository.deleteCalls[0].cutoff.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("row deletion calls = %#v", repository.deleteCalls)
	}
	wantReferenceBatches := [][]string{
		{"_generated/a.woff2", "_generated/b.woff2"},
		{"_generated/e.woff2", "_generated/f.woff2"},
	}
	if !reflect.DeepEqual(repository.referenceCalls, wantReferenceBatches) {
		t.Fatalf("reference batches = %#v, want %#v", repository.referenceCalls, wantReferenceBatches)
	}
	if want := []string{"_generated/b.woff2", "_generated/e.woff2", "_generated/f.woff2"}; !reflect.DeepEqual(objects.deleted, want) {
		t.Fatalf("deleted keys = %#v, want %#v", objects.deleted, want)
	}
	if len(objects.listCalls) != 2 || objects.listCalls[0].cursor != "" || objects.listCalls[1].cursor != "_generated/c.woff2" {
		t.Fatalf("list calls = %#v", objects.listCalls)
	}
}

func TestServiceRunStopsOnReferenceFailureWithoutDeletingPage(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	referenceFailure := errors.New("database unavailable")
	repository := &fakeRepository{referenceErr: referenceFailure}
	objects := &fakeObjectStore{pages: []ObjectPage{{Objects: []Object{
		{Key: "_generated/old.woff2", LastModified: now.Add(-2 * time.Hour)},
	}}}}
	service := newTestService(t, repository, objects, now, Config{})

	report, err := service.Run(context.Background())
	if !errors.Is(err, referenceFailure) {
		t.Fatalf("Run error = %v, want reference failure", err)
	}
	if len(objects.deleted) != 0 || report.ObjectsDeleted != 0 {
		t.Fatalf("objects deleted after reference failure: %#v", objects.deleted)
	}
}

func TestServiceRunSkipsObjectThatChangedAfterListing(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{}
	objects := &fakeObjectStore{
		pages: []ObjectPage{{Objects: []Object{{
			Key: "_generated/rebuilt.woff2", ETag: "old", SizeBytes: 4, LastModified: now.Add(-2 * time.Hour),
		}}}},
		deleteErrors: map[string]error{"_generated/rebuilt.woff2": ErrObjectChanged},
	}
	service := newTestService(t, repository, objects, now, Config{})

	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.ObjectsChanged != 1 || report.ObjectsDeleted != 0 || report.ObjectDeletionFailures != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestServiceRunContinuesIndependentPhasesAfterDatabaseFailures(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	retirementFailure := errors.New("retirement unavailable")
	rowDeletionFailure := errors.New("row deletion unavailable")
	repository := &fakeRepository{
		retireResults: []batchResult{{err: retirementFailure}},
		deleteResults: []batchResult{{err: rowDeletionFailure}},
	}
	objects := &fakeObjectStore{pages: []ObjectPage{{Objects: []Object{
		{Key: "_generated/orphan.woff2", LastModified: now.Add(-2 * time.Hour)},
	}}}}
	service := newTestService(t, repository, objects, now, Config{})

	report, err := service.Run(context.Background())
	if !errors.Is(err, retirementFailure) || !errors.Is(err, rowDeletionFailure) {
		t.Fatalf("Run error = %v, want both database failures", err)
	}
	if report.ObjectsDeleted != 1 {
		t.Fatalf("report = %#v, want one safely deleted orphan", report)
	}
	if want := []string{"_generated/orphan.woff2"}; !reflect.DeepEqual(objects.deleted, want) {
		t.Fatalf("deleted keys = %#v, want %#v", objects.deleted, want)
	}
}

func TestServiceRunHonorsCancellationDuringObjectDeletion(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	repository := &fakeRepository{}
	objects := &fakeObjectStore{pages: []ObjectPage{{Objects: []Object{
		{Key: "_generated/a.woff2", LastModified: now.Add(-2 * time.Hour)},
		{Key: "_generated/b.woff2", LastModified: now.Add(-2 * time.Hour)},
	}}}}
	objects.deleteFunc = func(string) error {
		cancel()
		return context.Canceled
	}
	service := newTestService(t, repository, objects, now, Config{})

	report, err := service.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	if want := []string{"_generated/a.woff2"}; !reflect.DeepEqual(objects.deleted, want) {
		t.Fatalf("delete attempts = %#v, want %#v", objects.deleted, want)
	}
	if report.ObjectDeletionFailures != 0 || report.ObjectsDeleted != 0 {
		t.Fatalf("canceled deletion report = %#v", report)
	}
}

func TestServiceRunReportsObjectPageLimit(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{referenced: map[string]struct{}{"_generated/a.woff2": {}}}
	objects := &fakeObjectStore{pages: []ObjectPage{{
		Objects:    []Object{{Key: "_generated/a.woff2", LastModified: now.Add(-2 * time.Hour)}},
		NextCursor: "_generated/a.woff2",
		HasMore:    true,
	}}}
	service := newTestService(t, repository, objects, now, Config{MaxObjectPages: 1})

	report, err := service.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.ObjectPageLimitReached || report.ObjectPagesScanned != 1 {
		t.Fatalf("page limit report = %#v", report)
	}
}

func TestServiceRejectsMalformedReferenceResult(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{referenceResult: []string{"_generated/not-requested.woff2"}}
	objects := &fakeObjectStore{pages: []ObjectPage{{Objects: []Object{
		{Key: "_generated/old.woff2", LastModified: now.Add(-2 * time.Hour)},
	}}}}
	service := newTestService(t, repository, objects, now, Config{})

	_, err := service.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected object reference") {
		t.Fatalf("Run error = %v, want malformed reference error", err)
	}
	if len(objects.deleted) != 0 {
		t.Fatalf("deleted keys = %#v", objects.deleted)
	}
}

func TestNewServiceValidatesConfigurationAndAppliesBounds(t *testing.T) {
	repository := &fakeRepository{}
	objects := &fakeObjectStore{}
	valid := Config{ArtifactRetention: time.Hour, RetirementGrace: time.Hour, OrphanGrace: time.Hour}
	service, err := NewService(repository, objects, valid)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if service.config.DatabaseBatchSize != DefaultDatabaseBatchSize ||
		service.config.MaxDatabaseRows != DefaultMaxDatabaseRows ||
		service.config.ObjectPageSize != DefaultObjectPageSize ||
		service.config.MaxObjectPages != DefaultMaxObjectPages {
		t.Fatalf("default config = %#v", service.config)
	}

	tests := []struct {
		name   string
		config Config
	}{
		{name: "artifact retention", config: Config{RetirementGrace: time.Hour, OrphanGrace: time.Hour}},
		{name: "retirement grace", config: Config{ArtifactRetention: time.Hour, OrphanGrace: time.Hour}},
		{name: "orphan grace", config: Config{ArtifactRetention: time.Hour, RetirementGrace: time.Hour}},
		{name: "unsafe orphan grace", config: Config{ArtifactRetention: time.Hour, RetirementGrace: time.Hour, OrphanGrace: time.Second}},
		{name: "negative bound", config: Config{ArtifactRetention: time.Hour, RetirementGrace: time.Hour, OrphanGrace: time.Hour, MaxObjectPages: -1}},
		{name: "oversized page", config: Config{ArtifactRetention: time.Hour, RetirementGrace: time.Hour, OrphanGrace: time.Hour, ObjectPageSize: 1_001}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewService(repository, objects, test.config)
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("NewService error = %v, want ErrInvalidConfiguration", err)
			}
		})
	}
}

func newTestService(t *testing.T, repository Repository, objects ObjectStore, now time.Time, overrides Config) *Service {
	t.Helper()
	config := Config{
		ArtifactRetention: time.Hour,
		RetirementGrace:   time.Hour,
		OrphanGrace:       time.Hour,
		DatabaseBatchSize: 10,
		MaxDatabaseRows:   10,
		ObjectPageSize:    10,
		MaxObjectPages:    10,
	}
	if overrides.MaxObjectPages != 0 {
		config.MaxObjectPages = overrides.MaxObjectPages
	}
	service, err := NewService(repository, objects, config, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

type batchResult struct {
	rows int64
	err  error
}

type retirementCall struct {
	cutoff    time.Time
	at        time.Time
	batchSize int32
}

type deletionCall struct {
	cutoff    time.Time
	batchSize int32
}

type fakeRepository struct {
	retireResults   []batchResult
	deleteResults   []batchResult
	referenced      map[string]struct{}
	referenceResult []string
	referenceErr    error

	retireCalls    []retirementCall
	deleteCalls    []deletionCall
	referenceCalls [][]string
}

func (f *fakeRepository) RetireFontArtifacts(_ context.Context, cutoff, at time.Time, batchSize int32) (int64, error) {
	f.retireCalls = append(f.retireCalls, retirementCall{cutoff: cutoff, at: at, batchSize: batchSize})
	if len(f.retireResults) == 0 {
		return 0, nil
	}
	result := f.retireResults[0]
	f.retireResults = f.retireResults[1:]
	return result.rows, result.err
}

func (f *fakeRepository) DeleteRetiredFontArtifacts(_ context.Context, cutoff time.Time, batchSize int32) (int64, error) {
	f.deleteCalls = append(f.deleteCalls, deletionCall{cutoff: cutoff, batchSize: batchSize})
	if len(f.deleteResults) == 0 {
		return 0, nil
	}
	result := f.deleteResults[0]
	f.deleteResults = f.deleteResults[1:]
	return result.rows, result.err
}

func (f *fakeRepository) FindReferencedFontObjectKeys(_ context.Context, keys []string) ([]string, error) {
	f.referenceCalls = append(f.referenceCalls, append([]string(nil), keys...))
	if f.referenceErr != nil {
		return nil, f.referenceErr
	}
	if f.referenceResult != nil {
		return append([]string(nil), f.referenceResult...), nil
	}
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := f.referenced[key]; ok {
			result = append(result, key)
		}
	}
	return result, nil
}

type listCall struct {
	prefix string
	cursor string
	limit  int
}

type fakeObjectStore struct {
	pages        []ObjectPage
	listErr      error
	deleteErrors map[string]error
	deleteFunc   func(string) error

	listCalls []listCall
	deleted   []string
}

func (f *fakeObjectStore) ListObjects(_ context.Context, prefix, cursor string, limit int) (ObjectPage, error) {
	f.listCalls = append(f.listCalls, listCall{prefix: prefix, cursor: cursor, limit: limit})
	if f.listErr != nil {
		return ObjectPage{}, f.listErr
	}
	if len(f.pages) == 0 {
		return ObjectPage{}, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	page.Objects = append([]Object(nil), page.Objects...)
	return page, nil
}

func (f *fakeObjectStore) DeleteObject(_ context.Context, object Object) error {
	key := object.Key
	f.deleted = append(f.deleted, key)
	if f.deleteFunc != nil {
		return f.deleteFunc(key)
	}
	return f.deleteErrors[key]
}
