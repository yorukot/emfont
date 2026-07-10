package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type txContextKey struct{}

// TxFunc is executed inside a database transaction.
type TxFunc func(ctx context.Context, tx pgx.Tx) error

// Transactor centralizes transaction boundaries for repository orchestration.
type Transactor struct {
	pool *pgxpool.Pool
}

func NewTransactor(pool *pgxpool.Pool) *Transactor {
	return &Transactor{pool: pool}
}

// WithinTx executes fn in a transaction, reusing an existing tx from ctx when present.
func (t *Transactor) WithinTx(ctx context.Context, opts pgx.TxOptions, fn TxFunc) error {
	if fn == nil {
		return errors.New("transaction function is nil")
	}
	if tx, ok := TxFromContext(ctx); ok {
		return withinNestedTx(ctx, tx, fn)
	}
	if t == nil || t.pool == nil {
		return ErrNilPool
	}

	tx, err := t.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	return finishTx(ctx, tx, fn)
}

// ContextWithTx returns a child context that carries tx for nested repository work.
func ContextWithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txContextKey{}, tx)
}

// TxFromContext returns a transaction previously attached with ContextWithTx.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txContextKey{}).(pgx.Tx)
	return tx, ok
}

func withinNestedTx(ctx context.Context, parent pgx.Tx, fn TxFunc) error {
	tx, err := parent.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin nested transaction: %w", err)
	}
	return finishTx(ctx, tx, fn)
}

func finishTx(ctx context.Context, tx pgx.Tx, fn TxFunc) error {
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := fn(ContextWithTx(ctx, tx), tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true

	return nil
}
