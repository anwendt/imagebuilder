// cmd/operator/main.go
//
// VM Image Builder Operator — entrypoint.
//
// Built-in platform plugins are activated by blank imports below.
// To add a new built-in plugin: add one import line. No other changes needed.
//
// External (gRPC) plugins are activated at runtime via PlatformProvider CRs —
// no code change required.

package main

import (
	"flag"
	"log/slog"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	imagebuilderv1alpha1 "github.com/yourorg/imagebuilder/api/v1alpha1"
	"github.com/yourorg/imagebuilder/pkg/plugin"

	// Built-in platform plugins — each registers itself via init().
	// Comment out any plugin to exclude it from the binary.
	_ "github.com/yourorg/imagebuilder/plugins/aws"
	_ "github.com/yourorg/imagebuilder/plugins/azure"
	_ "github.com/yourorg/imagebuilder/plugins/gcp"
	_ "github.com/yourorg/imagebuilder/plugins/openstack"
	_ "github.com/yourorg/imagebuilder/plugins/vsphere"
)

var (
	scheme = runtime.NewScheme()
	log    = ctrl.Log.WithName("main")
)

func init() {
	_ = imagebuilderv1alpha1.AddToScheme(scheme)
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		leaderElect          bool
		maxConcurrentBuilds  int
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe endpoint address")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for HA deployments")
	flag.IntVar(&maxConcurrentBuilds, "max-concurrent-builds", 3, "Maximum parallel build jobs")
	flag.Parse()

	opts := zap.Options{Development: true}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Log registered plugins
	registry := plugin.Default()
	slog.Info("registered platform plugins", slog.Any("plugins", registry.List()))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "imagebuilder.io",
	})
	if err != nil {
		slog.Error("unable to create manager", slog.Any("error", err))
		os.Exit(1)
	}

	// TODO: register controllers
	// if err = (&controller.VMImageReconciler{
	// 	Client:   mgr.GetClient(),
	// 	Scheme:   mgr.GetScheme(),
	// 	Registry: registry,
	// }).SetupWithManager(mgr); err != nil {
	// 	slog.Error("unable to create VMImage controller", slog.Any("error", err))
	// 	os.Exit(1)
	// }

	// TODO: register PlatformProvider controller
	// TODO: register ProviderConfig controller

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		slog.Error("unable to set up health check", slog.Any("error", err))
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		slog.Error("unable to set up readiness check", slog.Any("error", err))
		os.Exit(1)
	}

	slog.Info("starting VM Image Builder operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		slog.Error("problem running manager", slog.Any("error", err))
		os.Exit(1)
	}
}
