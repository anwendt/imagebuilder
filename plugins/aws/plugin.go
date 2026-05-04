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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

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
	log          *slog.Logger
	config       awsConfig
	localClient  awsLocalImageClient
	remoteClient awsRemoteBuildClient
}

var (
	_ platform.RemoteBuildPlugin        = (*AWSPlugin)(nil)
	_ platform.RemoteBuildCleanupPlugin = (*AWSPlugin)(nil)
)

type awsConfig struct {
	providerConfigName string
	region             string
	endpoint           string
	insecure           bool
	accessKeyID        string
	secretAccessKey    string
	sessionToken       string            // optional, for assume-role
	roleARN            string            // optional
	extraConfig        map[string]string // provider-specific config from ProviderConfig.spec.extra
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
		platform.FormatAMI,
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

func (p *AWSPlugin) SupportedBuildModes() []string {
	return []string{
		v1alpha1.BuildModeLocal,
		v1alpha1.BuildModeRemote,
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func (p *AWSPlugin) Init(ctx context.Context, cfg platform.PluginConfig) error {
	p.log = slog.Default().With(slog.String("plugin", p.Name()))

	creds := cfg.SecretData
	p.config = awsConfig{
		providerConfigName: cfg.ProviderConfigName,
		region:             cfg.Region,
		endpoint:           cfg.Endpoint,
		insecure:           cfg.Insecure,
		accessKeyID:        string(creds["accessKeyId"]),
		secretAccessKey:    string(creds["secretAccessKey"]),
		sessionToken:       string(creds["sessionToken"]),
		roleARN:            cfg.Extra["roleArn"],
		extraConfig:        cfg.Extra,
	}

	if p.config.region == "" {
		return fmt.Errorf("aws plugin: region is required in ProviderConfig")
	}
	if (p.config.accessKeyID == "") != (p.config.secretAccessKey == "") {
		return fmt.Errorf("aws plugin: secret must contain both accessKeyId and secretAccessKey when static credentials are used")
	}

	remoteClient, err := newAWSRemoteBuildClient(ctx, p.config, p.log)
	if err != nil {
		return fmt.Errorf("aws plugin: initialise remote build client: %w", err)
	}
	p.remoteClient = remoteClient
	localClient, err := newAWSLocalImageClient(ctx, p.config)
	if err != nil {
		return fmt.Errorf("aws plugin: initialise local image client: %w", err)
	}
	p.localClient = localClient

	p.log.Info("aws plugin initialised", slog.String("region", p.config.region))
	return nil
}

func (p *AWSPlugin) Validate(ctx context.Context, spec v1alpha1.TargetSpec) error {
	if spec.Format != string(platform.FormatAMI) &&
		spec.Format != string(platform.FormatVMDK) &&
		spec.Format != string(platform.FormatRaw) &&
		spec.Format != string(platform.FormatVHD) {
		return fmt.Errorf("aws plugin: unsupported format %q — use ami, vmdk, raw, or vhd", spec.Format)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Core operations
// ---------------------------------------------------------------------------

func (p *AWSPlugin) Upload(ctx context.Context, artifact *platform.BuildArtifact) (*platform.UploadResult, error) {
	if artifact == nil {
		return nil, fmt.Errorf("aws plugin: artifact is required")
	}
	p.log.Info("uploading artifact to S3",
		slog.String("path", artifact.Path),
		slog.String("format", string(artifact.Format)),
		slog.Int64("size", artifact.SizeBytes),
	)

	buildID := artifact.Metadata["buildID"]
	if buildID == "" {
		return nil, fmt.Errorf("aws plugin: artifact metadata missing required key 'buildID'")
	}
	if artifact.Path == "" {
		return nil, fmt.Errorf("aws plugin: artifact path is required")
	}
	client := p.localClient
	if client == nil {
		return nil, fmt.Errorf("aws plugin: local image client is not initialised")
	}
	bucket := strings.TrimSpace(p.config.extraConfig["s3Bucket"])
	if bucket == "" {
		return nil, fmt.Errorf("aws plugin: ProviderConfig extra s3Bucket is required for local AWS uploads")
	}
	s3Key := localUploadKey(p.config.extraConfig, buildID, artifact.Format)
	if err := client.UploadObject(ctx, bucket, s3Key, artifact.Path); err != nil {
		return nil, fmt.Errorf("aws plugin: upload artifact to s3://%s/%s: %w", bucket, s3Key, err)
	}
	if artifact.Metadata == nil {
		artifact.Metadata = map[string]string{}
	}
	artifact.Metadata["aws.s3Bucket"] = bucket
	artifact.Metadata["aws.s3Key"] = s3Key

	return &platform.UploadResult{
		ProviderRef: s3Key,
		Metadata: map[string]string{
			"bucket":   bucket,
			"key":      s3Key,
			"buildID":  buildID,
			"format":   string(artifact.Format),
			"checksum": artifact.Checksum,
			"os":       string(artifact.OS),
			"imageName": firstNonEmpty(
				artifact.Metadata["imageName"],
				artifact.Metadata["vmimage"],
				p.config.extraConfig["imageName"],
				"imagebuilder-"+sanitizeAWSName(buildID),
			),
		},
	}, nil
}

func (p *AWSPlugin) Register(ctx context.Context, result *platform.UploadResult) (*platform.ImageRef, error) {
	if result == nil {
		return nil, fmt.Errorf("aws plugin: upload result is required")
	}
	p.log.Info("registering AMI", slog.String("s3key", result.ProviderRef))
	client := p.localClient
	if client == nil {
		return nil, fmt.Errorf("aws plugin: local image client is not initialised")
	}
	input, err := localRegisterInput(p.config, result)
	if err != nil {
		return nil, err
	}
	ref, err := client.RegisterAMI(ctx, input)
	if err != nil {
		cleanupErr := client.CleanupLocalImage(ctx, result.Metadata)
		if cleanupErr != nil {
			return nil, fmt.Errorf("aws plugin: register AMI: %w; cleanup failed: %v", err, cleanupErr)
		}
		return nil, fmt.Errorf("aws plugin: register AMI: %w", err)
	}
	if cleanupErr := client.CleanupLocalImage(ctx, map[string]string{
		"bucket": result.Metadata["bucket"],
		"key":    result.Metadata["key"],
	}); cleanupErr != nil {
		p.log.Warn("cleanup AWS staging object after successful registration", slog.Any("error", cleanupErr))
	}
	result.Metadata["imageId"] = ref.ID

	return ref, nil
}

func (p *AWSPlugin) Cleanup(ctx context.Context, artifact *platform.BuildArtifact) error {
	if artifact == nil || artifact.Metadata == nil {
		return nil
	}
	client := p.localClient
	if client == nil {
		return nil
	}
	bucket := firstNonEmpty(
		artifact.Metadata["aws.s3Bucket"],
		artifact.Metadata["bucket"],
		artifact.Metadata["s3Bucket"],
		artifact.Metadata["provider.extra.s3Bucket"],
	)
	key := firstNonEmpty(artifact.Metadata["aws.s3Key"], artifact.Metadata["key"], artifact.Metadata["providerRef"])
	if key == "" && bucket != "" {
		buildID := strings.TrimSpace(artifact.Metadata["buildID"])
		if buildID != "" {
			format := artifact.Format
			if format == "" {
				format = platform.ImageFormat(firstNonEmpty(artifact.Metadata["format"], string(platform.FormatVMDK)))
			}
			key = localUploadKey(p.config.extraConfig, buildID, format)
		}
	}
	metadata := map[string]string{
		"bucket":     bucket,
		"key":        key,
		"snapshotId": firstNonEmpty(artifact.Metadata["aws.snapshotId"], artifact.Metadata["snapshotId"]),
		"imageId":    firstNonEmpty(artifact.Metadata["aws.imageId"], artifact.Metadata["imageId"], artifact.Metadata["imageRef"]),
		"buildID":    artifact.Metadata["buildID"],
		"imageName":  artifact.Metadata["imageName"],
	}
	return client.CleanupLocalImage(ctx, metadata)
}

func (p *AWSPlugin) HealthCheck(ctx context.Context) error {
	if p.localClient == nil {
		return nil
	}
	return p.localClient.HealthCheck(ctx)
}

// ReconcileRemoteBuild starts or continues an AWS-owned build that produces an
// AMI without running the local QEMU build backend. The operation is deliberately
// modelled as reconciliation, because AWS import/build steps are long-running
// and must survive operator restarts.
func (p *AWSPlugin) ReconcileRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) (*platform.RemoteBuildResult, error) {
	if req == nil {
		return nil, fmt.Errorf("aws plugin: remote build request is required")
	}
	if req.BuildID == "" {
		return nil, fmt.Errorf("aws plugin: remote build request missing build ID")
	}
	if req.Target.Format != string(platform.FormatAMI) {
		return nil, fmt.Errorf("aws plugin: remote build requires target format %q, got %q", platform.FormatAMI, req.Target.Format)
	}
	if req.SourceType == "" {
		return nil, fmt.Errorf("aws plugin: remote build source type is required")
	}
	if firstNonEmpty(req.SourceProviderRef, req.SourceURL) == "" {
		return nil, fmt.Errorf("aws plugin: remote build source providerRef is required")
	}

	client := p.remoteClient
	if client == nil {
		return nil, fmt.Errorf("aws plugin: remote build client is not initialised")
	}

	state, err := client.ReconcileRemoteBuild(ctx, awsRemoteBuildRequest{
		BuildID:           req.BuildID,
		OperationRef:      req.OperationRef,
		ImageName:         req.ImageName,
		Namespace:         req.Namespace,
		Region:            p.config.region,
		SourceType:        req.SourceType,
		SourceURL:         req.SourceURL,
		SourceProviderRef: req.SourceProviderRef,
		SourceChecksum:    req.SourceChecksum,
		OSFamily:          req.OSFamily,
		OSDistribution:    req.OSDistribution,
		OSVersion:         req.OSVersion,
		OSArch:            req.OSArch,
		Target:            req.Target,
		Provisioners:      req.Provisioners,
		GuestAccess:       req.GuestAccess,
		Timeout:           req.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("aws plugin: reconcile remote build: %w", err)
	}
	if state == nil {
		return nil, fmt.Errorf("aws plugin: remote build client returned nil state")
	}
	if state.OperationRef == "" {
		return nil, fmt.Errorf("aws plugin: remote build client returned empty operation reference")
	}

	result := &platform.RemoteBuildResult{
		OperationRef: state.OperationRef,
		Phase:        state.Phase,
		Message:      state.Message,
		Done:         state.Done,
	}
	if state.Done {
		if state.AMIID == "" {
			return nil, fmt.Errorf("aws plugin: completed remote build missing AMI ID")
		}
		result.Hygiene = state.Hygiene
		if result.Hygiene == nil {
			result.Hygiene = &platform.RemoteHygieneResult{
				Status:  "unknown",
				Message: "AWS remote build provider did not return provider-side final image hygiene checks",
				Checks:  []string{"provider-attestation-missing"},
			}
		}
		result.Phase = platform.RemoteBuildPhaseReady
		result.Images = []platform.RemoteImageRef{
			{
				Provider:       p.Name(),
				ProviderConfig: req.Target.ProviderConfigRef.Name,
				Format:         platform.FormatAMI,
				Checksum:       state.Checksum,
				ImageRef: platform.ImageRef{
					ID:       state.AMIID,
					Name:     firstNonEmpty(state.ImageName, req.ImageName),
					Location: p.config.region,
					Tags:     req.Target.Tags,
				},
			},
		}
	}

	return result, nil
}

func (p *AWSPlugin) CleanupRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) error {
	if req == nil {
		return nil
	}
	client := p.remoteClient
	if client == nil {
		return nil
	}
	return client.CleanupRemoteBuild(ctx, awsRemoteBuildRequest{
		BuildID:      req.BuildID,
		OperationRef: req.OperationRef,
		ImageName:    req.ImageName,
		Namespace:    req.Namespace,
		Target:       req.Target,
	})
}

type awsRemoteBuildClient interface {
	ReconcileRemoteBuild(ctx context.Context, req awsRemoteBuildRequest) (*awsRemoteBuildState, error)
	CleanupRemoteBuild(ctx context.Context, req awsRemoteBuildRequest) error
}

type awsRemoteBuildRequest struct {
	BuildID           string
	OperationRef      string
	ImageName         string
	Namespace         string
	Region            string
	SourceType        string
	SourceURL         string
	SourceProviderRef string
	SourceChecksum    string
	OSFamily          platform.OSFamily
	OSDistribution    string
	OSVersion         string
	OSArch            string
	Target            v1alpha1.TargetSpec
	Provisioners      []v1alpha1.ProvisionerSpec
	GuestAccess       *v1alpha1.GuestAccessSpec
	Timeout           time.Duration
}

type awsRemoteBuildState struct {
	OperationRef string
	Phase        platform.RemoteBuildPhase
	Message      string
	Done         bool
	AMIID        string
	ImageName    string
	Checksum     string
	Hygiene      *platform.RemoteHygieneResult
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type awsLocalImageClient interface {
	UploadObject(ctx context.Context, bucket, key, filePath string) error
	RegisterAMI(ctx context.Context, input awsLocalRegisterInput) (*platform.ImageRef, error)
	CleanupLocalImage(ctx context.Context, metadata map[string]string) error
	HealthCheck(ctx context.Context) error
}

type awsSDKLocalImageClient struct {
	region   string
	kmsKeyID string
	ec2      ec2LocalImageAPI
	s3       s3LocalImageAPI
	sts      stsLocalImageAPI
}

type ec2LocalImageAPI interface {
	ImportSnapshot(ctx context.Context, params *ec2.ImportSnapshotInput, optFns ...func(*ec2.Options)) (*ec2.ImportSnapshotOutput, error)
	DescribeImportSnapshotTasks(ctx context.Context, params *ec2.DescribeImportSnapshotTasksInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImportSnapshotTasksOutput, error)
	RegisterImage(ctx context.Context, params *ec2.RegisterImageInput, optFns ...func(*ec2.Options)) (*ec2.RegisterImageOutput, error)
	DescribeImages(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	DeregisterImage(ctx context.Context, params *ec2.DeregisterImageInput, optFns ...func(*ec2.Options)) (*ec2.DeregisterImageOutput, error)
	DeleteSnapshot(ctx context.Context, params *ec2.DeleteSnapshotInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error)
	CancelImportTask(ctx context.Context, params *ec2.CancelImportTaskInput, optFns ...func(*ec2.Options)) (*ec2.CancelImportTaskOutput, error)
}

type s3LocalImageAPI interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	CreateMultipartUpload(ctx context.Context, params *s3.CreateMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(ctx context.Context, params *s3.UploadPartInput, optFns ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(ctx context.Context, params *s3.CompleteMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(ctx context.Context, params *s3.AbortMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

type stsLocalImageAPI interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type awsLocalRegisterInput struct {
	Bucket     string
	Key        string
	BuildID    string
	ImageName  string
	Format     platform.ImageFormat
	OS         platform.OSFamily
	Checksum   string
	Tags       map[string]string
	Timeout    time.Duration
	VolumeSize int32
	KMSKeyID   string
}

func newAWSLocalImageClient(ctx context.Context, cfg awsConfig) (awsLocalImageClient, error) {
	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &awsSDKLocalImageClient{
		region:   cfg.region,
		kmsKeyID: firstNonEmpty(cfg.extraConfig["local.kmsKeyId"], cfg.extraConfig["kmsKeyId"]),
		ec2:      ec2.NewFromConfig(awsCfg),
		s3:       s3.NewFromConfig(awsCfg),
		sts:      sts.NewFromConfig(awsCfg),
	}, nil
}

func (c *awsSDKLocalImageClient) UploadObject(ctx context.Context, bucket, key, filePath string) error {
	file, err := os.Open(filePath) // #nosec G304 -- Artifact path is supplied by the controller-owned build result.
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat artifact: %w", err)
	}
	if info.Size() <= 5*1024*1024*1024 {
		input := &s3.PutObjectInput{
			Bucket: awssdk.String(bucket),
			Key:    awssdk.String(key),
			Body:   file,
		}
		applyS3Encryption(input, c.kmsKeyID)
		_, err = c.s3.PutObject(ctx, input)
		if err != nil {
			return fmt.Errorf("put object: %w", err)
		}
		return nil
	}
	return c.multipartUpload(ctx, bucket, key, file, info.Size())
}

func applyS3Encryption(input *s3.PutObjectInput, kmsKeyID string) {
	if kmsKeyID == "" {
		input.ServerSideEncryption = s3types.ServerSideEncryptionAes256
		return
	}
	input.ServerSideEncryption = s3types.ServerSideEncryptionAwsKms
	input.SSEKMSKeyId = awssdk.String(kmsKeyID)
}

func applyMultipartS3Encryption(input *s3.CreateMultipartUploadInput, kmsKeyID string) {
	if kmsKeyID == "" {
		input.ServerSideEncryption = s3types.ServerSideEncryptionAes256
		return
	}
	input.ServerSideEncryption = s3types.ServerSideEncryptionAwsKms
	input.SSEKMSKeyId = awssdk.String(kmsKeyID)
}

func (c *awsSDKLocalImageClient) multipartUpload(ctx context.Context, bucket, key string, file *os.File, size int64) error {
	const partSize int64 = 64 * 1024 * 1024
	createInput := &s3.CreateMultipartUploadInput{
		Bucket: awssdk.String(bucket),
		Key:    awssdk.String(key),
	}
	applyMultipartS3Encryption(createInput, c.kmsKeyID)
	created, err := c.s3.CreateMultipartUpload(ctx, createInput)
	if err != nil {
		return fmt.Errorf("create multipart upload: %w", err)
	}
	if created.UploadId == nil || *created.UploadId == "" {
		return fmt.Errorf("create multipart upload: AWS returned no upload ID")
	}
	uploadID := *created.UploadId
	completed := make([]s3types.CompletedPart, 0, (size/partSize)+1)
	buffer := make([]byte, partSize)
	partNumber := int32(1)
	abort := true
	defer func() {
		if abort {
			_, _ = c.s3.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
				Bucket:   awssdk.String(bucket),
				Key:      awssdk.String(key),
				UploadId: awssdk.String(uploadID),
			})
		}
	}()
	for {
		n, readErr := io.ReadFull(file, buffer)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return fmt.Errorf("read multipart part %d: %w", partNumber, readErr)
		}
		out, err := c.s3.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     awssdk.String(bucket),
			Key:        awssdk.String(key),
			UploadId:   awssdk.String(uploadID),
			PartNumber: awssdk.Int32(partNumber),
			Body:       bytes.NewReader(buffer[:n]),
		})
		if err != nil {
			return fmt.Errorf("upload part %d: %w", partNumber, err)
		}
		completed = append(completed, s3types.CompletedPart{ETag: out.ETag, PartNumber: awssdk.Int32(partNumber)})
		partNumber++
		if errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	if len(completed) == 0 {
		return fmt.Errorf("multipart upload has no parts")
	}
	_, err = c.s3.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   awssdk.String(bucket),
		Key:      awssdk.String(key),
		UploadId: awssdk.String(uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{
			Parts: completed,
		},
	})
	if err != nil {
		return fmt.Errorf("complete multipart upload: %w", err)
	}
	abort = false
	return nil
}

func (c *awsSDKLocalImageClient) RegisterAMI(ctx context.Context, input awsLocalRegisterInput) (*platform.ImageRef, error) {
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = defaultAWSLocalRegisterTimeout
	}
	existing, err := c.findLocalAMI(ctx, input)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	snapshotID, err := c.importSnapshot(ctx, input, timeout)
	if err != nil {
		return nil, err
	}
	imageID, err := c.registerImage(ctx, input, snapshotID)
	if err != nil {
		_ = c.CleanupLocalImage(ctx, map[string]string{"snapshotId": snapshotID})
		return nil, err
	}
	ref, err := c.waitForAMI(ctx, input, imageID, timeout)
	if err != nil {
		_ = c.CleanupLocalImage(ctx, map[string]string{"imageId": imageID, "snapshotId": snapshotID})
		return nil, err
	}
	return ref, nil
}

func (c *awsSDKLocalImageClient) findLocalAMI(ctx context.Context, input awsLocalRegisterInput) (*platform.ImageRef, error) {
	out, err := c.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"self"},
		Filters: []ec2types.Filter{
			{Name: awssdk.String("name"), Values: []string{input.ImageName}},
			{Name: awssdk.String("tag:imagebuilder.io/build-id"), Values: []string{input.BuildID}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("describe existing local-build AMI: %w", err)
	}
	for _, image := range out.Images {
		if image.ImageId != nil && *image.ImageId != "" {
			return &platform.ImageRef{ID: *image.ImageId, Name: input.ImageName, Location: c.region, Tags: input.Tags}, nil
		}
	}
	return nil, nil
}

func (c *awsSDKLocalImageClient) importSnapshot(ctx context.Context, input awsLocalRegisterInput, timeout time.Duration) (string, error) {
	importInput := &ec2.ImportSnapshotInput{
		ClientToken: awssdk.String(idempotencyToken("import", input.BuildID)),
		Description: awssdk.String("Image Builder local import " + input.BuildID),
		DiskContainer: &ec2types.SnapshotDiskContainer{
			Format: awssdk.String(importSnapshotFormat(input.Format)),
			UserBucket: &ec2types.UserBucket{
				S3Bucket: awssdk.String(input.Bucket),
				S3Key:    awssdk.String(input.Key),
			},
		},
		TagSpecifications: []ec2types.TagSpecification{
			{ResourceType: ec2types.ResourceTypeImportSnapshotTask, Tags: localBuildTags(input)},
		},
		Encrypted: awssdk.Bool(true),
	}
	if input.KMSKeyID != "" {
		importInput.KmsKeyId = awssdk.String(input.KMSKeyID)
	}
	out, err := c.ec2.ImportSnapshot(ctx, importInput)
	if err != nil {
		return "", fmt.Errorf("import snapshot: %w", err)
	}
	if out.ImportTaskId == nil || *out.ImportTaskId == "" {
		return "", fmt.Errorf("import snapshot: AWS returned no import task ID")
	}
	importTaskID := *out.ImportTaskId
	deadline := time.Now().Add(timeout)
	for {
		status, err := c.ec2.DescribeImportSnapshotTasks(ctx, &ec2.DescribeImportSnapshotTasksInput{
			ImportTaskIds: []string{importTaskID},
		})
		if err != nil {
			return "", fmt.Errorf("describe import snapshot task %q: %w", importTaskID, err)
		}
		if len(status.ImportSnapshotTasks) == 0 {
			return "", fmt.Errorf("import snapshot task %q was not found", importTaskID)
		}
		task := status.ImportSnapshotTasks[0]
		if task.SnapshotTaskDetail != nil && task.SnapshotTaskDetail.SnapshotId != nil && *task.SnapshotTaskDetail.SnapshotId != "" {
			if task.SnapshotTaskDetail.Status != nil && strings.EqualFold(*task.SnapshotTaskDetail.Status, "completed") {
				return *task.SnapshotTaskDetail.SnapshotId, nil
			}
		}
		if task.SnapshotTaskDetail != nil && task.SnapshotTaskDetail.Status != nil {
			switch strings.ToLower(*task.SnapshotTaskDetail.Status) {
			case "deleted", "deleting":
				return "", fmt.Errorf("import snapshot task %q was deleted", importTaskID)
			}
		}
		if time.Now().After(deadline) {
			_, _ = c.ec2.CancelImportTask(context.Background(), &ec2.CancelImportTaskInput{ImportTaskId: awssdk.String(importTaskID)})
			return "", fmt.Errorf("import snapshot task %q timed out after %s", importTaskID, timeout)
		}
		select {
		case <-ctx.Done():
			_, _ = c.ec2.CancelImportTask(context.Background(), &ec2.CancelImportTaskInput{ImportTaskId: awssdk.String(importTaskID)})
			return "", ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
}

func (c *awsSDKLocalImageClient) registerImage(ctx context.Context, input awsLocalRegisterInput, snapshotID string) (string, error) {
	ebs := &ec2types.EbsBlockDevice{
		SnapshotId:          awssdk.String(snapshotID),
		DeleteOnTermination: awssdk.Bool(true),
	}
	if input.VolumeSize > 0 {
		ebs.VolumeSize = awssdk.Int32(input.VolumeSize)
	}
	out, err := c.ec2.RegisterImage(ctx, &ec2.RegisterImageInput{
		Name:           awssdk.String(input.ImageName),
		Architecture:   ec2types.ArchitectureValues(awsArchitecture(input)),
		RootDeviceName: awssdk.String(firstNonEmpty(inputName(input.Tags, "rootDeviceName"), "/dev/sda1")),
		BlockDeviceMappings: []ec2types.BlockDeviceMapping{
			{
				DeviceName: awssdk.String(firstNonEmpty(inputName(input.Tags, "rootDeviceName"), "/dev/sda1")),
				Ebs:        ebs,
			},
		},
		VirtualizationType: awssdk.String("hvm"),
		EnaSupport:         awssdk.Bool(true),
		TagSpecifications: []ec2types.TagSpecification{
			{ResourceType: ec2types.ResourceTypeImage, Tags: localBuildTags(input)},
			{ResourceType: ec2types.ResourceTypeSnapshot, Tags: localBuildTags(input)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("register image from snapshot %q: %w", snapshotID, err)
	}
	if out.ImageId == nil || *out.ImageId == "" {
		return "", fmt.Errorf("register image from snapshot %q: AWS returned no image ID", snapshotID)
	}
	return *out.ImageId, nil
}

func (c *awsSDKLocalImageClient) waitForAMI(ctx context.Context, input awsLocalRegisterInput, imageID string, timeout time.Duration) (*platform.ImageRef, error) {
	deadline := time.Now().Add(timeout)
	for {
		out, err := c.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{imageID}})
		if err != nil {
			return nil, fmt.Errorf("describe AMI %q: %w", imageID, err)
		}
		if len(out.Images) == 0 {
			return nil, fmt.Errorf("AMI %q was not found", imageID)
		}
		image := out.Images[0]
		switch image.State {
		case ec2types.ImageStateAvailable:
			return &platform.ImageRef{ID: imageID, Name: input.ImageName, Location: c.region, Tags: input.Tags}, nil
		case ec2types.ImageStateFailed, ec2types.ImageStateError:
			return nil, fmt.Errorf("AMI %q failed with state %q", imageID, image.State)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("AMI %q did not become available after %s", imageID, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
}

func (c *awsSDKLocalImageClient) CleanupLocalImage(ctx context.Context, metadata map[string]string) error {
	if len(metadata) == 0 {
		return nil
	}
	var errs []error
	imageID := firstNonEmpty(metadata["imageId"], metadata["aws.imageId"])
	snapshotID := firstNonEmpty(metadata["snapshotId"], metadata["aws.snapshotId"])
	if imageID == "" && metadata["buildID"] != "" && metadata["imageName"] != "" {
		ref, err := c.findLocalAMI(ctx, awsLocalRegisterInput{
			BuildID:   metadata["buildID"],
			ImageName: metadata["imageName"],
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("find local-build AMI for cleanup: %w", err))
		} else if ref != nil {
			imageID = ref.ID
		}
	}
	if imageID != "" {
		if _, err := c.ec2.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: awssdk.String(imageID)}); err != nil {
			errs = append(errs, fmt.Errorf("deregister AMI %q: %w", imageID, err))
		}
	}
	if snapshotID != "" {
		if _, err := c.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: awssdk.String(snapshotID)}); err != nil {
			errs = append(errs, fmt.Errorf("delete snapshot %q: %w", snapshotID, err))
		}
	}
	bucket := firstNonEmpty(metadata["bucket"], metadata["aws.s3Bucket"])
	key := firstNonEmpty(metadata["key"], metadata["aws.s3Key"])
	if bucket != "" && key != "" {
		if _, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: awssdk.String(bucket), Key: awssdk.String(key)}); err != nil {
			errs = append(errs, fmt.Errorf("delete s3://%s/%s: %w", bucket, key, err))
		}
	}
	return errors.Join(errs...)
}

func (c *awsSDKLocalImageClient) HealthCheck(ctx context.Context) error {
	if c.sts == nil {
		return nil
	}
	_, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("sts get caller identity: %w", err)
	}
	return nil
}

const defaultAWSLocalRegisterTimeout = 2 * time.Hour

func localUploadKey(extra map[string]string, buildID string, format platform.ImageFormat) string {
	prefix := strings.Trim(strings.TrimSpace(firstNonEmpty(extra["s3Prefix"], "imagebuilder")), "/")
	if prefix == "" {
		prefix = "imagebuilder"
	}
	return path.Join(prefix, sanitizeAWSName(buildID), "disk."+string(format))
}

func localRegisterInput(cfg awsConfig, result *platform.UploadResult) (awsLocalRegisterInput, error) {
	bucket := strings.TrimSpace(result.Metadata["bucket"])
	key := strings.TrimSpace(firstNonEmpty(result.Metadata["key"], result.ProviderRef))
	buildID := strings.TrimSpace(result.Metadata["buildID"])
	if bucket == "" || key == "" || buildID == "" {
		return awsLocalRegisterInput{}, fmt.Errorf("aws plugin: upload result metadata must contain bucket, key, and buildID")
	}
	timeout := defaultAWSLocalRegisterTimeout
	if raw := strings.TrimSpace(cfg.extraConfig["registerTimeout"]); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return awsLocalRegisterInput{}, fmt.Errorf("aws plugin: extra registerTimeout must be a duration: %w", err)
		}
		timeout = parsed
	}
	volumeSize := int32(0)
	if raw := strings.TrimSpace(cfg.extraConfig["rootVolumeSizeGiB"]); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed <= 0 {
			return awsLocalRegisterInput{}, fmt.Errorf("aws plugin: extra rootVolumeSizeGiB must be a positive integer")
		}
		volumeSize = int32(parsed)
	}
	kmsKeyID := strings.TrimSpace(firstNonEmpty(cfg.extraConfig["local.kmsKeyId"], cfg.extraConfig["kmsKeyId"]))
	return awsLocalRegisterInput{
		Bucket:     bucket,
		Key:        key,
		BuildID:    buildID,
		ImageName:  sanitizeAWSName(firstNonEmpty(result.Metadata["imageName"], "imagebuilder-"+buildID)),
		Format:     platform.ImageFormat(firstNonEmpty(result.Metadata["format"], string(platform.FormatVMDK))),
		OS:         platform.OSFamily(result.Metadata["os"]),
		Checksum:   result.Metadata["checksum"],
		Tags:       localRegisterTags(cfg, result.Metadata),
		Timeout:    timeout,
		VolumeSize: volumeSize,
		KMSKeyID:   kmsKeyID,
	}, nil
}

func localRegisterTags(cfg awsConfig, metadata map[string]string) map[string]string {
	tags := map[string]string{"imagebuilder.io/provider-config": cfg.providerConfigName}
	for key, value := range metadata {
		if strings.HasPrefix(key, "target.tag.") {
			tags[strings.TrimPrefix(key, "target.tag.")] = value
		}
	}
	return tags
}

func importSnapshotFormat(format platform.ImageFormat) string {
	switch format {
	case platform.FormatRaw:
		return "RAW"
	case platform.FormatVHD:
		return "VHD"
	default:
		return "VMDK"
	}
}

func awsArchitecture(input awsLocalRegisterInput) string {
	if strings.EqualFold(input.Tags["arch"], "arm64") {
		return string(ec2types.ArchitectureValuesArm64)
	}
	return string(ec2types.ArchitectureValuesX8664)
}

func inputName(values map[string]string, key string) string {
	if values == nil {
		return ""
	}
	return values[key]
}

func localBuildTags(input awsLocalRegisterInput) []ec2types.Tag {
	tags := []ec2types.Tag{
		{Key: awssdk.String("imagebuilder.io/build-id"), Value: awssdk.String(input.BuildID)},
		{Key: awssdk.String("Name"), Value: awssdk.String(input.ImageName)},
	}
	for key, value := range input.Tags {
		if strings.HasPrefix(strings.ToLower(key), "aws:") {
			continue
		}
		tags = append(tags, ec2types.Tag{Key: awssdk.String(key), Value: awssdk.String(value)})
	}
	return tags
}
