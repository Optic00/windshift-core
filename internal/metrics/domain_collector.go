package metrics

import (
	"context"
	"fmt"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"

	"github.com/prometheus/client_golang/prometheus"
)

const collectionTimeout = 2 * time.Second

var terminalAgentRunStatuses = []string{
	models.AgentRunStatusSucceeded,
	models.AgentRunStatusFailed,
	models.AgentRunStatusCanceled,
	models.AgentRunStatusKilled,
}

type domainCollector struct {
	db database.Database

	agentRunQueueDepth      *prometheus.Desc
	agentRunsInFlight       *prometheus.Desc
	agentRunOutcomes        *prometheus.Desc
	agentRunDurationAverage *prometheus.Desc
	agentRunDurationSamples *prometheus.Desc
	webhookDispatches       *prometheus.Desc
}

func newDomainCollector(db database.Database) prometheus.Collector {
	return &domainCollector{
		db: db,
		agentRunQueueDepth: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "agent", "run_queue_depth"),
			"Current number of queued agent runs.", nil, nil,
		),
		agentRunsInFlight: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "agent", "runs_in_flight"),
			"Current number of running agent runs.", nil, nil,
		),
		agentRunOutcomes: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "agent", "run_outcomes"),
			"Current retained agent runs by terminal outcome.", []string{"outcome"}, nil,
		),
		agentRunDurationAverage: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "agent", "run_duration_average_seconds"),
			"Average duration of retained completed agent runs by outcome.", []string{"outcome"}, nil,
		),
		agentRunDurationSamples: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "agent", "run_duration_samples"),
			"Number of retained completed agent runs included in the duration average.", []string{"outcome"}, nil,
		),
		webhookDispatches: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "webhook", "dispatches"),
			"Current retained webhook deliveries by outcome.", []string{"outcome"}, nil,
		),
	}
}

func (c *domainCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.agentRunQueueDepth
	ch <- c.agentRunsInFlight
	ch <- c.agentRunOutcomes
	ch <- c.agentRunDurationAverage
	ch <- c.agentRunDurationSamples
	ch <- c.webhookDispatches
}

func (c *domainCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectionTimeout)
	defer cancel()

	if err := c.collectAgentRuns(ctx, ch); err != nil {
		ch <- prometheus.NewInvalidMetric(c.agentRunOutcomes, fmt.Errorf("collect agent run metrics: %w", err))
	}
	if err := c.collectWebhookDispatches(ctx, ch); err != nil {
		ch <- prometheus.NewInvalidMetric(c.webhookDispatches, fmt.Errorf("collect webhook metrics: %w", err))
	}
}

func (c *domainCollector) collectAgentRuns(ctx context.Context, ch chan<- prometheus.Metric) error {
	counts := map[string]int64{
		models.AgentRunStatusQueued:    0,
		models.AgentRunStatusRunning:   0,
		models.AgentRunStatusSucceeded: 0,
		models.AgentRunStatusFailed:    0,
		models.AgentRunStatusCanceled:  0,
		models.AgentRunStatusKilled:    0,
	}
	rows, err := c.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM agent_runs GROUP BY status`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			_ = rows.Close()
			return err
		}
		counts[status] = count
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	ch <- prometheus.MustNewConstMetric(c.agentRunQueueDepth, prometheus.GaugeValue, float64(counts[models.AgentRunStatusQueued]))
	ch <- prometheus.MustNewConstMetric(c.agentRunsInFlight, prometheus.GaugeValue, float64(counts[models.AgentRunStatusRunning]))
	for _, status := range terminalAgentRunStatuses {
		ch <- prometheus.MustNewConstMetric(c.agentRunOutcomes, prometheus.GaugeValue, float64(counts[status]), status)
	}

	return c.collectAgentRunDurations(ctx, ch)
}

func (c *domainCollector) collectAgentRunDurations(ctx context.Context, ch chan<- prometheus.Metric) error {
	durationExpression := "(julianday(ended_at) - julianday(started_at)) * 86400.0"
	if database.IsPostgresDriver(c.db.GetDriverName()) {
		durationExpression = "EXTRACT(EPOCH FROM (ended_at - started_at))"
	}
	query := fmt.Sprintf(`
		SELECT status, COUNT(*), COALESCE(SUM(%s), 0)
		FROM agent_runs
		WHERE started_at IS NOT NULL AND ended_at IS NOT NULL
		GROUP BY status
	`, durationExpression)
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var status string
		var count int64
		var durationSum float64
		if err := rows.Scan(&status, &count, &durationSum); err != nil {
			return err
		}
		if count <= 0 || !models.IsAgentRunTerminal(status) {
			continue
		}
		average := durationSum / float64(count)
		if average < 0 {
			average = 0
		}
		ch <- prometheus.MustNewConstMetric(c.agentRunDurationAverage, prometheus.GaugeValue, average, status)
		ch <- prometheus.MustNewConstMetric(c.agentRunDurationSamples, prometheus.GaugeValue, float64(count), status)
	}
	return rows.Err()
}

func (c *domainCollector) collectWebhookDispatches(ctx context.Context, ch chan<- prometheus.Metric) error {
	counts := map[bool]int64{false: 0, true: 0}
	rows, err := c.db.QueryContext(ctx, `SELECT success, COUNT(*) FROM webhook_deliveries GROUP BY success`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var success bool
		var count int64
		if err := rows.Scan(&success, &count); err != nil {
			return err
		}
		counts[success] = count
	}
	if err := rows.Err(); err != nil {
		return err
	}
	ch <- prometheus.MustNewConstMetric(c.webhookDispatches, prometheus.GaugeValue, float64(counts[true]), "success")
	ch <- prometheus.MustNewConstMetric(c.webhookDispatches, prometheus.GaugeValue, float64(counts[false]), "failure")
	return nil
}
