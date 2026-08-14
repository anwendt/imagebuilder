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
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	imagebuilderv1alpha1 "github.com/anwendt/imagebuilder/api/v1alpha1"
	providercontroller "github.com/anwendt/imagebuilder/pkg/controller/provider"
	vmimagecontroller "github.com/anwendt/imagebuilder/pkg/controller/vmimage"
	"github.com/anwendt/imagebuilder/pkg/kubecompat"
	"github.com/anwendt/imagebuilder/pkg/observability"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/security/signaturepolicy"

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
)

func init() {
	_ = imagebuilderv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = coordinationv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = admissionregistrationv1.AddToScheme(scheme)
}

func main() {
	var (
		metricsAddr                       string
		probeAddr                         string
		leaderElect                       bool
		maxConcurrentBuilds               int
		deprecatedMaxPerNode              int
		schedulerNamespace                string
		providerNamespace                 string
		requireProviderMTLS               bool
		requireProviderDigest             bool
		requireProviderSignature          bool
		providerSignaturePolicy           string
		allowedProviderRegistries         string
		restrictProviderServiceAccounts   bool
		allowedProviderServiceAccounts    string
		forbidProviderServiceAccountToken bool
		logLevel                          string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe endpoint address")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for HA deployments")
	flag.IntVar(&maxConcurrentBuilds, "max-concurrent-builds", 3, "Maximum parallel build jobs")
	flag.IntVar(&deprecatedMaxPerNode, "max-concurrent-builds-per-node", 1, "Deprecated and ignored; kube-scheduler owns node placement")
	flag.StringVar(&schedulerNamespace, "scheduler-namespace", os.Getenv("POD_NAMESPACE"), "Namespace used for global build slot Leases; defaults to each VMImage namespace when empty")
	flag.StringVar(&providerNamespace, "provider-namespace", os.Getenv("POD_NAMESPACE"), "Namespace used for PlatformProvider Deployments and Services")
	flag.BoolVar(&requireProviderMTLS, "require-provider-mtls", false, "Require all PlatformProvider resources to use spec.transport.tls.mode=Mutual")
	flag.BoolVar(&requireProviderDigest, "require-provider-digest", false, "Require all PlatformProvider package references to be digest-pinned")
	flag.BoolVar(&requireProviderSignature, "require-provider-signature", false, "Require all PlatformProvider resources to enable and pass cryptographic image signature verification")
	flag.StringVar(&providerSignaturePolicy, "provider-signature-policy", "", "Name of the enforcing Kyverno ClusterPolicy used for provider signature verification")
	flag.StringVar(&allowedProviderRegistries, "allowed-provider-registries", "", "Comma-separated registry prefixes allowed for PlatformProvider packages")
	flag.BoolVar(&restrictProviderServiceAccounts, "restrict-provider-serviceaccounts", false, "Reject custom PlatformProvider ServiceAccounts unless explicitly allowlisted")
	flag.StringVar(&allowedProviderServiceAccounts, "allowed-provider-serviceaccounts", "", "Comma-separated ServiceAccount names permitted for PlatformProvider pods")
	flag.BoolVar(&forbidProviderServiceAccountToken, "forbid-provider-serviceaccount-token", false, "Forbid PlatformProvider pods from automounting Kubernetes API tokens")
	// OR-012: log level must be configurable at runtime without redeployment.
	flag.StringVar(&logLevel, "log-level", "info", "Log level: debug, info, warn, error")
	flag.Parse()
	_ = deprecatedMaxPerNode
	allowedProviderRegistryList := splitCSV(allowedProviderRegistries)
	allowedProviderServiceAccountList := splitCSV(allowedProviderServiceAccounts)
	imagebuilderv1alpha1.SetPlatformProviderAdmissionPolicy(imagebuilderv1alpha1.PlatformProviderAdmissionPolicy{
		RequireMTLS:               requireProviderMTLS,
		RequireDigest:             requireProviderDigest,
		RequireSignature:          requireProviderSignature,
		AllowedRegistries:         allowedProviderRegistryList,
		ProviderNamespace:         providerNamespace,
		RestrictServiceAccounts:   restrictProviderServiceAccounts,
		AllowedServiceAccounts:    allowedProviderServiceAccountList,
		ForbidServiceAccountToken: forbidProviderServiceAccountToken,
	})

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

	restConfig := ctrl.GetConfigOrDie()
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		slog.Error("unable to create Kubernetes discovery client for compatibility check", slog.Any("error", err))
		os.Exit(1)
	}
	if err := kubecompat.CheckServer(discoveryClient); err != nil {
		slog.Error("Kubernetes compatibility check failed", slog.Any("error", err))
		os.Exit(1)
	}
	slog.Info("Kubernetes compatibility check passed", slog.String("minimumVersion", kubecompat.MinimumKubernetesVersion))

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
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
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		Registry:            registry,
		MaxConcurrentBuilds: maxConcurrentBuilds,
		SchedulerNamespace:  schedulerNamespace,
		ProviderNamespace:   providerNamespace,
	}).SetupWithManager(mgr); err != nil {
		slog.Error("unable to create VMImage controller", slog.Any("error", err))
		os.Exit(1)
	}

	if err = (&providercontroller.PlatformProviderReconciler{
		Client:                    mgr.GetClient(),
		Scheme:                    mgr.GetScheme(),
		Registry:                  registry,
		ProviderNamespace:         providerNamespace,
		RequireMTLS:               requireProviderMTLS,
		RequireDigest:             requireProviderDigest,
		RequireSignature:          requireProviderSignature,
		AllowedRegistries:         allowedProviderRegistryList,
		RestrictServiceAccounts:   restrictProviderServiceAccounts,
		AllowedServiceAccounts:    allowedProviderServiceAccountList,
		ForbidServiceAccountToken: forbidProviderServiceAccountToken,
		SignatureVerifier: &signaturepolicy.Verifier{
			Client: mgr.GetClient(),
			Config: signaturepolicy.Config{
				PolicyName:        providerSignaturePolicy,
				ProviderNamespace: providerNamespace,
			},
		},
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
	if err = (&imagebuilderv1alpha1.PlatformProvider{}).SetupWebhookWithManager(mgr); err != nil {
		slog.Error("unable to create PlatformProvider webhook", slog.Any("error", err))
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

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}
