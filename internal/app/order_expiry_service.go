package app

import (
	"context"
	"fmt"
	"time"
)

type OrderExpiryRepository interface {
	ExpireOverdueOrders(context.Context, time.Time, int32) ([]int64, error)
}

type OrderExpiryService struct {
	repository OrderExpiryRepository
	batchSize  int32
	clock      func() time.Time
}

func NewOrderExpiryService(repository OrderExpiryRepository, batchSize int32) *OrderExpiryService {
	return &OrderExpiryService{repository: repository, batchSize: batchSize, clock: time.Now}
}

func (s *OrderExpiryService) RunOnce(ctx context.Context) (int, error) {
	if s.batchSize <= 0 {
		return 0, ErrInvalidInput
	}
	ids, err := s.repository.ExpireOverdueOrders(ctx, s.clock(), s.batchSize)
	if err != nil {
		return 0, fmt.Errorf("expire overdue orders: %w", err)
	}
	return len(ids), nil
}
