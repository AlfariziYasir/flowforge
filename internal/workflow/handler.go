package workflow

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"flowforge/internal/auth"
	"flowforge/internal/domain"
	"flowforge/internal/platform/httpx"
)

type WorkflowHandler struct {
	useCase WorkflowUseCase
}

func NewWorkflowHandler(useCase WorkflowUseCase) *WorkflowHandler {
	return &WorkflowHandler{useCase: useCase}
}

type CreateWorkflowRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type UpdateWorkflowRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	RowVersion  int     `json:"rowVersion"`
}

type ArchiveWorkflowRequest struct {
	RowVersion int `json:"rowVersion"`
}

type SaveDraftRequest struct {
	Graph      domain.Graph `json:"graph"`
	RowVersion int          `json:"rowVersion"`
}

type PublishWorkflowRequest struct {
	RowVersion int `json:"rowVersion"`
}

type RollbackWorkflowRequest struct {
	RowVersion int `json:"rowVersion"`
}

func (h *WorkflowHandler) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	var req CreateWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			httpx.Fail(w, http.StatusRequestEntityTooLarge, httpx.CodeInvalidRequestBody, "request body exceeds maximum allowed size")
			return
		}
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "invalid JSON body")
		return
	}

	wf, ver, err := h.useCase.CreateWorkflow(r.Context(), CreateWorkflowCommand{
		TenantID:    authUser.TenantID,
		ActorID:     authUser.ID,
		Name:        req.Name,
		Description: req.Description,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpx.Created(w, map[string]any{
		"workflow": NewWorkflowDetail(wf),
		"draft":    NewVersionSummary(ver),
	})
}

func (h *WorkflowHandler) Get(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	wf, err := h.useCase.GetWorkflow(r.Context(), authUser.TenantID, wfID)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpx.OK(w, NewWorkflowDetail(wf))
}

func (h *WorkflowHandler) List(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	q := ListWorkflowsQuery{
		TenantID:  authUser.TenantID,
		Status:    r.URL.Query().Get("status"),
		Search:    r.URL.Query().Get("search"),
		SortBy:    r.URL.Query().Get("sortBy"),
		SortOrder: r.URL.Query().Get("sortOrder"),
	}

	if p := r.URL.Query().Get("page"); p != "" {
		if val, err := strconv.Atoi(p); err == nil {
			q.Page = val
		}
	}
	if ps := r.URL.Query().Get("pageSize"); ps != "" {
		if val, err := strconv.Atoi(ps); err == nil {
			q.PageSize = val
		}
	}
	res, err := h.useCase.ListWorkflows(r.Context(), q)
	if err != nil {
		h.handleError(w, err)
		return
	}

	items := make([]WorkflowSummary, len(res.Items))
	for i, item := range res.Items {
		items[i] = NewWorkflowSummary(item)
	}

	httpx.OK(w, httpx.List{
		Items:      items,
		Pagination: httpx.NewPagination(res.Page, res.PageSize, res.TotalItems),
	})
}

func (h *WorkflowHandler) Update(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	var req UpdateWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			httpx.Fail(w, http.StatusRequestEntityTooLarge, httpx.CodeInvalidRequestBody, "request body exceeds maximum allowed size")
			return
		}
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "invalid JSON body")
		return
	}
	if req.RowVersion < 1 {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "rowVersion is required and must be >= 1")
		return
	}

	wf, err := h.useCase.UpdateWorkflow(r.Context(), UpdateWorkflowCommand{
		TenantID:    authUser.TenantID,
		ActorID:     authUser.ID,
		WorkflowID:  wfID,
		Name:        req.Name,
		Description: req.Description,
		RowVersion:  req.RowVersion,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpx.OK(w, NewWorkflowUpdatedResponse(wf))
}

func (h *WorkflowHandler) Archive(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	var req ArchiveWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			httpx.Fail(w, http.StatusRequestEntityTooLarge, httpx.CodeInvalidRequestBody, "request body exceeds maximum allowed size")
			return
		}
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "invalid JSON body")
		return
	}
	if req.RowVersion < 1 {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "rowVersion is required and must be >= 1")
		return
	}

	_, err = h.useCase.ArchiveWorkflow(r.Context(), ArchiveWorkflowCommand{
		TenantID:   authUser.TenantID,
		ActorID:    authUser.ID,
		WorkflowID: wfID,
		RowVersion: req.RowVersion,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpx.NoContent(w)
}

func (h *WorkflowHandler) SaveDraft(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	var req SaveDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			httpx.Fail(w, http.StatusRequestEntityTooLarge, httpx.CodeInvalidRequestBody, "request body exceeds maximum allowed size")
			return
		}
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "invalid JSON body")
		return
	}

	req.Graph.Normalize()

	draftVer, err := h.useCase.SaveDraft(r.Context(), SaveDraftCommand{
		TenantID:   authUser.TenantID,
		ActorID:    authUser.ID,
		WorkflowID: wfID,
		Graph:      req.Graph,
		RowVersion: req.RowVersion,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpx.OK(w, draftVer)
}

func (h *WorkflowHandler) Publish(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	var req PublishWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			httpx.Fail(w, http.StatusRequestEntityTooLarge, httpx.CodeInvalidRequestBody, "request body exceeds maximum allowed size")
			return
		}
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "invalid JSON body")
		return
	}
	if req.RowVersion < 1 {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "rowVersion is required and must be >= 1")
		return
	}

	res, err := h.useCase.PublishVersion(r.Context(), PublishCommand{
		TenantID:   authUser.TenantID,
		ActorID:    authUser.ID,
		WorkflowID: wfID,
		RowVersion: req.RowVersion,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpx.OK(w, res)
}

func (h *WorkflowHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	verID, err := uuid.Parse(r.PathValue("versionId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid version ID format")
		return
	}

	var req RollbackWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			httpx.Fail(w, http.StatusRequestEntityTooLarge, httpx.CodeInvalidRequestBody, "request body exceeds maximum allowed size")
			return
		}
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "invalid JSON body")
		return
	}
	if req.RowVersion < 1 {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "rowVersion is required and must be >= 1")
		return
	}

	res, err := h.useCase.RollbackVersion(r.Context(), RollbackCommand{
		TenantID:   authUser.TenantID,
		ActorID:    authUser.ID,
		WorkflowID: wfID,
		VersionID:  verID,
		RowVersion: req.RowVersion,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpx.OK(w, res)
}

func (h *WorkflowHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	versions, err := h.useCase.ListVersions(r.Context(), authUser.TenantID, wfID)
	if err != nil {
		h.handleError(w, err)
		return
	}

	summaries := make([]VersionSummary, len(versions))
	for i, v := range versions {
		summaries[i] = NewVersionSummary(v)
	}
	httpx.OK(w, summaries)
}

func (h *WorkflowHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	verID, err := uuid.Parse(r.PathValue("versionId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid version ID format")
		return
	}

	detail, err := h.useCase.GetVersion(r.Context(), authUser.TenantID, wfID, verID)
	if err != nil {
		h.handleError(w, err)
		return
	}

	httpx.OK(w, detail)
}

func (h *WorkflowHandler) handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNameRequired):
		httpx.Fail(w, http.StatusUnprocessableEntity, httpx.CodeWorkflowNameRequired, "workflow name is required and cannot be empty")
	case errors.Is(err, ErrWorkflowNotFound):
		httpx.Fail(w, http.StatusNotFound, httpx.CodeWorkflowNotFound, "requested workflow was not found")
	case errors.Is(err, ErrWorkflowAlreadyExists):
		httpx.Fail(w, http.StatusConflict, httpx.CodeWorkflowAlreadyExists, "a workflow with this name already exists in tenant")
	case errors.Is(err, ErrWorkflowArchived):
		httpx.Fail(w, http.StatusConflict, httpx.CodeWorkflowVersionConflict, "cannot modify an archived workflow")
	case errors.Is(err, ErrVersionNotFound):
		httpx.Fail(w, http.StatusNotFound, httpx.CodeWorkflowVersionNotFound, "requested workflow version was not found")
	case errors.Is(err, ErrVersionConflict):
		httpx.Fail(w, http.StatusConflict, httpx.CodeWorkflowVersionConflict, "workflow was modified by another request; refetch and retry")
	case errors.Is(err, ErrVersionImmutable):
		httpx.Fail(w, http.StatusConflict, httpx.CodeWorkflowVersionImmutable, "published versions cannot be modified")
	case errors.Is(err, domain.ErrCycleDetected):
		httpx.FailWithDetails(w, http.StatusUnprocessableEntity, httpx.CodeWorkflowCycleDetected, "workflow graph contains a cycle", map[string]any{"reason": err.Error()})
	case errors.Is(err, domain.ErrInvalidDAG):
		httpx.FailWithDetails(w, http.StatusUnprocessableEntity, httpx.CodeWorkflowInvalidDAG, "workflow graph structure is invalid", map[string]any{"reason": err.Error()})
	case errors.Is(err, ErrInvalidSortField):
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidQueryParam, "invalid sort field parameter")
	default:
		httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternalServerError, "internal server error")
	}
}
