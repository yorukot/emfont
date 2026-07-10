package system

import (
	"context"
	"fmt"
)

type Service struct {
	store  Store
	tracer Tracer
}

type Option func(*Service)

func NewService(store Store, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: store is required", ErrInvalidInput)
	}

	service := &Service{
		store:  store,
		tracer: noopTracer{},
	}
	for _, opt := range opts {
		opt(service)
	}
	return service, nil
}

func WithTracer(tracer Tracer) Option {
	return func(service *Service) {
		if tracer != nil {
			service.tracer = tracer
		}
	}
}

func (s *Service) Get(ctx context.Context, req GetRequest) (dto SystemDTO, err error) {
	ctx, span := s.tracer.Start(normalizeContext(ctx), "application.system.get", TraceAttribute{
		Key:   "system.id",
		Value: req.ID,
	})
	defer func() {
		span.End(err)
	}()

	id, err := validateGetRequest(req)
	if err != nil {
		return SystemDTO{}, err
	}

	system, err := s.store.GetSystem(ctx, id)
	if err != nil {
		return SystemDTO{}, storeError("get", err)
	}
	return toDTO(system), nil
}

func (s *Service) Upsert(ctx context.Context, req UpsertRequest) (dto SystemDTO, err error) {
	ctx, span := s.tracer.Start(normalizeContext(ctx), "application.system.upsert", TraceAttribute{
		Key:   "system.id",
		Value: req.ID,
	})
	defer func() {
		span.End(err)
	}()

	system, err := validateUpsertRequest(req)
	if err != nil {
		return SystemDTO{}, err
	}

	if err := s.store.UpsertSystem(ctx, system); err != nil {
		return SystemDTO{}, storeError("upsert", err)
	}
	return toDTO(system), nil
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
