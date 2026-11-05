package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Total number of feed jobs executed
	feedJobCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "oc3",
			Name:      "feed_job_executions_total",
			Help: fmt.Sprintf("Total number of feed job executions (job_type={%s}, status={%s})",
				strings.Join([]string{jtDaemonPing, jtDaemonStatus, jtNodeSystem, jtInstanceAction, jtInstanceResourceInfo, jtInstanceStatus, jtNodeChecks, jtNodeDisk, jtObjectConfig, jtSANSwitch, jtArray}, "|"),
				strings.Join([]string{jobStatusFailed, jobStatusOk}, "|")),
		},
		[]string{"job_type", "status"},
	)

	// Duration of entire feed jobs
	feedJobDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "oc3",
			Name:      "feed_job_duration_seconds",
			Help: fmt.Sprintf("Duration of entire feed job executions in seconds (job_type={%s}, status={%s})",
				strings.Join([]string{jtDaemonPing, jtDaemonStatus, jtNodeSystem, jtInstanceAction, jtInstanceResourceInfo, jtInstanceStatus, jtNodeChecks, jtNodeDisk, jtObjectConfig, jtSANSwitch, jtArray}, "|"),
				strings.Join([]string{jobStatusFailed, jobStatusOk}, "|")),
			Buckets: prometheus.DefBuckets,
		},
		[]string{"job_type", "status"},
	)

	// Duration of individual steps within jobs
	feedJobStepDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "oc3",
			Name:      "feed_job_step_duration_seconds",
			Help: fmt.Sprintf("Duration of individual steps within feed jobs in seconds (job_type={%s}, status={%s}, job_step={main|...}})",
				strings.Join([]string{jtDaemonPing, jtDaemonStatus, jtNodeSystem, jtInstanceAction, jtInstanceResourceInfo, jtInstanceStatus, jtNodeChecks, jtNodeDisk, jtObjectConfig, jtSANSwitch, jtArray}, "|"),
				strings.Join([]string{jobStatusFailed, jobStatusOk}, "|")),
			Buckets: prometheus.DefBuckets,
		},
		[]string{"job_type", "job_step", "status"},
	)
)

var (
	// Number of feed jobs running, to compare with feedJobRunners
	feedJobRunning = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "oc3",
			Name:      "feed_jobs_running",
			Help:      "Number of feed jobs running",
		},
	)

	// Maximum number of feed jobs running in parallel
	feedJobRunners = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "oc3",
			Name:      "feed_job_runners",
			Help:      "Maximum number of feed jobs running in parallel",
		},
	)

	feedQueueLengthDesc = prometheus.NewDesc(
		"oc3_feed_queue_length",
		"Number of jobs waiting in a feed queue",
		[]string{"queue"}, nil,
	)
)

// queueCollector reports the length of the worker queues, read from redis
// when the metrics are scraped.
type queueCollector struct {
	redis  *redis.Client
	queues []string
}

func (c *queueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- feedQueueLengthDesc
}

func (c *queueCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, q := range c.queues {
		n, err := c.redis.LLen(ctx, q).Result()
		if err != nil {
			// skip the queue: an invalid metric would fail the whole scrape
			slog.Warn(fmt.Sprintf("feed queue metrics: llen %s: %s", q, err))
			continue
		}
		ch <- prometheus.MustNewConstMetric(feedQueueLengthDesc, prometheus.GaugeValue, float64(n), q)
	}
}

// registerQueueMetrics exposes the length of the worker queues.
func registerQueueMetrics(r *redis.Client, queues []string) {
	if err := prometheus.Register(&queueCollector{redis: r, queues: queues}); err != nil {
		slog.Warn(fmt.Sprintf("feed queue metrics: %s", err))
	}
}
