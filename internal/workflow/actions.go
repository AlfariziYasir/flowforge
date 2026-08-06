package workflow

// Audit action names for workflow mutations, recorded via domain.AuditRepository.
const (
	ActionWorkflowCreated    = "workflow.created"
	ActionWorkflowUpdated    = "workflow.updated"
	ActionWorkflowArchived   = "workflow.archived"
	ActionWorkflowDraftSaved = "workflow.draft_saved"
	ActionWorkflowPublished  = "workflow.published"
	ActionWorkflowRolledBack = "workflow.rolled_back"
)
