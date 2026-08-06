package metrics_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"

	"flowforge/internal/platform/metrics"
)

var varLabelsRe = regexp.MustCompile(`variableLabels: \{([^}]*)\}`)

func labelNames(c prometheus.Collector) []string {
	ch := make(chan *prometheus.Desc, 1)
	c.Describe(ch)
	d := <-ch
	matches := varLabelsRe.FindStringSubmatch(d.String())
	if len(matches) < 2 || strings.TrimSpace(matches[1]) == "" {
		return nil
	}
	return strings.Split(matches[1], ",")
}

// Q-24 / B-2: counters carry tenant_id; histograms do not. The label sets are
// pinned here so a later "helpful" addition (e.g. tenant_id on a histogram)
// trips a test instead of quietly exploding series cardinality.
func TestMetrics_LabelDiscipline(t *testing.T) {
	m := metrics.New(prometheus.NewRegistry())

	assert.Equal(t, []string{"tenant_id"}, labelNames(m.RunsCreated), "run counter must be per-tenant")
	assert.Equal(t, []string{"tenant_id", "status"}, labelNames(m.RunsFinished))
	assert.Equal(t, []string{"tenant_id", "node_type", "status"}, labelNames(m.Steps))
	assert.Equal(t, []string{"tenant_id", "node_type"}, labelNames(m.Retries))

	assert.Equal(t, []string{"node_type", "status"}, labelNames(m.StepDuration),
		"step duration histogram must NOT carry tenant_id (B-2)")
	assert.Equal(t, []string{"status"}, labelNames(m.RunAge),
		"run age histogram must NOT carry tenant_id (B-2)")
	assert.Equal(t, []string{"queue"}, labelNames(m.QueueDepth))
}
