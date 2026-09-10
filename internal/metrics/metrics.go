// Package metrics holds transcoder-service business metric counters.
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	Processed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "transcoder_processed_total",
		Help: "Total number of videos processed by the transcoder.",
	})

	Errors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "transcoder_errors_total",
		Help: "Total number of failed transcoding tasks.",
	})

	DiskFull = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "transcoder_disk_full_total",
		Help: "Total number of aborted tasks due to insufficient disk space.",
	})

	Aborted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "transcoder_aborted_total",
		Help: "Total number of transcoding tasks aborted by context cancellation.",
	})

	Duration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "transcoder_duration_seconds",
		Help:    "Transcoding duration in seconds.",
		Buckets: prometheus.DefBuckets,
	})
)

func init() {
	prometheus.MustRegister(Processed, Errors, DiskFull, Aborted, Duration)
}
