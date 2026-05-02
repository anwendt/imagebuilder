package observability

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	registerOnce sync.Once

	BuildDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "imagebuilder_build_duration_seconds",
		Help:    "Duration of VMImage build jobs.",
		Buckets: prometheus.DefBuckets,
	}, []string{"phase", "provider", "format"})

	ProvisionerDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "imagebuilder_provisioner_duration_seconds",
		Help:    "Duration of in-process provisioner steps.",
		Buckets: prometheus.DefBuckets,
	}, []string{"type", "success"})

	UploadDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "imagebuilder_upload_duration_seconds",
		Help:    "Duration of provider upload operations.",
		Buckets: prometheus.DefBuckets,
	}, []string{"provider", "format", "success"})

	RegisterDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "imagebuilder_register_duration_seconds",
		Help:    "Duration of provider image registration operations.",
		Buckets: prometheus.DefBuckets,
	}, []string{"provider", "format", "success"})

	QueueDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "imagebuilder_queue_duration_seconds",
		Help:    "Duration a VMImage waits before a build job is created.",
		Buckets: prometheus.DefBuckets,
	}, []string{"provider", "format"})

	ActiveBuilds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "imagebuilder_active_builds",
		Help: "Number of VMImage build jobs currently tracked as active.",
	}, []string{"provider", "format"})

	FailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "imagebuilder_failures_total",
		Help: "VMImage failures classified by phase, reason, and provider.",
	}, []string{"phase", "reason", "provider"})

	CleanupFailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "imagebuilder_cleanup_failures_total",
		Help: "Cleanup failures classified by cleanup scope, reason, and provider.",
	}, []string{"scope", "reason", "provider"})
)

func Register() {
	registerOnce.Do(func() {
		metrics.Registry.MustRegister(
			BuildDurationSeconds,
			ProvisionerDurationSeconds,
			UploadDurationSeconds,
			RegisterDurationSeconds,
			QueueDurationSeconds,
			ActiveBuilds,
			FailuresTotal,
			CleanupFailuresTotal,
		)
	})
}
