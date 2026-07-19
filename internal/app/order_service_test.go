package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

func TestOrderServiceRetriesPaymentReferenceCollision(t *testing.T) {
	tx := validOrderTransaction()
	tx.collisions = 2
	references := &sequenceReferences{}
	service := testOrderService(&singleTransactionRepository{tx: tx}, references)
	result, err := service.Create(context.Background(), validCreateCommand())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if tx.insertCalls != 3 || references.calls != 3 || result.Order.PaymentReference != "TS000003" {
		t.Fatalf("collision retry = inserts:%d references:%d result:%#v", tx.insertCalls, references.calls, result)
	}
}

func TestOrderServiceStopsPaymentReferenceCollisionRetry(t *testing.T) {
	tx := validOrderTransaction()
	tx.collisions = paymentReferenceAttempts
	references := &sequenceReferences{}
	service := testOrderService(&singleTransactionRepository{tx: tx}, references)
	_, err := service.Create(context.Background(), validCreateCommand())
	if !errors.Is(err, ErrPaymentReferenceCollision) {
		t.Fatalf("Create() error = %v", err)
	}
	if tx.insertCalls != paymentReferenceAttempts || references.calls != paymentReferenceAttempts {
		t.Fatalf("retry limit = inserts:%d references:%d", tx.insertCalls, references.calls)
	}
}

func TestOrderServiceDuplicateReturnsStableReferenceWithoutGeneratingAnother(t *testing.T) {
	tx := validOrderTransaction()
	tx.existing = OrderDetail{
		ID: 55, Status: domain.OrderStatusPendingPayment, Total: 10_000,
		PaymentReference: "TSSTABLE01", ExpiresAt: time.Now().Add(time.Minute),
		BankBIN: "970422", BankName: "Test", BankDisplayName: "Primary",
		BankAccountName: "OWNER", BankProtected: ProtectedBankAccountNumber{Format: "test"},
	}
	references := &sequenceReferences{}
	service := testOrderService(&singleTransactionRepository{tx: tx}, references)
	result, err := service.Create(context.Background(), validCreateCommand())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Duplicate || result.Order.PaymentReference != "TSSTABLE01" || references.calls != 0 || tx.historyCalls != 0 {
		t.Fatalf("duplicate result = %#v, references:%d history:%d", result, references.calls, tx.historyCalls)
	}
}

func TestOrderServiceValidatesQuantityBeforeTransaction(t *testing.T) {
	repository := &singleTransactionRepository{tx: validOrderTransaction()}
	service := testOrderService(repository, &sequenceReferences{})
	command := validCreateCommand()
	command.Quantity = 0
	if _, err := service.Create(context.Background(), command); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("zero quantity error = %v", err)
	}
	command.Quantity = 11
	if _, err := service.Create(context.Background(), command); !errors.Is(err, ErrQuantityLimitExceeded) {
		t.Fatalf("over-limit quantity error = %v", err)
	}
	if repository.calls != 0 {
		t.Fatalf("invalid quantity opened %d transactions", repository.calls)
	}
}

func validCreateCommand() CreateOrderCommand {
	return CreateOrderCommand{
		TelegramUserID: 100, ProductID: 20, BankAccountID: 30, Quantity: 1,
		IdempotencyKey: "operation-1", Meta: RequestMeta{RequestID: "request-1", UpdateID: 40},
	}
}

func testOrderService(repository OrderRepository, references PaymentReferenceGenerator) *OrderService {
	service := NewOrderService(repository, testBankCipher{}, testInstructionGenerator{}, references, 15*time.Minute, 10, 8)
	service.clock = func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	return service
}

type singleTransactionRepository struct {
	tx    *testOrderTransaction
	calls int
}

func (r *singleTransactionRepository) WithinOrderTransaction(ctx context.Context, fn func(OrderTransaction) error) error {
	r.calls++
	return fn(r.tx)
}

func (*singleTransactionRepository) ListOrdersOwnedByTelegramUser(context.Context, int64, int32, int32) ([]OrderSummary, int64, error) {
	return nil, 0, nil
}

func (*singleTransactionRepository) GetOrderOwnedByTelegramUser(context.Context, int64, int64) (OrderDetail, error) {
	return OrderDetail{}, ErrOrderNotFound
}

type testOrderTransaction struct {
	existing     OrderDetail
	collisions   int
	insertCalls  int
	historyCalls int
}

func validOrderTransaction() *testOrderTransaction { return &testOrderTransaction{} }

func (*testOrderTransaction) LockUserByTelegramID(context.Context, int64) (User, error) {
	return User{ID: 10, TelegramUserID: 100, Status: domain.UserStatusActive}, nil
}

func (tx *testOrderTransaction) FindOrderByIdempotency(context.Context, int64, string) (OrderDetail, error) {
	if tx.existing.ID > 0 {
		return tx.existing, nil
	}
	return OrderDetail{}, ErrOrderNotFound
}

func (*testOrderTransaction) GetProductForOrderCreation(context.Context, int64) (OrderProductRecord, error) {
	return OrderProductRecord{ID: 20, Name: "Product", Price: 10_000, DeliveryType: "inventory", ProductActive: true, CategoryActive: true}, nil
}

func (*testOrderTransaction) CountClaimableInventory(context.Context, int64) (int64, error) {
	return 10, nil
}

func (*testOrderTransaction) GetActiveBankAccountForOrder(context.Context, int64, string) (BankAccountRecord, error) {
	return BankAccountRecord{
		BankAccountOption: BankAccountOption{ID: 30, BankBIN: "970422", BankName: "Test", DisplayName: "Primary", AccountName: "OWNER", Last4: "7890"},
		Protected:         ProtectedBankAccountNumber{Format: "test"}, Active: true,
	}, nil
}

func (tx *testOrderTransaction) InsertPendingOrder(_ context.Context, input PendingOrderInsert) (OrderDetail, bool, error) {
	tx.insertCalls++
	if tx.insertCalls <= tx.collisions {
		return OrderDetail{}, false, nil
	}
	return OrderDetail{
		ID: int64(100 + tx.insertCalls), UserID: input.UserID, Status: domain.OrderStatusPendingPayment,
		Total: input.LineTotal, PaymentReference: input.PaymentReference, ExpiresAt: input.ExpiresAt,
		BankBIN: input.Bank.BankBIN, BankName: input.Bank.BankName, BankDisplayName: input.Bank.DisplayName,
		BankAccountName: input.Bank.AccountName, BankProtected: input.Bank.Protected,
	}, true, nil
}

func (*testOrderTransaction) InsertOrderItem(_ context.Context, _ int64, item OrderItemSnapshot) (OrderItemSnapshot, error) {
	item.ID = 1
	return item, nil
}

func (tx *testOrderTransaction) InsertOrderHistory(context.Context, int64, *domain.OrderStatus, domain.OrderStatus, string, string, int64, string) error {
	tx.historyCalls++
	return nil
}

func (*testOrderTransaction) LockOrderOwnedByTelegramUser(context.Context, int64, int64) (OrderDetail, error) {
	return OrderDetail{}, ErrOrderNotFound
}

func (*testOrderTransaction) CancelPendingOrder(context.Context, int64, int64, int64, time.Time) (OrderDetail, error) {
	return OrderDetail{}, nil
}

func (*testOrderTransaction) CompleteUpdate(context.Context, int64) error { return nil }

type sequenceReferences struct{ calls int }

func (g *sequenceReferences) Generate() (string, error) {
	g.calls++
	return fmt.Sprintf("TS%06d", g.calls), nil
}

type testBankCipher struct{}

func (testBankCipher) Protect(context.Context, string, string) (ProtectedBankAccountNumber, error) {
	return ProtectedBankAccountNumber{}, nil
}

func (testBankCipher) Decrypt(context.Context, string, ProtectedBankAccountNumber) (string, error) {
	return "1234567890", nil
}

type testInstructionGenerator struct{}

func (testInstructionGenerator) Generate(_ context.Context, request PaymentInstructionRequest) (PaymentInstruction, error) {
	return PaymentInstruction{
		ImageURL: "https://img.example.test/qr.png", BankDisplayName: request.BankDisplayName,
		BankName: request.BankName, AccountNumber: request.AccountNumber, AccountName: request.AccountName,
		Amount: request.Amount, TransferContent: request.PaymentReference, ExpiresAt: request.ExpiresAt,
	}, nil
}
