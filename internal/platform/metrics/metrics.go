// Package metrics exposes FlowForge worker telemetry.
//
// Label discipline (decision B-2): counters carry tenant_id — one series each,
// cheap. Histograms do NOT carry tenant_id: a histogram with 10 buckets × N
// node types × M tenants multiplies series by bucket count, a cardinality bomb
// for UUID tenants that Prometheus never reaps. Per-tenant latency belongs in
// the tenant-scoped execution_logs table, not in metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is the set of collectors the worker registers. The label sets are
// asserted by TestMetrics_LabelDiscipline (Q-24) so a later "helpful" addition
// trips a test.
type Metrics struct {
	RunsCreated  *prometheus.CounterVec   // labels: tenant_id
	RunsFinished *prometheus.CounterVec   // labels: tenant_id, status
	Steps        *prometheus.CounterVec   // labels: tenant_id, node_type, status
	Retries      *prometheus.CounterVec   // labels: tenant_id, node_type
	StepDuration *prometheus.HistogramVec // labels: node_type, status (no tenant_id)
	QueueDepth   *prometheus.GaugeVec     // labels: queue
	RunAge       *prometheus.HistogramVec // labels: status (no tenant_id)
}

// New builds the collectors and registers them with reg. The namespace is
// "flowforge".
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		RunsCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "flowforge", Name: "runs_created_total",
			Help: "Workflow runs created, per tenant.",
		}, []string{"tenant_id"}),
		RunsFinished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "flowforge", Name: "runs_finished_total",
			Help: "Workflow runs reaching a terminal status, per tenant and status.",
		}, []string{"tenant_id", "status"}),
		Steps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "flowforge", Name: "steps_total",
			Help: "Step executions by terminal status, per tenant, node type and status.",
		}, []string{"tenant_id", "node_type", "status"}),
		Retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "flowforge", Name: "step_retries_total",
			Help: "Step retries scheduled, per tenant and node type.",
		}, []string{"tenant_id", "node_type"}),
		StepDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "flowforge", Name: "step_duration_seconds",
			Help:    "Step execution duration, per node type and status. Deliberately without tenant_id (B-2).",
			Buckets: prometheus.DefBuckets,
		}, []string{"node_type", "status"}),
		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "flowforge", Name: "queue_depth",
			Help: "Pending tasks per asynq queue.",
		}, []string{"queue"}),
		RunAge: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "flowforge", Name: "run_age_seconds",
			Help:    "Run age at terminal status. Deliberately without tenant_id (B-2).",
			Buckets: prometheus.DefBuckets,
		}, []string{"status"}),
	}
	reg.MustRegister(m.RunsCreated, m.RunsFinished, m.Steps, m.Retries, m.StepDuration, m.QueueDepth, m.RunAge)
	return m
}
