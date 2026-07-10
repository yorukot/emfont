package system

import (
	"context"
	"errors"
	"testing"

	domain "github.com/emfont/emfont/backend/internal/domain/system"
)

func TestServiceUpsertNormalizesAndStoresSystem(t *testing.T) {
	store := newMemoryStore()
	tracer := &recordingTracer{}
	service, err := NewService(store, WithTracer(tracer))
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	got, err := service.Upsert(context.Background(), UpsertRequest{
		ID:          " Controller ",
		Name:        "  Emfont   Controller ",
		Environment: " Production ",
		Version:     " 1.0.0 ",
		Status:      "Degraded",
	})
	if err != nil {
		t.Fatalf("Upsert returned error: %v", err)
	}

	if got.ID != "controller" {
		t.Fatalf("ID = %q", got.ID)
	}
	if got.Name != "Emfont Controller" {
		t.Fatalf("Name = %q", got.Name)
	}
	if got.Environment != "production" {
		t.Fatalf("Environment = %q", got.Environment)
	}
	if got.Status != "degraded" {
		t.Fatalf("Status = %q", got.Status)
	}
	if _, ok := store.systems["controller"]; !ok {
		t.Fatalf("stored systems = %#v, want controller key", store.systems)
	}
	if tracer.lastSpanName != "application.system.upsert" {
		t.Fatalf("last span = %q", tracer.lastSpanName)
	}
	if tracer.lastErr != nil {
		t.Fatalf("trace end error = %v", tracer.lastErr)
	}
}

func TestServiceGetReturnsStoredSystem(t *testing.T) {
	store := newMemoryStore()
	system, err := domain.VN(domain.VNInput{
		ID:          "controller",
		Name:        "Emfont Controller",
		Environment: "production",
		Version:     "1.0.0",
	})
	if err != nil {
		t.Fatalf("domain.VN returned error: %v", err)
	}
	store.systems[system.ID()] = system

	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	got, err := service.Get(context.Background(), GetRequest{ID: " Controller "})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID != "controller" {
		t.Fatalf("ID = %q", got.ID)
	}
	if got.Status != "ready" {
		t.Fatalf("Status = %q", got.Status)
	}
}

func TestServiceGetPropagatesNotFound(t *testing.T) {
	service, err := NewService(newMemoryStore())
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	_, err = service.Get(context.Background(), GetRequest{ID: "missing"})
	if err == nil {
		t.Fatal("Get returned nil error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestServiceUpsertRejectsInvalidInput(t *testing.T) {
	tracer := &recordingTracer{}
	service, err := NewService(newMemoryStore(), WithTracer(tracer))
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	_, err = service.Upsert(context.Background(), UpsertRequest{
		ID:          "bad id",
		Environment: "production",
	})
	if err == nil {
		t.Fatal("Upsert returned nil error")
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if tracer.lastErr == nil {
		t.Fatal("trace end error is nil")
	}
}

func TestNewServiceRequiresStore(t *testing.T) {
	_, err := NewService(nil)
	if err == nil {
		t.Fatal("NewService returned nil error")
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}

type memoryStore struct {
	systems map[domain.ID]domain.System
}

func newMemoryStore() *memoryStore {
	return &memoryStore{systems: make(map[domain.ID]domain.System)}
}

func (s *memoryStore) GetSystem(_ context.Context, id domain.ID) (domain.System, error) {
	system, ok := s.systems[id]
	if !ok {
		return domain.System{}, ErrNotFound
	}
	return system, nil
}

func (s *memoryStore) UpsertSystem(_ context.Context, system domain.System) error {
	s.systems[system.ID()] = system
	return nil
}

type recordingTracer struct {
	lastSpanName string
	lastErr      error
}

func (t *recordingTracer) Start(ctx context.Context, name string, _ ...TraceAttribute) (context.Context, Span) {
	t.lastSpanName = name
	return ctx, recordingSpan{tracer: t}
}

type recordingSpan struct {
	tracer *recordingTracer
}

func (s recordingSpan) End(err error) {
	s.tracer.lastErr = err
}
