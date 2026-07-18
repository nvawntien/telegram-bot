package app

import (
	"context"
	"errors"
	"fmt"
)

type CatalogRepository interface {
	ListActiveCategories(context.Context, int32, int32) ([]Category, int64, error)
	ListActiveProducts(context.Context, int64, int32, int32) ([]Product, int64, error)
	GetActiveProduct(context.Context, int64) (Product, error)
	ListAdminCategories(context.Context, int32, int32) ([]Category, int64, error)
	ListAdminProducts(context.Context, int32, int32) ([]Product, int64, error)
}

type CatalogService struct {
	repository CatalogRepository
	pageSize   int
}

func NewCatalogService(repository CatalogRepository, pageSize int) *CatalogService {
	if pageSize <= 0 || pageSize > MaxPageSize {
		pageSize = DefaultPageSize
	}
	return &CatalogService{repository: repository, pageSize: pageSize}
}

func (s *CatalogService) ListCategories(ctx context.Context, page int) (CategoryPage, error) {
	offset, err := s.offset(page)
	if err != nil {
		return CategoryPage{}, err
	}
	items, total, err := s.repository.ListActiveCategories(ctx, int32(offset), int32(s.pageSize))
	if err != nil {
		return CategoryPage{}, fmt.Errorf("list active categories: %w", err)
	}
	return CategoryPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *CatalogService) ListProducts(ctx context.Context, categoryID int64, page int) (ProductPage, error) {
	if categoryID <= 0 {
		return ProductPage{}, ErrInvalidInput
	}
	offset, err := s.offset(page)
	if err != nil {
		return ProductPage{}, err
	}
	items, total, err := s.repository.ListActiveProducts(ctx, categoryID, int32(offset), int32(s.pageSize))
	if err != nil {
		return ProductPage{}, fmt.Errorf("list active products: %w", err)
	}
	return ProductPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *CatalogService) GetProduct(ctx context.Context, productID int64) (Product, error) {
	if productID <= 0 {
		return Product{}, ErrInvalidInput
	}
	product, err := s.repository.GetActiveProduct(ctx, productID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Product{}, ErrNotFound
		}
		return Product{}, fmt.Errorf("get active product: %w", err)
	}
	return product, nil
}

func (s *CatalogService) ListAdminCategories(ctx context.Context, page int) (CategoryPage, error) {
	offset, err := s.offset(page)
	if err != nil {
		return CategoryPage{}, err
	}
	items, total, err := s.repository.ListAdminCategories(ctx, int32(offset), int32(s.pageSize))
	if err != nil {
		return CategoryPage{}, fmt.Errorf("list admin categories: %w", err)
	}
	return CategoryPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *CatalogService) ListAdminProducts(ctx context.Context, page int) (ProductPage, error) {
	offset, err := s.offset(page)
	if err != nil {
		return ProductPage{}, err
	}
	items, total, err := s.repository.ListAdminProducts(ctx, int32(offset), int32(s.pageSize))
	if err != nil {
		return ProductPage{}, fmt.Errorf("list admin products: %w", err)
	}
	return ProductPage{Items: items, Page: pageInfo(page, s.pageSize, total)}, nil
}

func (s *CatalogService) offset(page int) (int, error) {
	if page < 0 {
		return 0, ErrInvalidInput
	}
	return page * s.pageSize, nil
}
