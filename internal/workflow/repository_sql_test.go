package workflow

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/platform/postgres"
)

// T-34: buildUpdateMetadataSQL includes tenant_id, id, and row_version predicates and bumps row_version.
func TestBuildUpdateMetadataSQL(t *testing.T) {
	tenantID := uuid.New()
	wfID := uuid.New()
	wf := &domain.Workflow{
		ID:          wfID,
		TenantID:    tenantID,
		Name:        "New Workflow Name",
		Description: "New Description",
	}

	sqlStr, args, err := buildUpdateMetadataSQL(wf, 3)
	require.NoError(t, err)

	assert.Contains(t, sqlStr, "UPDATE workflows SET")
	assert.Contains(t, sqlStr, "name = $1")
	assert.Contains(t, sqlStr, "description = $2")
	assert.Contains(t, sqlStr, "row_version = row_version + 1")
	assert.Contains(t, sqlStr, "updated_at = NOW()")
	assert.Contains(t, sqlStr, "WHERE")
	assert.Contains(t, sqlStr, "id = $3")
	assert.Contains(t, sqlStr, "row_version = $4")
	assert.Contains(t, sqlStr, "tenant_id = $5")

	assert.Equal(t, []any{"New Workflow Name", "New Description", wfID.String(), 3, tenantID.String()}, args)
}

// T-36: buildSetCurrentVersionSQL and buildUpdateStatusSQL carry row_version predicate.
func TestBuildSetCurrentVersionAndStatusSQL(t *testing.T) {
	tenantID := uuid.New()
	wfID := uuid.New()
	verID := uuid.New()

	t.Run("buildSetCurrentVersionSQL", func(t *testing.T) {
		sqlStr, args, err := buildSetCurrentVersionSQL(tenantID, wfID, verID, 2, 1)
		require.NoError(t, err)

		assert.Contains(t, sqlStr, "UPDATE workflows SET")
		assert.Contains(t, sqlStr, "current_version_id = $1")
		assert.Contains(t, sqlStr, "current_version_number = $2")
		assert.Contains(t, sqlStr, "status = $3")
		assert.Contains(t, sqlStr, "row_version = row_version + 1")
		assert.Contains(t, sqlStr, "WHERE id = $4 AND row_version = $5 AND tenant_id = $6")

		assert.Equal(t, []any{verID, 2, domain.WorkflowStatusPublished, wfID.String(), 1, tenantID.String()}, args)
	})

	t.Run("buildUpdateStatusSQL", func(t *testing.T) {
		sqlStr, args, err := buildUpdateStatusSQL(tenantID, wfID, domain.WorkflowStatusArchived, 5)
		require.NoError(t, err)

		assert.Contains(t, sqlStr, "UPDATE workflows SET")
		assert.Contains(t, sqlStr, "status = $1")
		assert.Contains(t, sqlStr, "row_version = row_version + 1")
		assert.Contains(t, sqlStr, "WHERE id = $2 AND row_version = $3 AND tenant_id = $4")

		assert.Equal(t, []any{domain.WorkflowStatusArchived, wfID.String(), 5, tenantID.String()}, args)
	})
}

// T-37: buildMaxVersionSQL COALESCE MAX version_number.
func TestBuildMaxVersionSQL(t *testing.T) {
	tenantID := uuid.New()
	wfID := uuid.New()

	sqlStr, args, err := buildMaxVersionSQL(tenantID, wfID)
	require.NoError(t, err)

	assert.Equal(t, "SELECT COALESCE(MAX(version_number), 0) FROM workflow_versions WHERE tenant_id = $1 AND workflow_id = $2", sqlStr)
	assert.Equal(t, []any{tenantID.String(), wfID.String()}, args)
}

// T-37b: buildTouchRowVersionSQL carries tenant_id, id and expected row_version.
func TestBuildTouchRowVersionSQL(t *testing.T) {
	tenantID := uuid.New()
	wfID := uuid.New()

	sqlStr, args, err := buildTouchRowVersionSQL(tenantID, wfID, 5)
	require.NoError(t, err)

	assert.Contains(t, sqlStr, "UPDATE workflows SET")
	assert.Contains(t, sqlStr, "row_version = row_version + 1")
	assert.Contains(t, sqlStr, "WHERE id = $1 AND row_version = $2 AND tenant_id = $3")
	assert.Equal(t, []any{wfID.String(), 5, tenantID.String()}, args)
}

// T-35: buildListWorkflowsParams asserts filter properties (tenant_id present, exclude status default, search mapping).
func TestBuildListWorkflowsParams(t *testing.T) {
	tenantID := uuid.New()

	t.Run("default filters exclude archived workflows and require tenant_id", func(t *testing.T) {
		f := ListWorkflowsFilter{
			TenantID:      tenantID,
			Page:          1,
			PageSize:      20,
			OrderByColumn: "updated_at",
			OrderAsc:      false,
			ExcludeStatus: domain.WorkflowStatusArchived,
		}

		params := buildListWorkflowsParams(f)
		assert.Equal(t, tenantID, params.Filters["tenant_id"])
		assert.Equal(t, domain.WorkflowStatusArchived, params.Exclude["status"])
		assert.Nil(t, params.Search)
		assert.Equal(t, "updated_at DESC", params.OrderBy)
	})

	t.Run("maps search query to name column", func(t *testing.T) {
		f := ListWorkflowsFilter{
			TenantID:      tenantID,
			Search:        "my_workflow",
			OrderByColumn: "name",
			OrderAsc:      true,
		}

		params := buildListWorkflowsParams(f)
		assert.Equal(t, map[string]string{"name": "my_workflow"}, params.Search)
		assert.Equal(t, "name ASC", params.OrderBy)
	})
}

type recordingDBTX struct {
	postgres.DBTX
	queries []string
}

func (r *recordingDBTX) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	r.queries = append(r.queries, sql)
	return pgconn.NewCommandTag("EXEC 1"), nil
}

func (r *recordingDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.queries = append(r.queries, sql)
	return nil, nil
}

func (r *recordingDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	r.queries = append(r.queries, sql)
	return nil
}

// T-38: ReplaceGraph statement order is delete-edges -> delete-nodes -> insert-nodes -> insert-edges.
func TestReplaceGraph_StatementExecutionOrder(t *testing.T) {
	tenantID := uuid.New()
	verID := uuid.New()

	recorder := &recordingDBTX{}
	ctx := postgres.ContextWithDBTX(context.Background(), recorder)

	verRepo := NewVersionRepository(nil)

	node1 := domain.WorkflowNode{ID: uuid.New(), TenantID: tenantID, WorkflowVersionID: verID, NodeKey: "start", NodeType: domain.NodeTypeHTTP}
	edge1 := domain.WorkflowEdge{ID: uuid.New(), TenantID: tenantID, WorkflowVersionID: verID, FromNodeID: node1.ID, ToNodeID: node1.ID}

	err := verRepo.ReplaceGraph(ctx, tenantID, verID, []domain.WorkflowNode{node1}, []domain.WorkflowEdge{edge1})
	require.NoError(t, err)

	require.Len(t, recorder.queries, 4)
	assert.Contains(t, recorder.queries[0], "DELETE FROM workflow_edges")
	assert.Contains(t, recorder.queries[1], "DELETE FROM workflow_nodes")
	assert.Contains(t, recorder.queries[2], "INSERT INTO workflow_nodes")
	assert.Contains(t, recorder.queries[3], "INSERT INTO workflow_edges")
}
