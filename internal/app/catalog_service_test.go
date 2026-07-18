package app

import (
	"context"
	"errors"
	"testing"
)

type catalogRepositoryStub struct {
	offset int32
	limit  int32
}

func (s *catalogRepositoryStub) ListActiveCategories(_ context.Context, offset, limit int32) ([]Category, int64, error) {
	s.offset, s.limit = offset, limit
	return []Category{{ID: int64(offset + 1)}}, 13, nil
}

func (s *catalogRepositoryStub) ListActiveProducts(_ context.Context, _ int64, offset, limit int32) ([]Product, int64, error) {
	s.offset, s.limit = offset, limit
	return []Product{{ID: int64(offset + 1)}}, 13, nil
}

func (*catalogRepositoryStub) GetActiveProduct(_ context.Context, id int64) (Product, error) {
	if id == 99 {
		return Product{}, ErrNotFound
	}
	return Product{ID: id}, nil
}

func (s *catalogRepositoryStub) ListAdminCategories(ctx context.Context, offset, limit int32) ([]Category, int64, error) {
	return s.ListActiveCategories(ctx, offset, limit)
}

func (s *catalogRepositoryStub) ListAdminProducts(ctx context.Context, offset, limit int32) ([]Product, int64, error) {
	return s.ListActiveProducts(ctx, 1, offset, limit)
}

func TestCatalogPaginationIsBoundedAndDeterministic(t *testing.T) {
	repository := &catalogRepositoryStub{}
	service := NewCatalogService(repository, 5)
	page, err := service.ListCategories(context.Background(), 2)
	if err != nil {
		t.Fatalf("ListCategories() error = %v", err)
	}
	if repository.offset != 10 || repository.limit != 5 {
		t.Fatalf("offset/limit = %d/%d, want 10/5", repository.offset, repository.limit)
	}
	if page.Page.TotalPages != 3 || page.Page.Page != 2 {
		t.Fatalf("page info = %#v", page.Page)
	}
	if _, err := service.ListCategories(context.Background(), -1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative page error = %v", err)
	}
}

func TestCatalogNotFoundMapping(t *testing.T) {
	service := NewCatalogService(&catalogRepositoryStub{}, 5)
	if _, err := service.GetProduct(context.Background(), 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetProduct() error = %v", err)
	}
}
