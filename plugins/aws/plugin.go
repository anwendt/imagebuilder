// plugins/aws/plugin.go
//
// Built-in AWS platform provider.
// Registers itself via init() — activated by blank import in cmd/operator/main.go:
//
//	import _ "github.com/anwendt/imagebuilder/plugins/aws"
//
// License: Apache 2.0
// SDK: aws-sdk-go-v2 (Apache 2.0)

package aws

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func init() {
	if err := plugin.Register(&AWSPlugin{}); err != nil {
		panic(fmt.Sprintf("aws plugin: failed to register: %v", err))
	}
}

// AWSPlugin uploads VM images to AWS and registers them as AMIs.
// Upload flow: artifact → S3 → ec2:ImportSnapshot → ec2:RegisterImage (AMI)
type AWSPlugin struct {
	log    *slog.Logger
	config awsConfig
	// ec2Client *ec2.Client  — add after go.sum is populated
	// s3Client  *s3.Client
}

type awsConfig struct {
	region          string
	accessKeyID     string
	secretAccessKey string
	sessionToken    string            // optional, for assume-role
	roleARN         string            // optional
	extraConfig     map[string]string // provider-specific config from ProviderConfig.spec.extra
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

func (p *AWSPlugin) Name() string    { return "aws" }
func (p *AWSPlugin) Version() string { return "v0.1.0" }

// ---------------------------------------------------------------------------
// Capabilities
// ---------------------------------------------------------------------------

func (p *AWSPlugin) SupportedFormats() []platform.ImageFormat {
	return []platform.ImageFormat{
		platform.FormatVMDK,
		platform.FormatRaw,
		platform.FormatVHD,
	}
}

func (p *AWSPlugin) SupportedOS() []platform.OSFamily {
	return []platform.OSFamily{
		platform.OSFamilyLinux,
		platform.OSFamilyWindows,
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func (p *AWSPlugin) Init(ctx context.Context, cfg platform.PluginConfig) error {
	p.log = slog.Default().With(slog.String("plugin", p.Name()))

	creds := cfg.SecretData
	p.config = awsConfig{
		region:          cfg.Region,
		accessKeyID:     string(creds["accessKeyId"]),
		secretAccessKey: string(creds["secretAccessKey"]),
		sessionToken:    string(creds["sessionToken"]),
		roleARN:         cfg.Extra["roleArn"],
		extraConfig:     cfg.Extra,
	}

	if p.config.region == "" {
		return fmt.Errorf("aws plugin: region is required in ProviderConfig")
	}
	if p.config.accessKeyID == "" || p.config.secretAccessKey == "" {
		return fmt.Errorf("aws plugin: secret must contain accessKeyId and secretAccessKey")
	}

	// TODO: initialise aws-sdk-go-v2 clients
	// cfg, err := awsconfig.LoadDefaultConfig(ctx, ...)
	// p.ec2Client = ec2.NewFromConfig(cfg)
	// p.s3Client  = s3.NewFromConfig(cfg)

	p.log.Info("aws plugin initialised", slog.String("region", p.config.region))
	return nil
}

func (p *AWSPlugin) Validate(ctx context.Context, spec v1alpha1.TargetSpec) error {
	if spec.Format != string(platform.FormatVMDK) &&
		spec.Format != string(platform.FormatRaw) &&
		spec.Format != string(platform.FormatVHD) {
		return fmt.Errorf("aws plugin: unsupported format %q — use vmdk, raw, or vhd", spec.Format)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Core operations
// ---------------------------------------------------------------------------

func (p *AWSPlugin) Upload(ctx context.Context, artifact *platform.BuildArtifact) (*platform.UploadResult, error) {
	p.log.Info("uploading artifact to S3",
		slog.String("path", artifact.Path),
		slog.String("format", string(artifact.Format)),
		slog.Int64("size", artifact.SizeBytes),
	)

	// TODO: implement multipart S3 upload
	// 1. Create S3 bucket (or use configured one from Extra["s3Bucket"])
	// 2. Multipart upload artifact.Path → s3://bucket/imagebuilder/{buildID}/disk.vmdk
	// 3. Return S3 key as ProviderRef

	buildID := artifact.Metadata["buildID"]
	if buildID == "" {
		return nil, fmt.Errorf("aws plugin: artifact metadata missing required key 'buildID'")
	}
	s3Key := fmt.Sprintf("imagebuilder/%s/disk.%s", buildID, artifact.Format)

	return &platform.UploadResult{
		ProviderRef: s3Key,
		Metadata: map[string]string{
			"bucket": p.config.extraConfig["s3Bucket"],
			"key":    s3Key,
		},
	}, nil
}

func (p *AWSPlugin) Register(ctx context.Context, result *platform.UploadResult) (*platform.ImageRef, error) {
	p.log.Info("registering AMI", slog.String("s3key", result.ProviderRef))

	// TODO: implement AMI registration
	// 1. ec2:ImportSnapshot (s3Key → snapshot)
	// 2. ec2:RegisterImage  (snapshot → AMI)
	// 3. Poll until AMI state == "available"

	return &platform.ImageRef{
		ID:       "ami-placeholder",
		Name:     result.Metadata["imageName"],
		Location: p.config.region,
	}, nil
}

func (p *AWSPlugin) Cleanup(ctx context.Context, artifact *platform.BuildArtifact) error {
	// TODO: delete S3 object if it was uploaded
	p.log.Info("cleanup: nothing to clean (placeholder)")
	return nil
}

func (p *AWSPlugin) HealthCheck(ctx context.Context) error {
	// TODO: ec2:DescribeRegions or sts:GetCallerIdentity
	return nil
}

