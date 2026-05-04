package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"

	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
	"github.com/anwendt/imagebuilder/plugins/vsphere"
)

func main() {
	var listen string
	flag.StringVar(&listen, "listen", ":9443", "gRPC listen address")
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

	slog.Info("starting vsphere provider", slog.String("address", listen))
	if err := sdk.Serve(context.Background(), listener, vsphere.NewSDKProvider(), serverOptions...); err != nil {
		slog.Error("provider stopped", slog.Any("error", err))
		os.Exit(1)
	}
}
