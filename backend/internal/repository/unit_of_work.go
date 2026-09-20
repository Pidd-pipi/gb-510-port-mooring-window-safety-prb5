package repository

import (
	"context"

	"gorm.io/gorm"
)

// TxRepositories groups transaction-scoped repositories so multi-entity
// writes commit or roll back together.
type TxRepositories struct {
	SafetyClearance SafetyClearanceRepository
	MooringPlan     MooringPlanRepository
	WeatherWindow   WeatherWindowRepository
	Security        SecurityRepository
}

// UnitOfWork runs a set of repository operations inside one database
// transaction. Callers receive transaction-bound repositories and must not
// touch the outer repositories until the transaction commits.
type UnitOfWork interface {
	WithinTransaction(context.Context, func(TxRepositories) error) error
}

type unitOfWork struct{ db *gorm.DB }

func NewUnitOfWork(db *gorm.DB) UnitOfWork { return &unitOfWork{db: db} }

func (u *unitOfWork) WithinTransaction(ctx context.Context, fn func(TxRepositories) error) error {
	return u.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(TxRepositories{
			SafetyClearance: NewSafetyClearanceRepository(tx),
			MooringPlan:     NewMooringPlanRepository(tx),
			WeatherWindow:   NewWeatherWindowRepository(tx),
			Security:        NewSecurityRepository(tx),
		})
	})
}
