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

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	imagebuilderv1alpha1 "github.com/anwendt/imagebuilder/api/v1alpha1"
	providercontroller "github.com/anwendt/imagebuilder/pkg/controller/provider"
	vmimagecontroller "github.com/anwendt/imagebuilder/pkg/controller/vmimage"
	"github.com/anwendt/imagebuilder/pkg/observability"
	"github.com/anwendt/imagebuilder/pkg/plugin"

	// Built-in platform plugins — each registers itself via init().
	// Comment out any plugin to exclude it from the binary.
	_ "github.com/anwendt/imagebuilder/plugins/aws"
	_ "github.com/anwendt/imagebuilder/plugins/azure"
	_ "github.com/anwendt/imagebuilder/plugins/gcp"
	_ "github.com/anwendt/imagebuilder/plugins/openstack"
	_ "github.com/anwendt/imagebuilder/plugins/vsphere"
)

var (
	scheme = runtime.NewScheme()
	log    = ctrl.Log.WithName("main")
)

func init() {
	_ = imagebuilderv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = coordinationv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
}

func main() {
	var (
		metricsAddr                string
		probeAddr                  string
		leaderElect                bool
		maxConcurrentBuilds        int
		maxConcurrentBuildsPerNode int
		schedulerNamespace         string
		logLevel                   string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe endpoint address")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for HA deployments")
	flag.IntVar(&maxConcurrentBuilds, "max-concurrent-builds", 3, "Maximum parallel build jobs")
	flag.IntVar(&maxConcurrentBuildsPerNode, "max-concurrent-builds-per-node", 1, "Maximum parallel build jobs per node selector")
	flag.StringVar(&schedulerNamespace, "scheduler-namespace", os.Getenv("POD_NAMESPACE"), "Namespace used for build slot Leases; defaults to each VMImage namespace when empty")
	// OR-012: log level must be configurable at runtime without redeployment.
	flag.StringVar(&logLevel, "log-level", "info", "Log level: debug, info, warn, error")
	flag.Parse()

	var slogLevel slog.Level
	if err := slogLevel.UnmarshalText([]byte(logLevel)); err != nil {
		slogLevel = slog.LevelInfo
	}
	// TF-031/TF-032: structured JSON logs to stderr; collection is the cluster's concern.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel})))

	opts := zap.Options{Development: slogLevel == slog.LevelDebug}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	observability.Register()

	// Log registered plugins
	registry := plugin.Default()
	slog.Info("registered platform plugins", slog.Any("plugins", registry.List()))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "imagebuilder.io",
	})
	if err != nil {
		slog.Error("unable to create manager", slog.Any("error", err))
		os.Exit(1)
	}

	if err = (&vmimagecontroller.VMImageReconciler{
		Client:                     mgr.GetClient(),
		Scheme:                     mgr.GetScheme(),
		Registry:                   registry,
		MaxConcurrentBuilds:        maxConcurrentBuilds,
		MaxConcurrentBuildsPerNode: maxConcurrentBuildsPerNode,
		SchedulerNamespace:         schedulerNamespace,
	}).SetupWithManager(mgr); err != nil {
		slog.Error("unable to create VMImage controller", slog.Any("error", err))
		os.Exit(1)
	}

	if err = (&providercontroller.PlatformProviderReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Registry: registry,
	}).SetupWithManager(mgr); err != nil {
		slog.Error("unable to create PlatformProvider controller", slog.Any("error", err))
		os.Exit(1)
	}

	if err = (&imagebuilderv1alpha1.VMImage{}).SetupWebhookWithManager(mgr); err != nil {
		slog.Error("unable to create VMImage webhook", slog.Any("error", err))
		os.Exit(1)
	}
	if err = (&imagebuilderv1alpha1.ProviderConfig{}).SetupWebhookWithManager(mgr); err != nil {
		slog.Error("unable to create ProviderConfig webhook", slog.Any("error", err))
		os.Exit(1)
	}

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
