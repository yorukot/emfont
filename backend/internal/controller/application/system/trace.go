package system

import "context"

type TraceAttribute struct {
	Key   string
	Value any
}

type Tracer interface {
	Start(context.Context, string, ...TraceAttribute) (context.Context, Span)
}

type Span interface {
	End(error)
}

type noopTracer struct{}

type noopSpan struct{}

func (noopTracer) Start(ctx context.Context, _ string, _ ...TraceAttribute) (context.Context, Span) {
	return normalizeContext(ctx), noopSpan{}
}

func (noopSpan) End(error) {}
