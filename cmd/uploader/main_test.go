package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/uploadpod"
)

func TestReadSecretData_ExpandsJSONCredentialsFile(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"accessKeyId":"id","secretAccessKey":"secret","sessionToken":"token"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials"), raw, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	data, err := readSecretData(dir)
	if err != nil {
		t.Fatalf("readSecretData returned error: %v", err)
	}
	if string(data["accessKeyId"]) != "id" {
		t.Fatalf("accessKeyId = %q, want id", string(data["accessKeyId"]))
	}
	if string(data["secretAccessKey"]) != "secret" {
		t.Fatalf("secretAccessKey = %q, want secret", string(data["secretAccessKey"]))
	}
}

func TestReadSecretData_KeepsProviderSpecificFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "username"), []byte("admin"), 0o600); err != nil {
		t.Fatalf("write username: %v", err)
	}

	data, err := readSecretData(dir)
	if err != nil {
		t.Fatalf("readSecretData returned error: %v", err)
	}
	if string(data["username"]) != "admin" {
		t.Fatalf("username = %q, want admin", string(data["username"]))
	}
}

func TestRecordUploadOperation_UpsertsByProviderConfigAndRef(t *testing.T) {
	workspace := t.TempDir()
	first := uploadOperationRecord{
		Provider:           "aws",
		ProviderConfigName: "aws-prod",
		Format:             "vmdk",
		ProviderRef:        "imagebuilder/build/disk.vmdk",
		Metadata:           map[string]string{"bucket": "old"},
	}
	if err := recordUploadOperation(workspace, first); err != nil {
		t.Fatalf("record first operation: %v", err)
	}
	first.Metadata = map[string]string{"bucket": "new"}
	if err := recordUploadOperation(workspace, first); err != nil {
		t.Fatalf("record updated operation: %v", err)
	}
	ops, err := readUploadOperations(filepath.Join(workspace, operationsName))
	if err != nil {
		t.Fatalf("read operations: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("operations len = %d, want 1", len(ops))
	}
	if ops[0].Metadata["bucket"] != "new" {
		t.Fatalf("metadata bucket = %q, want new", ops[0].Metadata["bucket"])
	}
}

func TestFallbackUploadOperations_UsesBuildResultAndTargetMetadata(t *testing.T) {
	workspace := t.TempDir()
	if err := writeJSON(filepath.Join(workspace, resultFileName), v1alpha1.ArtifactStatus{
		Path:     "/workspace/artifact.vmdk",
		Format:   "vmdk",
		Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OS:       "linux",
		Metadata: map[string]string{
			"buildID":   "build-123",
			"imageName": "ubuntu-prod",
		},
	}); err != nil {
		t.Fatalf("write build result: %v", err)
	}

	ops, err := fallbackUploadOperations(workspace, []uploadpod.TargetConfig{
		{
			Provider:           "aws",
			ProviderConfigName: "aws-prod",
			Format:             "vmdk",
			Extra:              map[string]string{"s3Bucket": "images"},
			Tags:               map[string]string{"env": "prod"},
		},
	})
	if err != nil {
		t.Fatalf("fallbackUploadOperations returned error: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("operations len = %d, want 1", len(ops))
	}
	if ops[0].Metadata["s3Bucket"] != "images" {
		t.Fatalf("s3Bucket metadata = %q, want images", ops[0].Metadata["s3Bucket"])
	}
	if ops[0].Metadata["target.tag.env"] != "prod" {
		t.Fatalf("target tag metadata = %q, want prod", ops[0].Metadata["target.tag.env"])
	}
	if ops[0].Metadata["buildID"] != "build-123" {
		t.Fatalf("buildID metadata = %q, want build-123", ops[0].Metadata["buildID"])
	}
}
