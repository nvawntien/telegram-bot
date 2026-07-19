package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

const paymentReferenceAttempts = 8

type PaymentInstructionGenerator interface {
	Generate(context.Context, PaymentInstructionRequest) (PaymentInstruction, error)
}

type OrderProductRecord struct {
	ID             int64
	Name           string
	Price          domain.Money
	DeliveryType   string
	ProductActive  bool
	CategoryActive bool
}

type PendingOrderInsert struct {
	UserID             int64
	Product            OrderProductRecord
	Quantity           int32
	LineTotal          domain.Money
	PaymentReference   string
	IdempotencyKey     string
	ExpiresAt          time.Time
	Bank               BankAccountRecord
	PaymentEnvironment string
}

type OrderTransaction interface {
	LockUserByTelegramID(context.Context, int64) (User, error)
	FindOrderByIdempotency(context.Context, int64, string) (OrderDetail, error)
	GetProductForOrderCreation(context.Context, int64) (OrderProductRecord, error)
	CountClaimableInventory(context.Context, int64) (int64, error)
	GetActiveBankAccountForOrder(context.Context, int64, string) (BankAccountRecord, error)
	InsertPendingOrder(context.Context, PendingOrderInsert) (OrderDetail, bool, error)
	InsertOrderItem(context.Context, int64, OrderItemSnapshot) (OrderItemSnapshot, error)
	InsertOrderHistory(context.Context, int64, *domain.OrderStatus, domain.OrderStatus, string, string, int64, string) error
	LockOrderOwnedByTelegramUser(context.Context, int64, int64) (OrderDetail, error)
	CancelPendingOrder(context.Context, int64, int64, int64, time.Time) (OrderDetail, error)
	CompleteUpdate(context.Context, int64) error
}

type OrderRepository interface {
	WithinOrderTransaction(context.Context, func(OrderTransaction) error) error
	ListOrdersOwnedByTelegramUser(context.Context, int64, int32, int32) ([]OrderSummary, int64, error)
	GetOrderOwnedByTelegramUser(context.Context, int64, int64) (OrderDetail, error)
}

type OrderService struct {
	repository         OrderRepository
	cipher             BankAccountCipher
	instructions       PaymentInstructionGenerator
	references         PaymentReferenceGenerator
	expiry             time.Duration
	maxQuantity        int32
	pageSize           int
	clock              func() time.Time
	paymentEnvironment string
}

func NewOrderService(
	repository OrderRepository,
	cipher BankAccountCipher,
	instructions PaymentInstructionGenerator,
	references PaymentReferenceGenerator,
	expiry time.Duration,
	maxQuantity int32,
	pageSize int,
) *OrderService {
	if pageSize <= 0 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}
	return &OrderService{
		repository: repository, cipher: cipher, instructions: instructions, references: references,
		expiry: expiry, maxQuantity: maxQuantity, pageSize: pageSize, clock: time.Now,
		paymentEnvironment: "production",
	}
}

func (s *OrderService) WithPaymentEnvironment(environment string) *OrderService {
	if validPaymentEnvironment(environment) {
		s.paymentEnvironment = environment
	}
	return s
}

func (s *OrderService) Create(ctx context.Context, command CreateOrderCommand) (CreateOrderResult, error) {
	if command.TelegramUserID <= 0 || command.ProductID <= 0 || command.BankAccountID <= 0 || command.IdempotencyKey == "" || command.Meta.UpdateID <= 0 {
		return CreateOrderResult{}, ErrInvalidInput
	}
	if command.Quantity <= 0 {
		return CreateOrderResult{}, ErrInvalidQuantity
	}
	if s.maxQuantity <= 0 || command.Quantity > s.maxQuantity {
		return CreateOrderResult{}, ErrQuantityLimitExceeded
	}
	if s.expiry <= 0 {
		return CreateOrderResult{}, ErrInvalidInput
	}

	var result CreateOrderResult
	err := s.repository.WithinOrderTransaction(ctx, func(tx OrderTransaction) error {
		user, err := tx.LockUserByTelegramID(ctx, command.TelegramUserID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrUnauthorized
			}
			return err
		}
		if user.Status != domain.UserStatusActive {
			return ErrUserBlocked
		}

		existing, err := tx.FindOrderByIdempotency(ctx, user.ID, command.IdempotencyKey)
		if err == nil {
			result.Order = existing
			result.Duplicate = true
			return tx.CompleteUpdate(ctx, command.Meta.UpdateID)
		}
		if !errors.Is(err, ErrOrderNotFound) {
			return err
		}

		product, err := tx.GetProductForOrderCreation(ctx, command.ProductID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrOrderNotFound
			}
			return err
		}
		if !product.CategoryActive {
			return ErrCategoryInactive
		}
		if !product.ProductActive || product.DeliveryType != "inventory" {
			return ErrProductInactive
		}
		lineTotal, err := product.Price.Multiply(command.Quantity)
		if errors.Is(err, domain.ErrMoneyOverflow) {
			return ErrMoneyOverflow
		}
		if errors.Is(err, domain.ErrInvalidQuantity) {
			return ErrInvalidQuantity
		}
		if err != nil {
			return err
		}
		available, err := tx.CountClaimableInventory(ctx, product.ID)
		if err != nil {
			return err
		}
		if available < int64(command.Quantity) {
			return ErrInsufficientInventory
		}
		bank, err := tx.GetActiveBankAccountForOrder(ctx, command.BankAccountID, s.paymentEnvironment)
		if err != nil {
			if errors.Is(err, ErrBankAccountNotFound) || errors.Is(err, ErrNotFound) {
				return ErrBankAccountInactive
			}
			return err
		}

		expiresAt := s.clock().Add(s.expiry)
		for attempt := 0; attempt < paymentReferenceAttempts; attempt++ {
			reference, err := s.references.Generate()
			if err != nil {
				return err
			}
			order, inserted, err := tx.InsertPendingOrder(ctx, PendingOrderInsert{
				UserID: user.ID, Product: product, Quantity: command.Quantity,
				LineTotal: lineTotal, PaymentReference: reference,
				IdempotencyKey: command.IdempotencyKey, ExpiresAt: expiresAt, Bank: bank,
				PaymentEnvironment: s.paymentEnvironment,
			})
			if err != nil {
				return err
			}
			if !inserted {
				existing, findErr := tx.FindOrderByIdempotency(ctx, user.ID, command.IdempotencyKey)
				if findErr == nil {
					result.Order = existing
					result.Duplicate = true
					return tx.CompleteUpdate(ctx, command.Meta.UpdateID)
				}
				if !errors.Is(findErr, ErrOrderNotFound) {
					return findErr
				}
				continue
			}
			item, err := tx.InsertOrderItem(ctx, order.ID, OrderItemSnapshot{
				ProductID: product.ID, Name: product.Name, UnitPrice: product.Price,
				Quantity: command.Quantity, LineTotal: lineTotal,
			})
			if err != nil {
				return err
			}
			order.Item = item
			if err := tx.InsertOrderHistory(ctx, order.ID, nil, domain.OrderStatusPendingPayment, "order_created", "user", user.ID, command.Meta.RequestID); err != nil {
				return err
			}
			if err := tx.CompleteUpdate(ctx, command.Meta.UpdateID); err != nil {
				return err
			}
			result.Order = order
			return nil
		}
		return ErrPaymentReferenceCollision
	})
	if err != nil {
		return CreateOrderResult{}, fmt.Errorf("create order: %w", err)
	}
	instruction, err := s.instruction(ctx, result.Order)
	if err != nil {
		return CreateOrderResult{}, err
	}
	result.Instruction = instruction
	return result, nil
}

func (s *OrderService) List(ctx context.Context, telegramUserID int64, page int) (OrderPage, error) {
	if telegramUserID <= 0 || page < 0 {
		return OrderPage{}, ErrInvalidInput
	}
	items, total, err := s.repository.ListOrdersOwnedByTelegramUser(ctx, telegramUserID, int32(page*s.pageSize), int32(s.pageSize))
	if err != nil {
		return OrderPage{}, fmt.Errorf("list orders: %w", err)
	}
	return OrderPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *OrderService) Get(ctx context.Context, telegramUserID, orderID int64) (OrderDetail, PaymentInstruction, error) {
	if telegramUserID <= 0 || orderID <= 0 {
		return OrderDetail{}, PaymentInstruction{}, ErrInvalidInput
	}
	order, err := s.repository.GetOrderOwnedByTelegramUser(ctx, telegramUserID, orderID)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) || errors.Is(err, ErrOrderNotOwned) {
			return OrderDetail{}, PaymentInstruction{}, ErrOrderNotFound
		}
		return OrderDetail{}, PaymentInstruction{}, fmt.Errorf("get order: %w", err)
	}
	if order.BankAccountID == 0 {
		return order, PaymentInstruction{}, nil
	}
	instruction, err := s.instruction(ctx, order)
	if err != nil {
		return OrderDetail{}, PaymentInstruction{}, err
	}
	return order, instruction, nil
}

func (s *OrderService) Cancel(ctx context.Context, command CancelOrderCommand) (CancelOrderResult, error) {
	if command.TelegramUserID <= 0 || command.OrderID <= 0 || command.ExpectedVersion <= 0 || command.Meta.UpdateID <= 0 {
		return CancelOrderResult{}, ErrInvalidInput
	}
	var result CancelOrderResult
	err := s.repository.WithinOrderTransaction(ctx, func(tx OrderTransaction) error {
		order, err := tx.LockOrderOwnedByTelegramUser(ctx, command.TelegramUserID, command.OrderID)
		if err != nil {
			return ErrOrderNotFound
		}
		if order.Status == domain.OrderStatusCancelled {
			result.Order = order
			result.AlreadyCancelled = true
			return tx.CompleteUpdate(ctx, command.Meta.UpdateID)
		}
		now := s.clock()
		if order.Status == domain.OrderStatusExpired || !order.ExpiresAt.After(now) {
			return ErrOrderExpired
		}
		if order.Version != command.ExpectedVersion {
			return ErrStaleVersion
		}
		if err := domain.ValidateCustomerOrderTransition(order.Status, domain.OrderStatusCancelled); err != nil {
			return ErrInvalidOrderState
		}
		updated, err := tx.CancelPendingOrder(ctx, order.ID, order.UserID, order.Version, now)
		if err != nil {
			return err
		}
		updated.Item = order.Item
		if err := tx.InsertOrderHistory(ctx, order.ID, &order.Status, domain.OrderStatusCancelled, "customer_cancelled", "user", order.UserID, command.Meta.RequestID); err != nil {
			return err
		}
		if err := tx.CompleteUpdate(ctx, command.Meta.UpdateID); err != nil {
			return err
		}
		result.Order = updated
		return nil
	})
	if err != nil {
		return CancelOrderResult{}, fmt.Errorf("cancel order: %w", err)
	}
	return result, nil
}

func (s *OrderService) instruction(ctx context.Context, order OrderDetail) (PaymentInstruction, error) {
	accountNumber, err := s.cipher.Decrypt(ctx, order.BankBIN, order.BankProtected)
	if err != nil {
		return PaymentInstruction{}, fmt.Errorf("decrypt payment instruction: %w", err)
	}
	instruction, err := s.instructions.Generate(ctx, PaymentInstructionRequest{
		BankBIN: order.BankBIN, BankName: order.BankName, AccountNumber: accountNumber,
		AccountName: order.BankAccountName, BankDisplayName: order.BankDisplayName,
		Amount: order.Total, PaymentReference: order.PaymentReference,
		OrderID: order.ID, ExpiresAt: order.ExpiresAt,
	})
	if err != nil {
		return PaymentInstruction{}, fmt.Errorf("generate payment instruction: %w", err)
	}
	return instruction, nil
}
