package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
	"github.com/anwendt/imagebuilder/plugins/azure"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	var listen string
	var metricsListen string
	flag.StringVar(&listen, "listen", sdk.DefaultListenAddress, "gRPC listen address")
	flag.StringVar(&metricsListen, "metrics-listen", ":8080", "Prometheus metrics listen address; empty disables metrics")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	serverOptions, err := sdk.ServerOptionsFromEnv()
	if err != nil {
		slog.Error("invalid provider TLS configuration", slog.Any("error", err))
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		slog.Error("listen failed", slog.String("address", listen), slog.Any("error", err))
		os.Exit(1)
	}
	if metricsListen != "" {
		go func() {
			mux := http.NewServeMux()
			mux.Handle("/metrics", promhttp.Handler())
			metricsServer := &http.Server{
				Addr:              metricsListen,
				Handler:           mux,
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       10 * time.Second,
				WriteTimeout:      10 * time.Second,
				IdleTimeout:       60 * time.Second,
			}
			slog.Info("starting azure provider metrics", slog.String("address", metricsListen))
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("metrics server stopped", slog.Any("error", err))
			}
		}()
	}

	slog.Info("starting azure provider", slog.String("address", listen))
	if err := sdk.Serve(context.Background(), listener, azure.NewSDKProvider(), serverOptions...); err != nil {
		slog.Error("provider stopped", slog.Any("error", err))
		os.Exit(1)
	}
}
