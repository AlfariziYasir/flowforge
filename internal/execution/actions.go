package execution

// Audit action names for the execution feature, matching the workflow package's
// actions.go convention.
const (
	ActionRunCreated           = "workflow.run.created"
	ActionRunRetryRequested    = "workflow.run.retryRequested"
	ActionRunCanceled          = "workflow.run.canceled"
	ActionRunAnalysisGenerated = "workflow.run.analysisGenerated"
)
