package azure

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	metricsOnce sync.Once

	azureOperationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "imagebuilder_azure_operation_duration_seconds",
		Help:    "Duration of Azure provider operations.",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation", "success"})

	azurePageUploadBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "imagebuilder_azure_page_upload_bytes_total",
		Help: "Total bytes uploaded to Azure Page Blobs.",
	}, []string{"container"})

	azurePageUploadRanges = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "imagebuilder_azure_page_upload_ranges_total",
		Help: "Total Azure Page Blob ranges uploaded.",
	}, []string{"container", "success"})
)

func registerAzureMetrics() {
	metricsOnce.Do(func() {
		prometheus.MustRegister(azureOperationDuration, azurePageUploadBytes, azurePageUploadRanges)
	})
}

func observeAzureOperation(operation string, start time.Time, err error) {
	success := "true"
	if err != nil {
		success = "false"
	}
	azureOperationDuration.WithLabelValues(operation, success).Observe(time.Since(start).Seconds())
}
