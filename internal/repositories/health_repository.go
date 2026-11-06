package repositories

import "context"

type DatabasePinger interface {
	Ping(context.Context) error
}

type HealthRepository struct {
	db DatabasePinger
}

func NewHealthRepository(db DatabasePinger) *HealthRepository {
	return &HealthRepository{db: db}
}

func (h *HealthRepository) Check(ctx context.Context) error {
	return h.db.Ping(ctx)
}
