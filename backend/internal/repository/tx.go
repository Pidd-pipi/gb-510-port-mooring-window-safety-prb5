package repository

import (
	"context"

	"gorm.io/gorm"
)

// txKey carries the active transaction through context so repositories can join
// a service-level unit of work without each method accepting a *gorm.Tx.
type txKey struct{}

// TxManager runs a function inside one database transaction. The clearance
// interlock needs the clearance update and its audit entry to commit or roll
// back together, while still re-reading the bound plan/window under the same
// transaction so concurrent aggregate changes cannot slip in between.
type TxManager interface {
	WithinTx(context.Context, func(context.Context) error) error
}

type txManager struct{ db *gorm.DB }

func NewTxManager(db *gorm.DB) TxManager { return &txManager{db: db} }

func (m *txManager) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := txFromContext(ctx); ok {
		return fn(ctx)
	}
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

func txFromContext(ctx context.Context) (*gorm.DB, bool) {
	tx, ok := ctx.Value(txKey{}).(*gorm.DB)
	return tx, ok
}

// conn returns the transactional connection when one is present, otherwise the
// plain connection.
func conn(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx, ok := txFromContext(ctx); ok {
		return tx.WithContext(ctx)
	}
	return db.WithContext(ctx)
}
