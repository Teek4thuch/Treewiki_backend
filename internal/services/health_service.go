package services

import (
	"context"
	"time"
)

// HealthRepository is the storage capability the service needs.
type HealthRepository interface {
	Check(context.Context) error
}

type HealthService struct {
	repository HealthRepository
}

func NewHealthService(repository HealthRepository) *HealthService {
	return &HealthService{repository: repository}
}

func (h *HealthService) CheckReadiness(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return h.repository.Check(ctx)
}
