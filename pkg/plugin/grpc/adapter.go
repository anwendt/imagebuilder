// pkg/plugin/grpc/adapter.go
//
// GRPCAdapter wraps an external gRPC provider connection and implements the
// platform.Plugin interface so it can be registered in the plugin.Registry
// exactly like a built-in in-process plugin.
//
// ADR-002: external providers run as separate Kubernetes Deployments and
// communicate over gRPC (TCP). The operator creates this adapter once the
// PlatformProvider Deployment is Ready and the capability handshake succeeds.
//
// Connection lifecycle:
//   1. PlatformProviderReconciler detects Deployment.ReadyReplicas > 0.
//   2. Calls grpc.NewAdapter() with the provider's ClusterIP service address.
//   3. Calls adapter.Connect() which dials and calls GetCapabilities.
//   4. Registers the adapter in the plugin.Registry.
//   5. On PlatformProvider deletion: calls adapter.Close() and deregisters.

package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	providerv1 "github.com/anwendt/imagebuilder/api/provider/v1"
	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

const (
	connectTimeout     = 10 * time.Second
	uploadChunkSize    = 4 * 1024 * 1024 // 4 MiB per chunk
	healthCheckTimeout = 5 * time.Second
)

// Adapter implements platform.Plugin by forwarding calls to an external
// gRPC provider. One Adapter exists per healthy PlatformProvider CR.
type Adapter struct {
	address      string // host:port of the provider gRPC service
	tlsConfig    *ProviderTLSConfig
	conn         *grpc.ClientConn
	client       providerv1.PlatformProviderClient
	capabilities *providerv1.CapabilitiesResponse
	log          *slog.Logger
}

type ProviderTLSConfig struct {
	ServerName string
	CABundle   []byte
	ClientCert []byte
	ClientKey  []byte
}

// NewAdapter creates an adapter for the provider at the given address.
// Call Connect() to establish the gRPC connection.
func NewAdapter(address string) *Adapter {
	return NewAdapterWithTLS(address, nil)
}

func NewAdapterWithTLS(address string, tlsConfig *ProviderTLSConfig) *Adapter {
	return &Adapter{
		address:   address,
		tlsConfig: tlsConfig,
		log:       slog.Default().With(slog.String("component", "grpc-adapter"), slog.String("address", address)),
	}
}

// Connect dials the provider, performs the capability handshake, and caches
// the capabilities. Returns an error if the provider is unreachable or
// returns an incompatible protocol version.
func (a *Adapter) Connect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	transport, err := a.transportCredentials()
	if err != nil {
		return err
	}
	//lint:ignore SA1019 DialContext is retained until grpc.NewClient has equivalent blocking dial semantics for this adapter.
	conn, err := grpc.DialContext(ctx, a.address,
		grpc.WithTransportCredentials(transport),
		//lint:ignore SA1019 WithBlock is tied to DialContext blocking semantics.
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("grpc dial %s: %w", a.address, err)
	}

	a.conn = conn
	a.client = providerv1.NewPlatformProviderClient(conn)

	caps, err := a.client.GetCapabilities(ctx, &providerv1.Empty{})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("GetCapabilities from %s: %w", a.address, err)
	}
	if caps.ProtocolVersion != "v1" {
		_ = conn.Close()
		return fmt.Errorf("provider at %s uses protocol %q, operator requires v1", a.address, caps.ProtocolVersion)
	}

	a.capabilities = caps
	a.log.Info("connected to gRPC provider",
		slog.String("provider", caps.ProviderName),
		slog.String("version", caps.ProviderVersion),
		slog.Any("formats", caps.Formats),
		slog.Any("os", caps.OsFamilies),
	)
	return nil
}

func (a *Adapter) transportCredentials() (credentials.TransportCredentials, error) {
	if a.tlsConfig == nil {
		return insecure.NewCredentials(), nil
	}
	if len(a.tlsConfig.CABundle) == 0 {
		return nil, fmt.Errorf("provider mTLS requires CA bundle")
	}
	if len(a.tlsConfig.ClientCert) == 0 || len(a.tlsConfig.ClientKey) == 0 {
		return nil, fmt.Errorf("provider mTLS requires client certificate and key")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(a.tlsConfig.CABundle) {
		return nil, fmt.Errorf("provider mTLS CA bundle contains no certificates")
	}
	cert, err := tls.X509KeyPair(a.tlsConfig.ClientCert, a.tlsConfig.ClientKey)
	if err != nil {
		return nil, fmt.Errorf("load provider mTLS client certificate: %w", err)
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		ServerName:   a.tlsConfig.ServerName,
		RootCAs:      roots,
		Certificates: []tls.Certificate{cert},
	}), nil
}

// Close terminates the gRPC connection.
func (a *Adapter) Close() error {
	if a.conn == nil {
		return nil
	}
	return a.conn.Close()
}

// ---------------------------------------------------------------------------
// platform.Plugin identity
// ---------------------------------------------------------------------------

func (a *Adapter) Name() string {
	if a.capabilities != nil {
		return a.capabilities.ProviderName
	}
	return a.address
}

func (a *Adapter) Version() string {
	if a.capabilities != nil {
		return a.capabilities.ProviderVersion
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// platform.Plugin capabilities
// ---------------------------------------------------------------------------

func (a *Adapter) SupportedFormats() []platform.ImageFormat {
	if a.capabilities == nil {
		return nil
	}
	formats := make([]platform.ImageFormat, 0, len(a.capabilities.Formats))
	for _, f := range a.capabilities.Formats {
		formats = append(formats, platform.ImageFormat(f))
	}
	return formats
}

func (a *Adapter) SupportedOS() []platform.OSFamily {
	if a.capabilities == nil {
		return nil
	}
	families := make([]platform.OSFamily, 0, len(a.capabilities.OsFamilies))
	for _, f := range a.capabilities.OsFamilies {
		families = append(families, platform.OSFamily(f))
	}
	return families
}

func (a *Adapter) SupportedBuildModes() []string {
	if a.capabilities == nil || len(a.capabilities.BuildModes) == 0 {
		return []string{v1alpha1.BuildModeLocal}
	}
	return append([]string(nil), a.capabilities.BuildModes...)
}

// ---------------------------------------------------------------------------
// platform.Plugin lifecycle
// ---------------------------------------------------------------------------

// Init sends the ProviderConfig credentials to the provider for validation.
// For gRPC providers, Init is called after Connect() and delegates to ValidateConfig.
func (a *Adapter) Init(ctx context.Context, cfg platform.PluginConfig) error {
	creds := make(map[string][]byte, len(cfg.SecretData))
	for k, v := range cfg.SecretData {
		creds[k] = v
	}
	req := &providerv1.ValidateConfigRequest{
		ProviderConfigName: cfg.ProviderConfigName,
		Credentials:        creds,
		Region:             cfg.Region,
		Endpoint:           cfg.Endpoint,
		Insecure:           cfg.Insecure,
		Extra:              cfg.Extra,
	}
	resp, err := a.client.ValidateConfig(ctx, req)
	if err != nil {
		return fmt.Errorf("gRPC ValidateConfig: %w", err)
	}
	if !resp.Valid {
		return fmt.Errorf("provider rejected config: %s", resp.Message)
	}
	return nil
}

// Validate delegates to the gRPC provider's ValidateConfig with only format info.
func (a *Adapter) Validate(ctx context.Context, spec v1alpha1.TargetSpec) error {
	req := &providerv1.ValidateConfigRequest{
		ProviderConfigName: spec.ProviderConfigRef.Name,
	}
	resp, err := a.client.ValidateConfig(ctx, req)
	if err != nil {
		return fmt.Errorf("gRPC ValidateConfig: %w", err)
	}
	if !resp.Valid {
		return fmt.Errorf("provider validation failed: %s", resp.Message)
	}
	return nil
}

// ---------------------------------------------------------------------------
// platform.Plugin core operations
// ---------------------------------------------------------------------------

// Upload streams the artifact file to the gRPC provider using client streaming.
// The provider is responsible for uploading to the target platform.
func (a *Adapter) Upload(ctx context.Context, artifact *platform.BuildArtifact) (*platform.UploadResult, error) {
	stream, err := a.client.UploadArtifact(ctx)
	if err != nil {
		return nil, fmt.Errorf("gRPC UploadArtifact open stream: %w", err)
	}

	// Read the artifact file and stream in chunks.
	// In production the artifact.Path points to a file in the build workspace.
	// Here we use a helper that callers can override for testing.
	if err := streamArtifact(stream, artifact); err != nil {
		return nil, fmt.Errorf("stream artifact: %w", err)
	}

	// Read progress responses from the server stream until phase="done".
	var providerRef string
	var lastMetadata map[string]string
	for {
		progress, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gRPC UploadArtifact recv: %w", err)
		}
		a.log.Info("upload progress",
			slog.String("phase", progress.Phase),
			slog.Int64("written", progress.BytesWritten),
			slog.Int64("total", progress.TotalBytes),
		)
		if progress.ProviderRef != "" {
			providerRef = progress.ProviderRef
		}
		_ = lastMetadata // populated if provider sends metadata in final progress
	}

	if providerRef == "" {
		return nil, fmt.Errorf("gRPC provider did not return a provider_ref after upload")
	}

	return &platform.UploadResult{
		ProviderRef: providerRef,
	}, nil
}

// Register calls the gRPC RegisterImage RPC.
func (a *Adapter) Register(ctx context.Context, result *platform.UploadResult) (*platform.ImageRef, error) {
	imageName := result.Metadata["imageName"]
	req := &providerv1.RegisterRequest{
		ProviderRef:        result.ProviderRef,
		ImageName:          imageName,
		ProviderConfigName: result.Metadata["providerConfigName"],
	}
	for k, v := range result.Metadata {
		if k != "imageName" && k != "providerConfigName" {
			if req.Tags == nil {
				req.Tags = make(map[string]string)
			}
			req.Tags[k] = v
		}
	}

	ref, err := a.client.RegisterImage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gRPC RegisterImage: %w", err)
	}

	return &platform.ImageRef{
		ID:       ref.Id,
		Name:     ref.Name,
		Location: ref.Location,
		Tags:     ref.Tags,
	}, nil
}

// Cleanup calls the gRPC DeleteArtifact RPC. Idempotent.
func (a *Adapter) Cleanup(ctx context.Context, artifact *platform.BuildArtifact) error {
	providerRef := artifact.Metadata["providerRef"]
	if providerRef == "" {
		a.log.Info("cleanup: no provider_ref in artifact metadata, nothing to delete")
		return nil
	}
	req := &providerv1.DeleteRequest{
		ProviderRef:        providerRef,
		ProviderConfigName: artifact.Metadata["providerConfigName"],
	}
	_, err := a.client.DeleteArtifact(ctx, req)
	if err != nil {
		return fmt.Errorf("gRPC DeleteArtifact: %w", err)
	}
	return nil
}

// HealthCheck calls the gRPC HealthCheck RPC.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()

	resp, err := a.client.HealthCheck(ctx, &providerv1.Empty{})
	if err != nil {
		return fmt.Errorf("gRPC HealthCheck: %w", err)
	}
	if !resp.Healthy {
		return fmt.Errorf("provider reported unhealthy: %s", resp.Message)
	}
	return nil
}

func (a *Adapter) ReconcileRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) (*platform.RemoteBuildResult, error) {
	resp, err := a.client.ReconcileRemoteBuild(ctx, remoteBuildProtoRequest(req))
	if err != nil {
		return nil, fmt.Errorf("gRPC ReconcileRemoteBuild: %w", err)
	}
	result := &platform.RemoteBuildResult{
		OperationRef: resp.GetOperationRef(),
		Phase:        platform.RemoteBuildPhase(resp.GetPhase()),
		Message:      resp.GetMessage(),
		Done:         resp.GetDone(),
	}
	if artifact := resp.GetArtifact(); artifact != nil {
		result.Artifact = &platform.BuildArtifact{
			Path:      artifact.GetPath(),
			Format:    platform.ImageFormat(artifact.GetFormat()),
			Checksum:  artifact.GetChecksum(),
			SizeBytes: artifact.GetSizeBytes(),
			OS:        platform.OSFamily(artifact.GetOsFamily()),
			Metadata:  artifact.GetMetadata(),
		}
	}
	if hygiene := resp.GetHygiene(); hygiene != nil {
		result.Hygiene = &platform.RemoteHygieneResult{
			Status:    hygiene.GetStatus(),
			Message:   hygiene.GetMessage(),
			Checks:    append([]string(nil), hygiene.GetChecks()...),
			ResultRef: hygiene.GetResultRef(),
		}
	}
	for _, image := range resp.GetImages() {
		result.Images = append(result.Images, platform.RemoteImageRef{
			Provider:       image.GetProvider(),
			ProviderConfig: image.GetProviderConfigName(),
			ImageRef: platform.ImageRef{
				ID:       image.GetImageRef(),
				Name:     image.GetImageName(),
				Location: image.GetLocation(),
				Tags:     image.GetTags(),
			},
			Format:   platform.ImageFormat(image.GetFormat()),
			Checksum: image.GetChecksum(),
		})
	}
	return result, nil
}

func (a *Adapter) CleanupRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) error {
	_, err := a.client.CleanupRemoteBuild(ctx, remoteBuildProtoRequest(req))
	if err != nil {
		return fmt.Errorf("gRPC CleanupRemoteBuild: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal streaming helper
// ---------------------------------------------------------------------------

// streamArtifact opens artifact.Path and sends it to the upload stream in chunks.
// The first chunk carries artifact metadata; subsequent chunks carry raw data only.
func streamArtifact(stream providerv1.PlatformProvider_UploadArtifactClient, artifact *platform.BuildArtifact) error {
	// Open file for reading.
	f, err := openFile(artifact.Path)
	if err != nil {
		return fmt.Errorf("open artifact %s: %w", artifact.Path, err)
	}
	defer f.Close()

	buf := make([]byte, uploadChunkSize)
	var offset int64
	first := true

	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := &providerv1.UploadChunk{
				Data:   buf[:n],
				Offset: offset,
				Last:   readErr == io.EOF,
			}
			if first {
				chunk.Format = string(artifact.Format)
				chunk.Checksum = artifact.Checksum
				chunk.TotalSizeBytes = artifact.SizeBytes
				chunk.OsFamily = string(artifact.OS)
				chunk.Metadata = artifact.Metadata
				first = false
			}
			if err := stream.Send(chunk); err != nil {
				return fmt.Errorf("send chunk at offset %d: %w", offset, err)
			}
			offset += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read artifact at offset %d: %w", offset, readErr)
		}
	}

	return stream.CloseSend()
}

func remoteBuildProtoRequest(req *platform.RemoteBuildRequest) *providerv1.RemoteBuildRequest {
	if req == nil {
		return &providerv1.RemoteBuildRequest{}
	}
	out := &providerv1.RemoteBuildRequest{
		BuildId:            req.BuildID,
		OperationRef:       req.OperationRef,
		ImageName:          req.ImageName,
		Namespace:          req.Namespace,
		OsFamily:           string(req.OSFamily),
		OsDistribution:     req.OSDistribution,
		OsVersion:          req.OSVersion,
		OsArch:             req.OSArch,
		SourceType:         req.SourceType,
		SourceUrl:          req.SourceURL,
		SourceProviderRef:  req.SourceProviderRef,
		SourceChecksum:     req.SourceChecksum,
		ProviderConfigName: req.Target.ProviderConfigRef.Name,
		Format:             req.Target.Format,
		Tags:               req.Target.Tags,
		TimeoutSeconds:     int64(req.Timeout.Seconds()),
	}
	for _, provisioner := range req.Provisioners {
		out.Provisioners = append(out.Provisioners, &providerv1.RemoteProvisioner{
			Type:      provisioner.Type,
			Image:     provisioner.Image,
			Inline:    provisioner.Inline,
			Playbook:  provisioner.Playbook,
			Args:      provisioner.Args,
			ExtraVars: provisioner.ExtraVars,
			Source:    grpcRemoteProvisionerSource(provisioner.Source),
		})
	}
	if req.GuestAccess != nil {
		out.GuestAccess = &providerv1.RemoteGuestAccess{
			Protocol:  req.GuestAccess.Protocol,
			User:      req.GuestAccess.User,
			GuestPort: req.GuestAccess.GuestPort,
		}
		if creds := req.GuestAccess.Credentials; creds != nil {
			if creds.Generate != nil {
				out.GuestAccess.GeneratedSshKey = creds.Generate.SSHKey
				out.GuestAccess.GeneratedPassword = creds.Generate.Password
			}
			if creds.Injection != nil {
				out.GuestAccess.InjectionMethod = creds.Injection.Method
			}
		}
	}
	return out
}

func grpcRemoteProvisionerSource(source *v1alpha1.ProvisionerSourceSpec) *providerv1.RemoteProvisionerSource {
	if source == nil {
		return nil
	}
	out := &providerv1.RemoteProvisionerSource{}
	if source.Git != nil {
		out.Git = &providerv1.RemoteGitProvisionerSource{
			Url:  source.Git.URL,
			Ref:  source.Git.Ref,
			Path: source.Git.Path,
		}
		if source.Git.Auth != nil {
			out.Git.Auth = &providerv1.RemoteGitProvisionerAuth{
				Token:    source.Git.Auth.RuntimeToken,
				Username: source.Git.Auth.RuntimeUsername,
				Password: source.Git.Auth.RuntimePassword,
			}
		}
	}
	return out
}
