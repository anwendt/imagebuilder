package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/uploadpod"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	plugingrpc "github.com/anwendt/imagebuilder/pkg/plugin/grpc"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
	"github.com/anwendt/imagebuilder/pkg/security/netguard"

	_ "github.com/anwendt/imagebuilder/plugins/aws"
	_ "github.com/anwendt/imagebuilder/plugins/azure"
	_ "github.com/anwendt/imagebuilder/plugins/gcp"
	_ "github.com/anwendt/imagebuilder/plugins/openstack"
	_ "github.com/anwendt/imagebuilder/plugins/vsphere"
)

const (
	defaultWorkspace         = "/workspace"
	resultFileName           = "result.json"
	uploadResultName         = "upload-result.json"
	operationsName           = "upload-operations.json"
	sessionsName             = "upload-sessions.json"
	terminationLog           = "/dev/termination-log"
	temporaryFailureExitCode = 75
)

type uploadResultFile struct {
	Images     []v1alpha1.ImageStatus           `json:"images"`
	Operations []v1alpha1.UploadOperationStatus `json:"operations,omitempty"`
}

type runResult struct {
	Images     []v1alpha1.ImageStatus
	Operations []v1alpha1.UploadOperationStatus
}

type uploadOperationRecord struct {
	Provider           string            `json:"provider"`
	ProviderConfigName string            `json:"providerConfigName"`
	Format             string            `json:"format"`
	ProviderRef        string            `json:"providerRef"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

type uploadSessionRecord struct {
	Provider           string                          `json:"provider"`
	ProviderConfigName string                          `json:"providerConfigName"`
	Format             string                          `json:"format"`
	Checksum           string                          `json:"checksum"`
	SizeBytes          int64                           `json:"sizeBytes"`
	IdempotencyKey     string                          `json:"idempotencyKey"`
	ResumeToken        string                          `json:"resumeToken,omitempty"`
	CommittedOffset    int64                           `json:"committedOffset,omitempty"`
	ResumeMode         string                          `json:"resumeMode,omitempty"`
	Phase              string                          `json:"phase"`
	ProviderRef        string                          `json:"providerRef,omitempty"`
	Metadata           map[string]string               `json:"metadata,omitempty"`
	Image              *v1alpha1.ImageStatus           `json:"image,omitempty"`
	Operation          *v1alpha1.UploadOperationStatus `json:"operation,omitempty"`
}

type uploadSessionFile struct {
	Version  int                   `json:"version"`
	Sessions []uploadSessionRecord `json:"sessions"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx := context.Background()

	result, err := run(ctx, os.Getenv)
	if err != nil {
		slog.Error("upload failed", slog.Any("error", err))
		_ = writeJSON(terminationLog, map[string]string{"error": err.Error()})
		if providererrors.IsTransient(err) {
			os.Exit(temporaryFailureExitCode)
		}
		os.Exit(1)
	}
	payload := uploadResultFile(result)
	if err := writeJSON(terminationLog, payload); err != nil {
		slog.Warn("write termination message", slog.Any("error", err))
	}
	workspace := envOrDefault(os.Getenv, "WORKSPACE_DIR", defaultWorkspace)
	if err := writeJSON(filepath.Join(workspace, uploadResultName), payload); err != nil {
		slog.Warn("write upload result", slog.Any("error", err))
	}
}

func run(ctx context.Context, getenv func(string) string) (runResult, error) {
	workspace := envOrDefault(getenv, "WORKSPACE_DIR", defaultWorkspace)
	if getenv("UPLOAD_CLEANUP_ONLY") == "true" {
		return cleanupUploadedArtifacts(ctx, workspace, getenv)
	}
	artifact, err := readArtifact(filepath.Join(workspace, resultFileName))
	if err != nil {
		return runResult{}, err
	}
	targets, err := readTargets(getenv("UPLOAD_TARGETS_JSON"))
	if err != nil {
		return runResult{}, err
	}
	images := make([]v1alpha1.ImageStatus, 0, len(targets))
	operations := make([]v1alpha1.UploadOperationStatus, 0, len(targets))
	sessionPath := filepath.Join(workspace, sessionsName)
	sessions, err := readUploadSessions(sessionPath)
	if err != nil {
		return runResult{}, err
	}
	for _, target := range targets {
		session, err := ensureUploadSession(sessionPath, &sessions, target, artifact)
		if err != nil {
			return runResult{}, err
		}
		if session.Phase == "registered" {
			if session.Image == nil || session.Operation == nil || session.ProviderRef == "" {
				return runResult{}, fmt.Errorf("registered upload session for %q is incomplete", target.ProviderConfigName)
			}
			if err := recordUploadOperation(workspace, uploadOperationRecord{
				Provider: target.Provider, ProviderConfigName: target.ProviderConfigName,
				Format: target.Format, ProviderRef: session.ProviderRef, Metadata: cloneStringMap(session.Metadata),
			}); err != nil {
				return runResult{}, fmt.Errorf("restore upload operation for provider %q: %w", target.Provider, err)
			}
			images = append(images, *session.Image)
			operations = append(operations, *session.Operation)
			continue
		}
		providerPlugin, closeProvider, err := providerPluginForTarget(ctx, target)
		if err != nil {
			return runResult{}, err
		}
		defer closeProvider()
		secretData, err := readSecretData(target.CredentialsPath)
		if err != nil {
			return runResult{}, fmt.Errorf("read credentials for %q: %w", target.ProviderConfigName, err)
		}
		if err := validateProviderEndpoint(ctx, target); err != nil {
			return runResult{}, err
		}
		if err := providerPlugin.Init(ctx, platform.PluginConfig{
			ProviderConfigName: target.ProviderConfigName,
			SecretData:         secretData,
			Region:             target.Region,
			Endpoint:           target.Endpoint,
			Insecure:           target.Insecure,
			Extra:              target.Extra,
		}); err != nil {
			return runResult{}, fmt.Errorf("init provider %q: %w", target.Provider, err)
		}
		targetSpec := v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: target.ProviderConfigName},
			Format:            target.Format,
			Tags:              target.Tags,
		}
		if err := providerPlugin.Validate(ctx, targetSpec); err != nil {
			return runResult{}, fmt.Errorf("validate provider %q: %w", target.Provider, err)
		}
		targetArtifact := *artifact
		targetArtifact.Metadata = cloneStringMap(artifact.Metadata)
		if targetArtifact.Metadata == nil {
			targetArtifact.Metadata = map[string]string{}
		}
		targetArtifact.Metadata["providerConfigName"] = target.ProviderConfigName
		targetArtifact.Metadata["format"] = target.Format
		targetArtifact.Metadata["upload.idempotencyKey"] = session.IdempotencyKey
		targetArtifact.Metadata["upload.sessionToken"] = session.ResumeToken
		var uploadResult *platform.UploadResult
		uploadMilliseconds := int64(0)
		if session.Phase == "uploaded" {
			uploadResult = &platform.UploadResult{ProviderRef: session.ProviderRef, Metadata: cloneStringMap(session.Metadata)}
		} else {
			uploadStarted := time.Now()
			if resumable, ok := providerPlugin.(platform.ResumablePlugin); ok {
				uploadResult, err = resumable.UploadResumable(ctx, &targetArtifact, platform.UploadSession{
					IdempotencyKey: session.IdempotencyKey, ResumeToken: session.ResumeToken,
					CommittedOffset: session.CommittedOffset, ResumeMode: session.ResumeMode,
				}, func(checkpoint platform.UploadSession) error {
					session.ResumeToken = checkpoint.ResumeToken
					session.CommittedOffset = checkpoint.CommittedOffset
					session.ResumeMode = checkpoint.ResumeMode
					targetArtifact.Metadata["upload.sessionToken"] = checkpoint.ResumeToken
					return writeUploadSessions(sessionPath, sessions)
				})
			} else {
				uploadResult, err = providerPlugin.Upload(ctx, &targetArtifact)
			}
			uploadMilliseconds = time.Since(uploadStarted).Milliseconds()
			if err != nil {
				if session.ResumeMode != "offset" {
					_ = providerPlugin.Cleanup(ctx, &targetArtifact)
				}
				return runResult{}, fmt.Errorf("upload provider %q session %q: %w", target.Provider, session.IdempotencyKey, err)
			}
			if uploadResult == nil || uploadResult.ProviderRef == "" {
				return runResult{}, fmt.Errorf("provider %q returned empty upload result", target.Provider)
			}
			session.Phase = "uploaded"
			session.ProviderRef = uploadResult.ProviderRef
			session.CommittedOffset = artifact.SizeBytes
			session.Metadata = cloneStringMap(uploadResult.Metadata)
			if err := writeUploadSessions(sessionPath, sessions); err != nil {
				return runResult{}, fmt.Errorf("checkpoint completed upload for provider %q: %w", target.Provider, err)
			}
		}
		if uploadResult.Metadata == nil {
			uploadResult.Metadata = map[string]string{}
		}
		uploadResult.Metadata["providerRef"] = uploadResult.ProviderRef
		uploadResult.Metadata["providerConfigName"] = target.ProviderConfigName
		for key, value := range target.Tags {
			uploadResult.Metadata["target.tag."+key] = value
		}
		if err := recordUploadOperation(workspace, uploadOperationRecord{
			Provider:           target.Provider,
			ProviderConfigName: target.ProviderConfigName,
			Format:             target.Format,
			ProviderRef:        uploadResult.ProviderRef,
			Metadata:           cloneStringMap(uploadResult.Metadata),
		}); err != nil {
			_ = providerPlugin.Cleanup(ctx, &targetArtifact)
			return runResult{}, fmt.Errorf("record upload operation for provider %q: %w", target.Provider, err)
		}
		registerStarted := time.Now()
		imageRef, err := providerPlugin.Register(ctx, uploadResult)
		registerMilliseconds := time.Since(registerStarted).Milliseconds()
		if err != nil {
			return runResult{}, fmt.Errorf("register provider %q session %q: %w", target.Provider, session.IdempotencyKey, err)
		}
		if imageRef == nil || imageRef.ID == "" {
			return runResult{}, fmt.Errorf("provider %q returned empty image reference", target.Provider)
		}
		uploadResult.Metadata["imageRef"] = imageRef.ID
		operation := v1alpha1.UploadOperationStatus{
			Provider: target.Provider, ProviderConfig: target.ProviderConfigName, Format: target.Format,
			Phase: "Succeeded", OperationRef: uploadResult.ProviderRef, ImageRef: imageRef.ID,
			LastTransitionTime: metav1.Now(), UploadMilliseconds: uploadMilliseconds,
			UploadBytes: artifact.SizeBytes, RegisterMilliseconds: registerMilliseconds,
		}
		image := v1alpha1.ImageStatus{
			Provider: target.Provider, ProviderConfig: target.ProviderConfigName, ImageRef: imageRef.ID,
			Location: imageRef.Location, Format: target.Format, Checksum: artifact.Checksum,
		}
		session.Phase = "registered"
		session.Metadata = cloneStringMap(uploadResult.Metadata)
		session.Image = &image
		session.Operation = &operation
		if err := writeUploadSessions(sessionPath, sessions); err != nil {
			return runResult{}, fmt.Errorf("checkpoint registered image for provider %q: %w", target.Provider, err)
		}
		if err := recordUploadOperation(workspace, uploadOperationRecord{
			Provider:           target.Provider,
			ProviderConfigName: target.ProviderConfigName,
			Format:             target.Format,
			ProviderRef:        uploadResult.ProviderRef,
			Metadata:           cloneStringMap(uploadResult.Metadata),
		}); err != nil {
			return runResult{}, fmt.Errorf("record registered operation for provider %q: %w", target.Provider, err)
		}
		operations = append(operations, operation)
		images = append(images, image)
	}
	return runResult{Images: images, Operations: operations}, nil
}

func cleanupUploadedArtifacts(ctx context.Context, workspace string, getenv func(string) string) (runResult, error) {
	targets, err := readTargets(getenv("UPLOAD_TARGETS_JSON"))
	if err != nil {
		return runResult{}, err
	}
	operations, err := readUploadOperations(filepath.Join(workspace, operationsName))
	if err != nil {
		if os.IsNotExist(err) {
			operations, err = fallbackUploadOperations(workspace, targets)
			if err != nil {
				return runResult{}, err
			}
			if len(operations) == 0 {
				return runResult{}, nil
			}
		} else {
			return runResult{}, err
		}
	}
	targetsByProviderConfig := map[string]uploadpod.TargetConfig{}
	for _, target := range targets {
		targetsByProviderConfig[target.ProviderConfigName] = target
	}
	for _, op := range operations {
		target, ok := targetsByProviderConfig[op.ProviderConfigName]
		if !ok {
			return runResult{}, fmt.Errorf("cleanup operation references unknown ProviderConfig %q", op.ProviderConfigName)
		}
		if op.Provider != "" && op.Provider != target.Provider {
			return runResult{}, fmt.Errorf("cleanup operation provider %q does not match ProviderConfig %q provider %q", op.Provider, op.ProviderConfigName, target.Provider)
		}
		providerPlugin, closeProvider, err := providerPluginForTarget(ctx, target)
		if err != nil {
			return runResult{}, err
		}
		defer closeProvider()
		secretData, err := readSecretData(target.CredentialsPath)
		if err != nil {
			return runResult{}, fmt.Errorf("read credentials for %q: %w", target.ProviderConfigName, err)
		}
		if err := validateProviderEndpoint(ctx, target); err != nil {
			return runResult{}, err
		}
		if err := providerPlugin.Init(ctx, platform.PluginConfig{
			ProviderConfigName: target.ProviderConfigName,
			SecretData:         secretData,
			Region:             target.Region,
			Endpoint:           target.Endpoint,
			Insecure:           target.Insecure,
			Extra:              target.Extra,
		}); err != nil {
			return runResult{}, fmt.Errorf("init provider %q: %w", target.Provider, err)
		}
		metadata := cloneStringMap(op.Metadata)
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata["providerRef"] = op.ProviderRef
		metadata["providerConfigName"] = op.ProviderConfigName
		if err := providerPlugin.Cleanup(ctx, &platform.BuildArtifact{
			Format:   platform.ImageFormat(op.Format),
			Metadata: metadata,
		}); err != nil {
			return runResult{}, fmt.Errorf("cleanup provider %q operation %q: %w", target.Provider, op.ProviderRef, err)
		}
	}
	return runResult{}, nil
}

func providerPluginForTarget(ctx context.Context, target uploadpod.TargetConfig) (platform.Plugin, func(), error) {
	if target.GRPC == nil {
		providerPlugin, err := plugin.Default().New(target.Provider)
		if err != nil {
			return nil, func() {}, fmt.Errorf("get provider %q: %w", target.Provider, err)
		}
		return providerPlugin, func() {
			if closePlugin, ok := providerPlugin.(platform.ClosePlugin); ok {
				_ = closePlugin.Close()
			}
		}, nil
	}
	if target.GRPC.Address == "" {
		return nil, func() {}, fmt.Errorf("gRPC address for provider %q is required", target.Provider)
	}
	tlsConfig, err := providerTLSConfig(target.GRPC.TLS)
	if err != nil {
		return nil, func() {}, fmt.Errorf("load gRPC TLS config for provider %q: %w", target.Provider, err)
	}
	adapter := plugingrpc.NewAdapterWithTLS(target.GRPC.Address, tlsConfig)
	if err := adapter.Connect(ctx); err != nil {
		return nil, func() {}, fmt.Errorf("connect to PlatformProvider %q at %s: %w", target.Provider, target.GRPC.Address, err)
	}
	if adapter.Name() != target.Provider {
		_ = adapter.Close()
		return nil, func() {}, fmt.Errorf("PlatformProvider at %s reported name %q, want %q", target.GRPC.Address, adapter.Name(), target.Provider)
	}
	return adapter, func() { _ = adapter.Close() }, nil
}

func providerTLSConfig(config *uploadpod.GRPCTLSConfig) (*plugingrpc.ProviderTLSConfig, error) {
	if config == nil {
		return nil, nil
	}
	if config.CAPath == "" || config.ClientCertPath == "" || config.ClientKeyPath == "" {
		return nil, fmt.Errorf("CA, client certificate, and client key paths are required")
	}
	ca, err := os.ReadFile(config.CAPath) // #nosec G304 -- Paths are injected by the trusted controller into the upload Job.
	if err != nil {
		return nil, fmt.Errorf("read CA bundle: %w", err)
	}
	cert, err := os.ReadFile(config.ClientCertPath) // #nosec G304 -- Paths are injected by the trusted controller into the upload Job.
	if err != nil {
		return nil, fmt.Errorf("read client certificate: %w", err)
	}
	key, err := os.ReadFile(config.ClientKeyPath) // #nosec G304 -- Paths are injected by the trusted controller into the upload Job.
	if err != nil {
		return nil, fmt.Errorf("read client key: %w", err)
	}
	return &plugingrpc.ProviderTLSConfig{
		ServerName: config.ServerName,
		CABundle:   ca,
		ClientCert: cert,
		ClientKey:  key,
	}, nil
}

func validateProviderEndpoint(ctx context.Context, target uploadpod.TargetConfig) error {
	return validateProviderEndpointWithOptions(ctx, target, netguard.Options{})
}

func validateProviderEndpointWithOptions(ctx context.Context, target uploadpod.TargetConfig, opts netguard.Options) error {
	if target.Endpoint == "" {
		return nil
	}
	if err := netguard.ValidatePublicHTTPSURL(ctx, "provider endpoint", target.Endpoint, opts); err != nil {
		return fmt.Errorf("provider endpoint for %q rejected by SSRF protection: %w", target.ProviderConfigName, err)
	}
	return nil
}

func fallbackUploadOperations(workspace string, targets []uploadpod.TargetConfig) ([]uploadOperationRecord, error) {
	artifact, err := readArtifact(filepath.Join(workspace, resultFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	ops := make([]uploadOperationRecord, 0, len(targets))
	for _, target := range targets {
		metadata := cloneStringMap(artifact.Metadata)
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata["providerConfigName"] = target.ProviderConfigName
		if _, ok := metadata["format"]; !ok {
			metadata["format"] = string(artifact.Format)
		}
		if _, ok := metadata["checksum"]; !ok {
			metadata["checksum"] = artifact.Checksum
		}
		if _, ok := metadata["os"]; !ok {
			metadata["os"] = string(artifact.OS)
		}
		for key, value := range target.Extra {
			metadata[key] = value
			metadata["provider.extra."+key] = value
		}
		for key, value := range target.Tags {
			metadata["target.tag."+key] = value
		}
		format := target.Format
		if format == "" {
			format = string(artifact.Format)
		}
		ops = append(ops, uploadOperationRecord{
			Provider:           target.Provider,
			ProviderConfigName: target.ProviderConfigName,
			Format:             format,
			ProviderRef:        metadata["providerRef"],
			Metadata:           metadata,
		})
	}
	return ops, nil
}

func readArtifact(path string) (*platform.BuildArtifact, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- Path points at the build result file in the controller-owned workspace.
	if err != nil {
		return nil, fmt.Errorf("read build result: %w", err)
	}
	status := &v1alpha1.ArtifactStatus{}
	if err := json.Unmarshal(data, status); err != nil {
		return nil, fmt.Errorf("parse build result: %w", err)
	}
	return &platform.BuildArtifact{
		Path:      status.Path,
		Format:    platform.ImageFormat(status.Format),
		Checksum:  status.Checksum,
		SizeBytes: status.SizeBytes,
		OS:        platform.OSFamily(status.OS),
		Metadata:  status.Metadata,
	}, nil
}

func readTargets(raw string) ([]uploadpod.TargetConfig, error) {
	if raw == "" {
		return nil, fmt.Errorf("UPLOAD_TARGETS_JSON is required")
	}
	var targets []uploadpod.TargetConfig
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, fmt.Errorf("parse UPLOAD_TARGETS_JSON: %w", err)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one upload target is required")
	}
	return targets, nil
}

func readSecretData(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	data := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		value, err := os.ReadFile(filepath.Join(dir, entry.Name())) // #nosec G304 -- Directory is a Kubernetes Secret mount controlled by the pod spec.
		if err != nil {
			return nil, err
		}
		if entry.Name() == "credentials" {
			expanded, ok := expandJSONCredentials(value)
			if ok {
				for key, expandedValue := range expanded {
					data[key] = expandedValue
				}
				continue
			}
		}
		data[entry.Name()] = value
	}
	return data, nil
}

func expandJSONCredentials(raw []byte) (map[string][]byte, bool) {
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false
	}
	expanded := make(map[string][]byte, len(values))
	for key, value := range values {
		expanded[key] = []byte(value)
	}
	return expanded, true
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func readUploadSessions(path string) ([]uploadSessionRecord, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- Path is inside the controller-owned workspace PVC.
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read upload sessions: %w", err)
	}
	var state uploadSessionFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse upload sessions: %w", err)
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("unsupported upload session file version %d", state.Version)
	}
	return state.Sessions, nil
}

func ensureUploadSession(path string, sessions *[]uploadSessionRecord, target uploadpod.TargetConfig, artifact *platform.BuildArtifact) (*uploadSessionRecord, error) {
	for i := range *sessions {
		session := &(*sessions)[i]
		if session.ProviderConfigName != target.ProviderConfigName || session.Format != target.Format {
			continue
		}
		if session.Provider != target.Provider || session.Checksum != artifact.Checksum || session.SizeBytes != artifact.SizeBytes {
			return nil, fmt.Errorf("upload session for ProviderConfig %q does not match the current provider or artifact", target.ProviderConfigName)
		}
		return session, nil
	}
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "%s\x00%s\x00%s\x00%s\x00%s\x00%d", artifact.Metadata["buildID"], target.Provider, target.ProviderConfigName, target.Format, artifact.Checksum, artifact.SizeBytes)
	digest := hasher.Sum(nil)
	*sessions = append(*sessions, uploadSessionRecord{
		Provider: target.Provider, ProviderConfigName: target.ProviderConfigName,
		Format: target.Format, Checksum: artifact.Checksum, SizeBytes: artifact.SizeBytes,
		IdempotencyKey: fmt.Sprintf("upload-%x", digest), Phase: "uploading",
	})
	slices.SortStableFunc(*sessions, func(a, b uploadSessionRecord) int {
		if a.ProviderConfigName < b.ProviderConfigName {
			return -1
		}
		if a.ProviderConfigName > b.ProviderConfigName {
			return 1
		}
		if a.Format < b.Format {
			return -1
		}
		if a.Format > b.Format {
			return 1
		}
		return 0
	})
	if err := writeUploadSessions(path, *sessions); err != nil {
		return nil, err
	}
	for i := range *sessions {
		if (*sessions)[i].ProviderConfigName == target.ProviderConfigName && (*sessions)[i].Format == target.Format {
			return &(*sessions)[i], nil
		}
	}
	return nil, fmt.Errorf("create upload session for ProviderConfig %q", target.ProviderConfigName)
}

func writeUploadSessions(path string, sessions []uploadSessionRecord) error {
	data, err := json.MarshalIndent(uploadSessionFile{Version: 1, Sessions: sessions}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal upload sessions: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("create upload session checkpoint: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect upload session checkpoint: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write upload session checkpoint: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync upload session checkpoint: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close upload session checkpoint: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit upload session checkpoint: %w", err)
	}
	return nil
}

func recordUploadOperation(workspace string, op uploadOperationRecord) error {
	if op.ProviderConfigName == "" || op.ProviderRef == "" {
		return fmt.Errorf("provider config and provider ref are required")
	}
	path := filepath.Join(workspace, operationsName)
	ops, err := readUploadOperations(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	replaced := false
	for i := range ops {
		if ops[i].ProviderConfigName == op.ProviderConfigName && ops[i].ProviderRef == op.ProviderRef {
			ops[i] = op
			replaced = true
			break
		}
	}
	if !replaced {
		ops = append(ops, op)
	}
	slices.SortStableFunc(ops, func(a, b uploadOperationRecord) int {
		if a.ProviderConfigName < b.ProviderConfigName {
			return -1
		}
		if a.ProviderConfigName > b.ProviderConfigName {
			return 1
		}
		if a.ProviderRef < b.ProviderRef {
			return -1
		}
		if a.ProviderRef > b.ProviderRef {
			return 1
		}
		return 0
	})
	return writeJSON(path, ops)
}

func readUploadOperations(path string) ([]uploadOperationRecord, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- Path points at the upload operations file in the controller-owned workspace.
	if err != nil {
		return nil, err
	}
	var ops []uploadOperationRecord
	if err := json.Unmarshal(data, &ops); err != nil {
		return nil, fmt.Errorf("parse upload operations: %w", err)
	}
	return ops, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func envOrDefault(getenv func(string) string, name, fallback string) string {
	if value := getenv(name); value != "" {
		return value
	}
	return fallback
}
