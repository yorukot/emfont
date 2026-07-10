package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type traceContextKey struct{}

type queryTraceStart struct {
	sql       string
	args      []any
	startedAt time.Time
}

// QueryTraceEvent is emitted when pgx finishes a query.
type QueryTraceEvent struct {
	SQL        string
	Args       []any
	CommandTag pgconn.CommandTag
	Err        error
	Duration   time.Duration
}

// QueryObserver receives completed pgx query events.
type QueryObserver interface {
	ObserveQuery(ctx context.Context, event QueryTraceEvent)
}

// QueryObserverFunc adapts a function to QueryObserver.
type QueryObserverFunc func(ctx context.Context, event QueryTraceEvent)

func (fn QueryObserverFunc) ObserveQuery(ctx context.Context, event QueryTraceEvent) {
	fn(ctx, event)
}

// QueryTracer is a small pgx tracer that forwards completed query events.
type QueryTracer struct {
	Observer QueryObserver
	Now      func() time.Time
}

func NewQueryTracer(observer QueryObserver) QueryTracer {
	return QueryTracer{
		Observer: observer,
		Now:      time.Now,
	}
}

func (t QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	now := t.Now
	if now == nil {
		now = time.Now
	}

	return context.WithValue(ctx, traceContextKey{}, queryTraceStart{
		sql:       data.SQL,
		args:      data.Args,
		startedAt: now(),
	})
}

func (t QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if t.Observer == nil {
		return
	}

	now := t.Now
	if now == nil {
		now = time.Now
	}

	event := QueryTraceEvent{
		CommandTag: data.CommandTag,
		Err:        data.Err,
	}
	if started, ok := ctx.Value(traceContextKey{}).(queryTraceStart); ok {
		event.SQL = started.sql
		event.Args = started.args
		event.Duration = now().Sub(started.startedAt)
	}

	t.Observer.ObserveQuery(ctx, event)
}
