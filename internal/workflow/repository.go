package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"flowforge/internal/domain"
	"flowforge/internal/platform/postgres"
)

var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

type ListWorkflowsFilter struct {
	TenantID       uuid.UUID
	Page, PageSize int
	Status         string
	Search         string
	OrderByColumn  string
	OrderAsc       bool
	ExcludeStatus  string
}

type WorkflowRepository interface {
	Create(ctx context.Context, wf *domain.Workflow) error
	FindByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error)
	FindByIDForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error)
	List(ctx context.Context, f ListWorkflowsFilter) ([]*domain.Workflow, int64, error)
	UpdateMetadata(ctx context.Context, wf *domain.Workflow, expectedRowVersion int) error
	UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status string, expectedRowVersion int) error
	SetCurrentVersion(ctx context.Context, tenantID, workflowID, versionID uuid.UUID, versionNumber, expectedRowVersion int) error
	TouchRowVersion(ctx context.Context, tenantID, id uuid.UUID, expectedRowVersion int) error
}

type VersionRepository interface {
	CreateVersion(ctx context.Context, v *domain.WorkflowVersion) error
	FindVersionByID(ctx context.Context, tenantID, workflowID, versionID uuid.UUID) (*domain.WorkflowVersion, error)
	FindDraftVersion(ctx context.Context, tenantID, workflowID uuid.UUID) (*domain.WorkflowVersion, error)
	ListVersions(ctx context.Context, tenantID, workflowID uuid.UUID) ([]*domain.WorkflowVersion, error)
	MaxVersionNumber(ctx context.Context, tenantID, workflowID uuid.UUID) (int, error)
	MarkPublished(ctx context.Context, tenantID, versionID uuid.UUID, checksum string, snapshot []byte, publishedAt time.Time) error
	ReplaceGraph(ctx context.Context, tenantID, versionID uuid.UUID, nodes []domain.WorkflowNode, edges []domain.WorkflowEdge) error
	LoadGraph(ctx context.Context, tenantID, versionID uuid.UUID) ([]domain.WorkflowNode, []domain.WorkflowEdge, error)
}

// ---------- pure SQL builders ----------k
func buildUpdateMetadataSQL(wf *domain.Workflow, expected int) (string, []any, error) {
	return psql.Update("workflows").
		Set("name", wf.Name).
		Set("description", wf.Description).
		Set("row_version", sq.Expr("row_version + 1")).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": wf.TenantID, "id": wf.ID, "row_version": expected}).
		ToSql()
}

func buildUpdateStatusSQL(tenantID, id uuid.UUID, status string, expected int) (string, []any, error) {
	return psql.Update("workflows").
		Set("status", status).
		Set("row_version", sq.Expr("row_version + 1")).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": tenantID, "id": id, "row_version": expected}).
		ToSql()
}

func buildSetCurrentVersionSQL(tenantID, wfID, verID uuid.UUID, num, expected int) (string, []any, error) {
	return psql.Update("workflows").
		Set("current_version_id", verID).
		Set("current_version_number", num).
		Set("status", domain.WorkflowStatusPublished).
		Set("row_version", sq.Expr("row_version + 1")).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": tenantID, "id": wfID, "row_version": expected}).
		ToSql()
}

func buildTouchRowVersionSQL(tenantID, id uuid.UUID, expected int) (string, []any, error) {
	return psql.Update("workflows").
		Set("row_version", sq.Expr("row_version + 1")).
		Set("updated_at", sq.Expr("NOW()")).
		Where(sq.Eq{"tenant_id": tenantID, "id": id, "row_version": expected}).
		ToSql()
}

func buildMaxVersionSQL(tenantID, wfID uuid.UUID) (string, []any, error) {
	return psql.Select("COALESCE(MAX(version_number), 0)").
		From("workflow_versions").
		Where(sq.Eq{"tenant_id": tenantID, "workflow_id": wfID}).
		ToSql()
}

// ---------- Repository implementations ----------

type postgresWorkflowRepository struct {
	base *postgres.BaseRepository[domain.Workflow]
	pool *pgxpool.Pool
}

func NewWorkflowRepository(pool *pgxpool.Pool) WorkflowRepository {
	return &postgresWorkflowRepository{
		base: postgres.NewBaseRepository[domain.Workflow](pool, "workflows"),
		pool: pool,
	}
}

func (r *postgresWorkflowRepository) getRunner(ctx context.Context) postgres.DBTX {
	if tx, ok := postgres.TxFromContext(ctx); ok {
		return tx
	}
	return r.pool
}

func (r *postgresWorkflowRepository) Create(ctx context.Context, wf *domain.Workflow) error {
	if wf.ID == uuid.Nil {
		wf.ID = uuid.New()
	}
	now := time.Now()
	if wf.CreatedAt.IsZero() {
		wf.CreatedAt = now
	}
	if wf.UpdatedAt.IsZero() {
		wf.UpdatedAt = now
	}
	if wf.RowVersion == 0 {
		wf.RowVersion = 1
	}
	if wf.Status == "" {
		wf.Status = domain.WorkflowStatusDraft
	}

	err := r.base.Create(ctx, wf)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return ErrWorkflowAlreadyExists
		}
		return err
	}
	return nil
}

func (r *postgresWorkflowRepository) FindByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) {
	wf, err := r.base.FindByFilter(ctx, sq.Eq{"tenant_id": tenantID, "id": id})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrWorkflowNotFound
		}
		return nil, err
	}
	return wf, nil
}

func (r *postgresWorkflowRepository) FindByIDForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) {
	query, args, err := psql.Select("id", "tenant_id", "name", "description", "status", "current_version_number", "current_version_id", "row_version", "created_at", "updated_at").
		From("workflows").
		Where(sq.Eq{"tenant_id": tenantID, "id": id}).
		Suffix("FOR UPDATE").
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build select for update sql: %w", err)
	}

	runner := r.getRunner(ctx)

	rows, err := runner.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("execute select for update: %w", err)
	}

	wf, err := pgx.RowToAddrOfStructByName[domain.Workflow](rows)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkflowNotFound
		}
		return nil, fmt.Errorf("scan workflow for update: %w", err)
	}

	return wf, nil
}

func buildListWorkflowsParams(f ListWorkflowsFilter) postgres.PaginationParams {
	filter := map[string]interface{}{
		"tenant_id": f.TenantID,
	}
	if f.Status != "" {
		filter["status"] = f.Status
	}

	exclude := make(map[string]interface{})
	if f.ExcludeStatus != "" {
		exclude["status"] = f.ExcludeStatus
	}

	order := "DESC"
	if f.OrderAsc {
		order = "ASC"
	}

	var searchMap map[string]string
	if f.Search != "" {
		searchMap = map[string]string{"name": f.Search}
	}

	return postgres.PaginationParams{
		Page:     f.Page,
		PageSize: f.PageSize,
		OrderBy:  fmt.Sprintf("%s %s", f.OrderByColumn, order),
		Filters:  filter,
		Exclude:  exclude,
		Search:   searchMap,
	}
}

func (r *postgresWorkflowRepository) List(ctx context.Context, f ListWorkflowsFilter) ([]*domain.Workflow, int64, error) {
	params := buildListWorkflowsParams(f)
	items, total, err := r.base.Paginate(ctx, params)
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

func (r *postgresWorkflowRepository) UpdateMetadata(ctx context.Context, wf *domain.Workflow, expectedRowVersion int) error {
	query, args, err := buildUpdateMetadataSQL(wf, expectedRowVersion)
	if err != nil {
		return err
	}
	return r.execOptimistic(ctx, query, args, wf.TenantID, wf.ID)
}

func (r *postgresWorkflowRepository) UpdateStatus(ctx context.Context, tenantID, id uuid.UUID, status string, expectedRowVersion int) error {
	query, args, err := buildUpdateStatusSQL(tenantID, id, status, expectedRowVersion)
	if err != nil {
		return err
	}
	return r.execOptimistic(ctx, query, args, tenantID, id)
}

func (r *postgresWorkflowRepository) SetCurrentVersion(ctx context.Context, tenantID, workflowID, versionID uuid.UUID, versionNumber, expectedRowVersion int) error {
	query, args, err := buildSetCurrentVersionSQL(tenantID, workflowID, versionID, versionNumber, expectedRowVersion)
	if err != nil {
		return err
	}
	return r.execOptimistic(ctx, query, args, tenantID, workflowID)
}

func (r *postgresWorkflowRepository) TouchRowVersion(ctx context.Context, tenantID, id uuid.UUID, expectedRowVersion int) error {
	query, args, err := buildTouchRowVersionSQL(tenantID, id, expectedRowVersion)
	if err != nil {
		return err
	}
	return r.execOptimistic(ctx, query, args, tenantID, id)
}

func (r *postgresWorkflowRepository) execOptimistic(ctx context.Context, query string, args []any, tenantID, id uuid.UUID) error {
	runner := r.getRunner(ctx)

	ct, err := runner.Exec(ctx, query, args...)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrWorkflowAlreadyExists
		}
		return fmt.Errorf("execute optimistic update: %w", err)
	}

	if ct.RowsAffected() == 0 {
		_, fetchErr := r.FindByID(ctx, tenantID, id)
		if fetchErr != nil {
			if errors.Is(fetchErr, ErrWorkflowNotFound) {
				return ErrWorkflowNotFound
			}
			return fetchErr
		}
		return ErrVersionConflict
	}
	return nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "unique constraint")
}

// ---------- VersionRepository implementation ----------

type postgresVersionRepository struct {
	base *postgres.BaseRepository[domain.WorkflowVersion]
	pool *pgxpool.Pool
}

func NewVersionRepository(pool *pgxpool.Pool) VersionRepository {
	return &postgresVersionRepository{
		base: postgres.NewBaseRepository[domain.WorkflowVersion](pool, "workflow_versions"),
		pool: pool,
	}
}

func (r *postgresVersionRepository) getRunner(ctx context.Context) postgres.DBTX {
	if tx, ok := postgres.TxFromContext(ctx); ok {
		return tx
	}
	if db, ok := postgres.DBTXFromContext(ctx); ok {
		return db
	}
	return r.pool
}

func (r *postgresVersionRepository) CreateVersion(ctx context.Context, v *domain.WorkflowVersion) error {
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now()
	}
	if v.Status == "" {
		v.Status = domain.VersionStatusDraft
	}
	if len(v.GraphSnapshot) == 0 {
		v.GraphSnapshot = json.RawMessage(`{}`)
	}
	if len(v.Metadata) == 0 {
		v.Metadata = json.RawMessage(`{}`)
	}

	err := r.base.Create(ctx, v)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrVersionConflict
		}
		return fmt.Errorf("create workflow version: %w", err)
	}
	return nil
}

func (r *postgresVersionRepository) FindVersionByID(ctx context.Context, tenantID, workflowID, versionID uuid.UUID) (*domain.WorkflowVersion, error) {
	v, err := r.base.FindByFilter(ctx, sq.Eq{"tenant_id": tenantID, "workflow_id": workflowID, "id": versionID})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrVersionNotFound
		}
		return nil, err
	}
	return v, nil
}

func (r *postgresVersionRepository) FindDraftVersion(ctx context.Context, tenantID, workflowID uuid.UUID) (*domain.WorkflowVersion, error) {
	v, err := r.base.FindByFilter(ctx, sq.Eq{"tenant_id": tenantID, "workflow_id": workflowID, "status": domain.VersionStatusDraft})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrDraftMissing
		}
		return nil, err
	}
	return v, nil
}

func (r *postgresVersionRepository) ListVersions(ctx context.Context, tenantID, workflowID uuid.UUID) ([]*domain.WorkflowVersion, error) {
	query, args, err := psql.Select("id", "tenant_id", "workflow_id", "version_number", "status", "graph_snapshot", "metadata", "checksum", "created_by", "published_at", "created_at").
		From("workflow_versions").
		Where(sq.Eq{"tenant_id": tenantID, "workflow_id": workflowID}).
		OrderBy("version_number DESC").
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("build list versions sql: %w", err)
	}

	runner := r.getRunner(ctx)

	rows, err := runner.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query list versions: %w", err)
	}

	versions, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[domain.WorkflowVersion])
	if err != nil {
		return nil, fmt.Errorf("collect version rows: %w", err)
	}

	return versions, nil
}

func (r *postgresVersionRepository) MaxVersionNumber(ctx context.Context, tenantID, workflowID uuid.UUID) (int, error) {
	query, args, err := buildMaxVersionSQL(tenantID, workflowID)
	if err != nil {
		return 0, err
	}

	runner := r.getRunner(ctx)

	var maxVal int
	if err := runner.QueryRow(ctx, query, args...).Scan(&maxVal); err != nil {
		return 0, fmt.Errorf("scan max version: %w", err)
	}
	return maxVal, nil
}

func (r *postgresVersionRepository) MarkPublished(ctx context.Context, tenantID, versionID uuid.UUID, checksum string, snapshot []byte, publishedAt time.Time) error {
	query, args, err := psql.Update("workflow_versions").
		Set("status", domain.VersionStatusPublished).
		Set("checksum", checksum).
		Set("graph_snapshot", json.RawMessage(snapshot)).
		Set("published_at", publishedAt).
		Where(sq.Eq{"tenant_id": tenantID, "id": versionID}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build mark published sql: %w", err)
	}

	runner := r.getRunner(ctx)

	ct, err := runner.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("execute mark published: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrVersionNotFound
	}
	return nil
}

func (r *postgresVersionRepository) ReplaceGraph(ctx context.Context, tenantID, versionID uuid.UUID, nodes []domain.WorkflowNode, edges []domain.WorkflowEdge) error {
	runner := r.getRunner(ctx)

	// 1. Delete edges
	delEdgesSQL, delEdgesArgs, err := psql.Delete("workflow_edges").
		Where(sq.Eq{"tenant_id": tenantID, "workflow_version_id": versionID}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build delete edges sql: %w", err)
	}
	if _, err := runner.Exec(ctx, delEdgesSQL, delEdgesArgs...); err != nil {
		return fmt.Errorf("delete graph edges: %w", err)
	}

	// 2. Delete nodes
	delNodesSQL, delNodesArgs, err := psql.Delete("workflow_nodes").
		Where(sq.Eq{"tenant_id": tenantID, "workflow_version_id": versionID}).
		ToSql()
	if err != nil {
		return fmt.Errorf("build delete nodes sql: %w", err)
	}
	if _, err := runner.Exec(ctx, delNodesSQL, delNodesArgs...); err != nil {
		return fmt.Errorf("delete graph nodes: %w", err)
	}

	// 3. Insert nodes
	for _, n := range nodes {
		insNodeSQL, insNodeArgs, err := psql.Insert("workflow_nodes").
			Columns("id", "tenant_id", "workflow_version_id", "node_key", "node_type", "config", "position_x", "position_y", "created_at").
			Values(n.ID, tenantID, versionID, n.NodeKey, n.NodeType, n.Config, n.PositionX, n.PositionY, n.CreatedAt).
			ToSql()
		if err != nil {
			return fmt.Errorf("build insert node sql: %w", err)
		}
		if _, err := runner.Exec(ctx, insNodeSQL, insNodeArgs...); err != nil {
			return fmt.Errorf("insert graph node %q: %w", n.NodeKey, err)
		}
	}

	// 4. Insert edges
	for _, e := range edges {
		insEdgeSQL, insEdgeArgs, err := psql.Insert("workflow_edges").
			Columns("id", "tenant_id", "workflow_version_id", "from_node_id", "to_node_id", "created_at").
			Values(e.ID, tenantID, versionID, e.FromNodeID, e.ToNodeID, e.CreatedAt).
			ToSql()
		if err != nil {
			return fmt.Errorf("build insert edge sql: %w", err)
		}
		if _, err := runner.Exec(ctx, insEdgeSQL, insEdgeArgs...); err != nil {
			return fmt.Errorf("insert graph edge: %w", err)
		}
	}

	return nil
}

func (r *postgresVersionRepository) LoadGraph(ctx context.Context, tenantID, versionID uuid.UUID) ([]domain.WorkflowNode, []domain.WorkflowEdge, error) {
	runner := r.getRunner(ctx)

	nodesSQL, nodesArgs, err := psql.Select("id", "tenant_id", "workflow_version_id", "node_key", "node_type", "config", "position_x", "position_y", "created_at").
		From("workflow_nodes").
		Where(sq.Eq{"tenant_id": tenantID, "workflow_version_id": versionID}).
		ToSql()
	if err != nil {
		return nil, nil, fmt.Errorf("build load nodes sql: %w", err)
	}

	nodesRows, err := runner.Query(ctx, nodesSQL, nodesArgs...)
	if err != nil {
		return nil, nil, fmt.Errorf("query load nodes: %w", err)
	}

	nodes, err := pgx.CollectRows(nodesRows, pgx.RowToStructByName[domain.WorkflowNode])
	if err != nil {
		return nil, nil, fmt.Errorf("collect node rows: %w", err)
	}

	edgesSQL, edgesArgs, err := psql.Select("id", "tenant_id", "workflow_version_id", "from_node_id", "to_node_id", "created_at").
		From("workflow_edges").
		Where(sq.Eq{"tenant_id": tenantID, "workflow_version_id": versionID}).
		ToSql()
	if err != nil {
		return nil, nil, fmt.Errorf("build load edges sql: %w", err)
	}

	edgesRows, err := runner.Query(ctx, edgesSQL, edgesArgs...)
	if err != nil {
		return nil, nil, fmt.Errorf("query load edges: %w", err)
	}

	edges, err := pgx.CollectRows(edgesRows, pgx.RowToStructByName[domain.WorkflowEdge])
	if err != nil {
		return nil, nil, fmt.Errorf("collect edge rows: %w", err)
	}

	return nodes, edges, nil
}
