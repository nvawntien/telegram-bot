package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

func TestInventoryReservationServiceValidatesClaimWindow(t *testing.T) {
	now := time.Now()
	repository := &inventoryReservationRepositoryStub{}
	service := NewInventoryReservationService(repository, 30*time.Minute, nil)
	service.clock = func() time.Time { return now }
	base := InventoryClaimRequest{
		OrderID: 1, OrderItemID: 2, ProductID: 3, Quantity: 1,
		ReservedUntil: now.Add(10 * time.Minute),
	}
	if _, err := service.Claim(context.Background(), base); err != nil {
		t.Fatalf("valid Claim() error = %v", err)
	}
	for _, mutate := range []func(*InventoryClaimRequest){
		func(request *InventoryClaimRequest) { request.Quantity = 0 },
		func(request *InventoryClaimRequest) { request.ReservedUntil = now },
		func(request *InventoryClaimRequest) { request.ReservedUntil = now.Add(time.Hour) },
	} {
		request := base
		mutate(&request)
		if _, err := service.Claim(context.Background(), request); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid Claim() error = %v", err)
		}
	}
}

func TestInventoryReservationServiceValidatesReleaseReason(t *testing.T) {
	service := NewInventoryReservationService(&inventoryReservationRepositoryStub{}, time.Hour, nil)
	if _, err := service.Release(context.Background(), InventoryReleaseRequest{
		OrderID: 1, Reason: domain.InventoryReleaseReason("unknown"),
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid Release() error = %v", err)
	}
}

type inventoryReservationRepositoryStub struct{}

func (*inventoryReservationRepositoryStub) ClaimInventory(_ context.Context, request InventoryClaimRequest) (InventoryClaimResult, error) {
	return InventoryClaimResult{OrderID: request.OrderID, Count: int(request.Quantity)}, nil
}

func (*inventoryReservationRepositoryStub) ReleaseInventory(_ context.Context, request InventoryReleaseRequest) (InventoryReleaseResult, error) {
	return InventoryReleaseResult{OrderID: request.OrderID, Reason: request.Reason}, nil
}

func (*inventoryReservationRepositoryStub) RecoverExpiredReservation(_ context.Context, orderID int64, _ time.Time, _ string) (InventoryRecoveryResult, error) {
	return InventoryRecoveryResult{OrderID: orderID}, nil
}
