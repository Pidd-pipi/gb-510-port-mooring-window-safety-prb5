package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/blueship581/port-mooring-window-safety/backend/internal/dto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrVersionConflict = errors.New("record was changed by another request")

type Page[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
}

// Store centralizes consistent paging and optimistic-lock semantics while
// concrete repository files retain an explicit boundary for each aggregate.
type Store[T any] struct {
	db *gorm.DB
}

func NewStore[T any](db *gorm.DB) *Store[T] { return &Store[T]{db: db} }

// supportsRowLocking reports whether the active dialect understands
// SELECT ... FOR UPDATE. The pure-Go SQLite test driver serializes writers and
// has no such clause, so locking is skipped there.
func supportsRowLocking(db *gorm.DB) bool {
	name := db.Dialector.Name()
	return name == "postgres" || name == "mysql"
}

func (s *Store[T]) List(ctx context.Context, query dto.PageQuery) (Page[T], error) {
	page, pageSize := normalizePage(query.Page, query.PageSize)
	db := conn(ctx, s.db).Model(new(T))
	if search := strings.TrimSpace(strings.ToLower(query.Search)); search != "" {
		wildcard := "%" + search + "%"
		db = db.Where("LOWER(code) LIKE ? OR LOWER(name) LIKE ?", wildcard, wildcard)
	}
	if status := strings.TrimSpace(query.Status); status != "" {
		db = db.Where("status = ?", status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return Page[T]{}, err
	}
	items := make([]T, 0)
	err := db.Order("updated_at DESC, id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error
	return Page[T]{Items: items, Total: total, Page: page, PageSize: pageSize}, err
}

func (s *Store[T]) Get(ctx context.Context, id uint) (T, error) {
	var item T
	err := conn(ctx, s.db).First(&item, id).Error
	return item, err
}

// GetForUpdate re-reads a row with a row lock when the dialect supports it.
// Inside the clearance confirm transaction it closes the race where two
// reviewers confirm the same pending clearance concurrently.
func (s *Store[T]) GetForUpdate(ctx context.Context, id uint) (T, error) {
	var item T
	db := conn(ctx, s.db)
	if supportsRowLocking(s.db) {
		db = db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := db.First(&item, id).Error
	return item, err
}

func (s *Store[T]) Create(ctx context.Context, item *T) error {
	return conn(ctx, s.db).Create(item).Error
}

// ListByCodes returns every aggregate whose code matches the given set. The
// clearance read model uses it to enrich a whole page with one query per
// aggregate instead of one query per row.
func (s *Store[T]) ListByCodes(ctx context.Context, codes []string) ([]T, error) {
	items := make([]T, 0)
	if len(codes) == 0 {
		return items, nil
	}
	err := conn(ctx, s.db).Where("code IN ?", codes).Find(&items).Error
	return items, err
}

func (s *Store[T]) Update(ctx context.Context, id, expectedVersion uint, item *T) error {
	result := conn(ctx, s.db).Model(new(T)).
		Where("id = ? AND version = ?", id, expectedVersion).
		Select("*").Omit("id", "code", "created_at", "deleted_at").Updates(item)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrVersionConflict
	}
	return nil
}

func (s *Store[T]) Delete(ctx context.Context, id uint) error {
	result := conn(ctx, s.db).Delete(new(T), id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store[T]) CountByStatus(ctx context.Context) (map[string]int64, error) {
	rows, err := conn(ctx, s.db).Model(new(T)).
		Select("status, COUNT(*) AS total").Group("status").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var status string
		var total int64
		if err := rows.Scan(&status, &total); err != nil {
			return nil, err
		}
		counts[status] = total
	}
	return counts, rows.Err()
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}
