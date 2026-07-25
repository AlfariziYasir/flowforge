package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"flowforge/internal/domain"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var validOrderPattern = regexp.MustCompile(`^[a-zA-Z0-9_.]+(\s+(?i:asc|desc))?$`)

// StatementBuilder is the pre-configured Squirrel builder for PostgreSQL ($1, $2, etc.).
var StatementBuilder = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

// BaseRepository provides generic, type-safe, and tenant-isolated CRUD database operations.
type BaseRepository[T any] struct {
	pool      *pgxpool.Pool
	tableName string
}

// NewBaseRepository instantiates a new generic BaseRepository for the target table name.
func NewBaseRepository[T any](pool *pgxpool.Pool, tableName string) *BaseRepository[T] {
	return &BaseRepository[T]{
		pool:      pool,
		tableName: tableName,
	}
}

// TableName returns the configured table name.
func (r *BaseRepository[T]) TableName() string {
	return r.tableName
}

// GetDB returns active transaction from context if present, or defaults to the database pool.
func (r *BaseRepository[T]) GetDB(ctx context.Context) DBTX {
	return GetDBTX(ctx, r.pool)
}

// FindByID retrieves a single entity by tenant ID and record primary key ID.
func (r *BaseRepository[T]) FindByID(ctx context.Context, tenantID string, id string) (*T, error) {
	query, args, err := StatementBuilder.
		Select("*").
		From(r.tableName).
		Where(sq.Eq{"tenant_id": tenantID, "id": id}).
		Limit(1).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build find by id sql: %w", err)
	}

	db := r.GetDB(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s by id: %w", r.tableName, err)
	}

	item, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByName[T])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("failed to scan %s record: %w", r.tableName, err)
	}

	return item, nil
}

// FindAll retrieves all entities belonging to the specified tenant ID.
func (r *BaseRepository[T]) FindAll(ctx context.Context, tenantID string) ([]*T, error) {
	query, args, err := StatementBuilder.
		Select("*").
		From(r.tableName).
		Where(sq.Eq{"tenant_id": tenantID}).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build find all sql: %w", err)
	}

	db := r.GetDB(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query all %s records: %w", r.tableName, err)
	}

	items, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[T])
	if err != nil {
		return nil, fmt.Errorf("failed to collect %s records: %w", r.tableName, err)
	}

	if items == nil {
		items = []*T{}
	}

	return items, nil
}

// PaginationParams encapsulates pagination and sorting options.
type PaginationParams struct {
	Page     int
	PageSize int
	OrderBy  string
}

// Paginate retrieves a page of entities for a tenant alongside the total count.
func (r *BaseRepository[T]) Paginate(ctx context.Context, tenantID string, params PaginationParams) ([]*T, int64, error) {
	page := params.Page
	if page < 1 {
		page = 1
	}
	pageSize := params.PageSize
	if pageSize < 1 {
		pageSize = 20
	}

	db := r.GetDB(ctx)

	countQuery, countArgs, err := StatementBuilder.
		Select("COUNT(*)").
		From(r.tableName).
		Where(sq.Eq{"tenant_id": tenantID}).
		ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build count sql: %w", err)
	}

	var total int64
	if err := db.QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count %s records: %w", r.tableName, err)
	}

	sanitizedOrder := sanitizeOrderBy(params.OrderBy)
	offset := uint64((page - 1) * pageSize)

	itemsQuery, itemsArgs, err := StatementBuilder.
		Select("*").
		From(r.tableName).
		Where(sq.Eq{"tenant_id": tenantID}).
		OrderBy(sanitizedOrder).
		Limit(uint64(pageSize)).
		Offset(offset).
		ToSql()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to build paginated items sql: %w", err)
	}

	rows, err := db.Query(ctx, itemsQuery, itemsArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query paginated %s records: %w", r.tableName, err)
	}

	items, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[T])
	if err != nil {
		return nil, 0, fmt.Errorf("failed to collect paginated %s records: %w", r.tableName, err)
	}

	if items == nil {
		items = []*T{}
	}

	return items, total, nil
}

// DeleteByPK deletes a record by tenant ID and primary key ID.
func (r *BaseRepository[T]) DeleteByPK(ctx context.Context, tenantID string, id string) error {
	query, args, err := StatementBuilder.
		Delete(r.tableName).
		Where(sq.Eq{"tenant_id": tenantID, "id": id}).
		ToSql()
	if err != nil {
		return fmt.Errorf("failed to build delete sql: %w", err)
	}

	db := r.GetDB(ctx)
	tag, err := db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to delete %s record: %w", r.tableName, err)
	}

	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}

	return nil
}

func sanitizeOrderBy(orderBy string) string {
	trimmed := strings.TrimSpace(orderBy)
	if trimmed == "" || !validOrderPattern.MatchString(trimmed) {
		return "created_at DESC"
	}
	return trimmed
}
