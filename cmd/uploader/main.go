package main

import (
	"context"
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
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"

	_ "github.com/anwendt/imagebuilder/plugins/aws"
	_ "github.com/anwendt/imagebuilder/plugins/azure"
	_ "github.com/anwendt/imagebuilder/plugins/gcp"
	_ "github.com/anwendt/imagebuilder/plugins/openstack"
	_ "github.com/anwendt/imagebuilder/plugins/vsphere"
)

const (
	defaultWorkspace = "/workspace"
	resultFileName   = "result.json"
	uploadResultName = "upload-result.json"
	operationsName   = "upload-operations.json"
	terminationLog   = "/dev/termination-log"
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

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx := context.Background()

	result, err := run(ctx, os.Getenv)
	if err != nil {
		slog.Error("upload failed", slog.Any("error", err))
		_ = writeJSON(terminationLog, map[string]string{"error": err.Error()})
		os.Exit(1)
	}
	payload := uploadResultFile{Images: result.Images, Operations: result.Operations}
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
	for _, target := range targets {
		providerPlugin, err := plugin.Default().Get(target.Provider)
		if err != nil {
			return runResult{}, fmt.Errorf("get provider %q: %w", target.Provider, err)
		}
		secretData, err := readSecretData(target.CredentialsPath)
		if err != nil {
			return runResult{}, fmt.Errorf("read credentials for %q: %w", target.ProviderConfigName, err)
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
		uploadStarted := time.Now()
		uploadResult, err := providerPlugin.Upload(ctx, artifact)
		uploadMilliseconds := time.Since(uploadStarted).Milliseconds()
		if err != nil {
			_ = providerPlugin.Cleanup(ctx, artifact)
			return runResult{}, fmt.Errorf("upload provider %q: %w", target.Provider, err)
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
			_ = providerPlugin.Cleanup(ctx, artifact)
			return runResult{}, fmt.Errorf("record upload operation for provider %q: %w", target.Provider, err)
		}
		registerStarted := time.Now()
		imageRef, err := providerPlugin.Register(ctx, uploadResult)
		registerMilliseconds := time.Since(registerStarted).Milliseconds()
		if err != nil {
			_ = providerPlugin.Cleanup(ctx, artifact)
			return runResult{}, fmt.Errorf("register provider %q: %w", target.Provider, err)
		}
		if imageRef == nil || imageRef.ID == "" {
			_ = providerPlugin.Cleanup(ctx, artifact)
			return runResult{}, fmt.Errorf("provider %q returned empty image reference", target.Provider)
		}
		uploadResult.Metadata["imageRef"] = imageRef.ID
		if err := recordUploadOperation(workspace, uploadOperationRecord{
			Provider:           target.Provider,
			ProviderConfigName: target.ProviderConfigName,
			Format:             target.Format,
			ProviderRef:        uploadResult.ProviderRef,
			Metadata:           cloneStringMap(uploadResult.Metadata),
		}); err != nil {
			return runResult{}, fmt.Errorf("record registered operation for provider %q: %w", target.Provider, err)
		}
		operations = append(operations, v1alpha1.UploadOperationStatus{
			Provider:             target.Provider,
			ProviderConfig:       target.ProviderConfigName,
			Format:               target.Format,
			Phase:                "Succeeded",
			OperationRef:         uploadResult.ProviderRef,
			ImageRef:             imageRef.ID,
			LastTransitionTime:   metav1.Now(),
			UploadMilliseconds:   uploadMilliseconds,
			RegisterMilliseconds: registerMilliseconds,
		})
		images = append(images, v1alpha1.ImageStatus{
			Provider:       target.Provider,
			ProviderConfig: target.ProviderConfigName,
			ImageRef:       imageRef.ID,
			Location:       imageRef.Location,
			Format:         target.Format,
			Checksum:       artifact.Checksum,
		})
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
		providerPlugin, err := plugin.Default().Get(target.Provider)
		if err != nil {
			return runResult{}, fmt.Errorf("get provider %q: %w", target.Provider, err)
		}
		secretData, err := readSecretData(target.CredentialsPath)
		if err != nil {
			return runResult{}, fmt.Errorf("read credentials for %q: %w", target.ProviderConfigName, err)
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
	data, err := os.ReadFile(path)
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
		value, err := os.ReadFile(filepath.Join(dir, entry.Name()))
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
	data, err := os.ReadFile(path)
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
