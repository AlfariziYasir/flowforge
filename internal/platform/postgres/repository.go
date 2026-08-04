package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"flowforge/internal/domain"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var validOrderPattern = regexp.MustCompile(`^[a-zA-Z0-9_.]+(\s+(?i:asc|desc))?$`)

var StatementBuilder = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505), and returns the violated constraint name.
func IsUniqueViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

type BaseRepository[T any] struct {
	pool      *pgxpool.Pool
	tableName string
}

func NewBaseRepository[T any](pool *pgxpool.Pool, tableName string) *BaseRepository[T] {
	return &BaseRepository[T]{
		pool:      pool,
		tableName: tableName,
	}
}

func (r *BaseRepository[T]) TableName() string {
	return r.tableName
}

func (r *BaseRepository[T]) GetDB(ctx context.Context) DBTX {
	return GetDBTX(ctx, r.pool)
}

func (r *BaseRepository[T]) FindByFilter(ctx context.Context, filters map[string]interface{}) (*T, error) {
	query, args, err := StatementBuilder.
		Select("*").
		From(r.tableName).
		Where(sq.Eq(filters)).
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

type PaginationParams struct {
	Page     int
	PageSize int
	OrderBy  string
	Search   map[string]string
	Filters  map[string]interface{}
	Exclude  map[string]interface{}
}

var validColumnPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func BuildPaginateSQL(tableName string, params PaginationParams) (string, []any, string, []any, error) {
	return buildPaginateSQL(tableName, params)
}

func buildPaginateSQL(tableName string, params PaginationParams) (string, []any, string, []any, error) {
	page := params.Page
	if page < 1 {
		page = 1
	}
	pageSize := params.PageSize
	if pageSize < 1 {
		pageSize = 20
	}

	countBuilder := StatementBuilder.
		Select("COUNT(*)").
		From(tableName)

	itemBuilder := StatementBuilder.
		Select("*").
		From(tableName)

	for k, v := range params.Filters {
		if !validColumnPattern.MatchString(k) {
			return "", nil, "", nil, fmt.Errorf("invalid filter column %q", k)
		}
		countBuilder = countBuilder.Where(sq.Eq{k: v})
		itemBuilder = itemBuilder.Where(sq.Eq{k: v})
	}

	for k, v := range params.Exclude {
		if !validColumnPattern.MatchString(k) {
			return "", nil, "", nil, fmt.Errorf("invalid exclude column %q", k)
		}
		countBuilder = countBuilder.Where(sq.NotEq{k: v})
		itemBuilder = itemBuilder.Where(sq.NotEq{k: v})
	}

	for k, v := range params.Search {
		if !validColumnPattern.MatchString(k) {
			return "", nil, "", nil, fmt.Errorf("invalid search column %q", k)
		}
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			countBuilder = countBuilder.Where(sq.ILike{k: escapeLike(trimmed)})
			itemBuilder = itemBuilder.Where(sq.ILike{k: escapeLike(trimmed)})
		}
	}

	countQuery, countArgs, err := countBuilder.ToSql()
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("failed to build count sql: %w", err)
	}

	sanitizedOrder := sanitizeOrderBy(params.OrderBy)
	offset := uint64((page - 1) * pageSize)
	itemsQuery, itemsArgs, err := itemBuilder.
		OrderBy(sanitizedOrder).
		Limit(uint64(pageSize)).
		Offset(offset).
		ToSql()
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("failed to build paginated items sql: %w", err)
	}

	return itemsQuery, itemsArgs, countQuery, countArgs, nil
}

func (r *BaseRepository[T]) Paginate(ctx context.Context, params PaginationParams) ([]*T, int64, error) {
	itemsQuery, itemsArgs, countQuery, countArgs, err := buildPaginateSQL(r.tableName, params)
	if err != nil {
		return nil, 0, err
	}

	db := r.GetDB(ctx)

	var total int64
	if err := db.QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count %s records: %w", r.tableName, err)
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

func (r *BaseRepository[T]) Create(ctx context.Context, entity *T) error {
	if entity == nil {
		return errors.New("cannot create nil entity")
	}

	setMap, err := structToMap(entity)
	if err != nil {
		return fmt.Errorf("failed to map %s entity fields: %w", r.tableName, err)
	}

	query, args, err := StatementBuilder.
		Insert(r.tableName).
		SetMap(setMap).
		ToSql()
	if err != nil {
		return fmt.Errorf("failed to build create sql: %w", err)
	}

	db := r.GetDB(ctx)
	_, err = db.Exec(ctx, query, args...)
	if err != nil {
		if constraint, ok := IsUniqueViolation(err); ok {
			return fmt.Errorf("%w: constraint %s violated", domain.ErrConflict, constraint)
		}
		return fmt.Errorf("failed to create %s record: %w", r.tableName, err)
	}

	return nil
}

func (r *BaseRepository[T]) Update(ctx context.Context, tenantID, id string, entity *T) error {
	if entity == nil {
		return errors.New("cannot update nil entity")
	}

	setMap, err := structToMap(entity)
	if err != nil {
		return fmt.Errorf("failed to map %s entity fields: %w", r.tableName, err)
	}

	delete(setMap, "tenant_id")
	delete(setMap, "id")

	query, args, err := StatementBuilder.
		Update(r.tableName).
		SetMap(setMap).
		Where(sq.Eq{"tenant_id": tenantID, "id": id}).
		ToSql()
	if err != nil {
		return fmt.Errorf("failed to build update sql: %w", err)
	}

	db := r.GetDB(ctx)
	tag, err := db.Exec(ctx, query, args...)
	if err != nil {
		if constraint, ok := IsUniqueViolation(err); ok {
			return fmt.Errorf("%w: constraint %s violated", domain.ErrConflict, constraint)
		}
		return fmt.Errorf("failed to update %s record: %w", r.tableName, err)
	}

	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}

	return nil
}

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

func structToMap(obj any) (map[string]any, error) {
	val := reflect.ValueOf(obj)

	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return nil, errors.New("object is nil")
		}

		val = val.Elem()
	}

	if val.Kind() != reflect.Struct {
		return nil, errors.New("expected a struct or pointer struct")
	}

	typ := val.Type()
	setMap := make(map[string]any, typ.NumField())

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		if !field.IsExported() {
			continue
		}

		dbTag := field.Tag.Get("db")
		if dbTag == "-" {
			continue
		}

		if dbTag == "" {
			jsonTag := field.Tag.Get("json")
			if jsonTag != "" && jsonTag != "-" {
				dbTag = strings.Split(jsonTag, ",")[0]
			}
		}

		if dbTag == "" || dbTag == "-" {
			continue
		}

		setMap[dbTag] = val.Field(i).Interface()
	}

	return setMap, nil
}

func EscapeLike(s string) string {
	return escapeLike(s)
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return "%" + s + "%"
}
