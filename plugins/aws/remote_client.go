package aws

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strconv"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

type awsSDKRemoteBuildClient struct {
	region   string
	ec2      ec2RemoteBuildAPI
	ssm      ssmRemoteBuildAPI
	s3       *s3.Client
	sts      *sts.Client
	settings awsRemoteBuildSettings
	log      *slog.Logger
}

type ec2RemoteBuildAPI interface {
	RunInstances(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	StopInstances(ctx context.Context, params *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
	TerminateInstances(ctx context.Context, params *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	CreateImage(ctx context.Context, params *ec2.CreateImageInput, optFns ...func(*ec2.Options)) (*ec2.CreateImageOutput, error)
	DescribeImages(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	DescribeSecurityGroups(ctx context.Context, params *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	DeregisterImage(ctx context.Context, params *ec2.DeregisterImageInput, optFns ...func(*ec2.Options)) (*ec2.DeregisterImageOutput, error)
	DeleteSnapshot(ctx context.Context, params *ec2.DeleteSnapshotInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error)
}

type ssmRemoteBuildAPI interface {
	DescribeInstanceInformation(ctx context.Context, params *ssm.DescribeInstanceInformationInput, optFns ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error)
	SendCommand(ctx context.Context, params *ssm.SendCommandInput, optFns ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	GetCommandInvocation(ctx context.Context, params *ssm.GetCommandInvocationInput, optFns ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
}

type awsRemoteBuildSettings struct {
	InstanceType       string
	SubnetID           string
	SecurityGroupIDs   []string
	IAMProfileName     string
	KeyName            string
	AllowSSHKey        bool
	AllowPublicIngress bool
	KMSKeyID           string
	RootVolumeSizeGiB  int32
}

func newAWSRemoteBuildClient(ctx context.Context, cfg awsConfig, log *slog.Logger) (awsRemoteBuildClient, error) {
	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return &awsSDKRemoteBuildClient{
		region:   cfg.region,
		ec2:      ec2.NewFromConfig(awsCfg),
		ssm:      ssm.NewFromConfig(awsCfg),
		s3:       s3.NewFromConfig(awsCfg),
		sts:      sts.NewFromConfig(awsCfg),
		settings: remoteBuildSettingsFromExtra(cfg.extraConfig),
		log:      log.With(slog.String("component", "aws-remote-build-client")),
	}, nil
}

func loadAWSConfig(ctx context.Context, cfg awsConfig) (awssdk.Config, error) {
	if cfg.insecure {
		return awssdk.Config{}, fmt.Errorf("insecure TLS verification is not supported by the AWS provider")
	}

	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.region),
	}
	if cfg.accessKeyID != "" && cfg.secretAccessKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.accessKeyID,
			cfg.secretAccessKey,
			cfg.sessionToken,
		)))
	}
	if cfg.endpoint != "" {
		options = append(options, awsconfig.WithEndpointResolverWithOptions(awsEndpointResolver(cfg.endpoint)))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return awssdk.Config{}, fmt.Errorf("load AWS SDK config: %w", err)
	}
	if cfg.roleARN != "" {
		awsCfg.Credentials = awssdk.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(awsCfg), cfg.roleARN))
	}

	return awsCfg, nil
}

func awsEndpointResolver(endpoint string) awssdk.EndpointResolverWithOptions {
	return awssdk.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (awssdk.Endpoint, error) {
		return awssdk.Endpoint{
			URL:               endpoint,
			SigningRegion:     region,
			HostnameImmutable: true,
		}, nil
	})
}

func (c *awsSDKRemoteBuildClient) ReconcileRemoteBuild(ctx context.Context, req awsRemoteBuildRequest) (*awsRemoteBuildState, error) {
	if req.SourceType != "cloud-image" && req.SourceType != "marketplace" {
		return nil, fmt.Errorf("AWS remote build supports source type cloud-image or marketplace, got %q", req.SourceType)
	}
	sourceAMI := firstNonEmpty(req.SourceProviderRef, req.SourceURL)
	if !strings.HasPrefix(sourceAMI, "ami-") {
		return nil, fmt.Errorf("AWS remote build source providerRef must be an AMI ID for source type %q", req.SourceType)
	}
	if err := c.validateRemoteBuildSettings(ctx, req); err != nil {
		return nil, err
	}

	ref, err := parseAWSRemoteOperationRef(req.OperationRef)
	if err != nil {
		return nil, err
	}
	if ref.ImageID != "" {
		return c.reconcileImage(ctx, req, ref)
	}
	if ref.InstanceID == "" {
		return c.startInstance(ctx, req)
	}
	return c.reconcileInstance(ctx, req, ref)
}

func (c *awsSDKRemoteBuildClient) CleanupRemoteBuild(ctx context.Context, req awsRemoteBuildRequest) error {
	ref, err := parseAWSRemoteOperationRef(req.OperationRef)
	if err != nil {
		return err
	}
	if ref.BuildID == "" {
		ref.BuildID = req.BuildID
	}
	var errs []error
	if ref.ImageID != "" || ref.InstanceID != "" {
		if err := c.cleanupRemoteBuild(ctx, ref); err != nil {
			errs = append(errs, err)
		}
	}
	if req.BuildID != "" {
		if err := c.cleanupRemoteResourcesByBuildID(ctx, req); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *awsSDKRemoteBuildClient) startInstance(ctx context.Context, req awsRemoteBuildRequest) (*awsRemoteBuildState, error) {
	sourceAMI := firstNonEmpty(req.SourceProviderRef, req.SourceURL)
	input := &ec2.RunInstancesInput{
		ImageId:      awssdk.String(sourceAMI),
		InstanceType: ec2types.InstanceType(c.settings.InstanceType),
		ClientToken:  awssdk.String(idempotencyToken("imagebuilder", req.BuildID)),
		MinCount:     awssdk.Int32(1),
		MaxCount:     awssdk.Int32(1),
		SubnetId:     awssdk.String(c.settings.SubnetID),
		SecurityGroupIds: append([]string(nil),
			c.settings.SecurityGroupIDs...,
		),
		InstanceInitiatedShutdownBehavior: ec2types.ShutdownBehaviorStop,
		MetadataOptions: &ec2types.InstanceMetadataOptionsRequest{
			HttpTokens: ec2types.HttpTokensStateRequired,
		},
		TagSpecifications: remoteBuildTagSpecifications(req),
	}
	if c.settings.IAMProfileName != "" {
		input.IamInstanceProfile = &ec2types.IamInstanceProfileSpecification{Name: awssdk.String(c.settings.IAMProfileName)}
	}
	if c.settings.KeyName != "" {
		input.KeyName = awssdk.String(c.settings.KeyName)
	}
	if c.settings.RootVolumeSizeGiB > 0 || c.settings.KMSKeyID != "" {
		root := ec2types.EbsBlockDevice{Encrypted: awssdk.Bool(true)}
		if c.settings.RootVolumeSizeGiB > 0 {
			root.VolumeSize = awssdk.Int32(c.settings.RootVolumeSizeGiB)
		}
		if c.settings.KMSKeyID != "" {
			root.KmsKeyId = awssdk.String(c.settings.KMSKeyID)
		}
		input.BlockDeviceMappings = []ec2types.BlockDeviceMapping{
			{
				DeviceName: awssdk.String("/dev/sda1"),
				Ebs:        &root,
			},
		}
	}

	out, err := c.ec2.RunInstances(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("run EC2 build instance: %w", err)
	}
	if len(out.Instances) == 0 || out.Instances[0].InstanceId == nil || *out.Instances[0].InstanceId == "" {
		return nil, fmt.Errorf("run EC2 build instance: AWS returned no instance ID")
	}

	instanceID := *out.Instances[0].InstanceId
	return &awsRemoteBuildState{
		OperationRef: awsRemoteOperationRef{BuildID: req.BuildID, InstanceID: instanceID}.String(),
		Phase:        platform.RemoteBuildPhaseBooting,
		Message:      "AWS build instance started",
	}, nil
}

func (c *awsSDKRemoteBuildClient) reconcileInstance(ctx context.Context, req awsRemoteBuildRequest, ref awsRemoteOperationRef) (*awsRemoteBuildState, error) {
	out, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{ref.InstanceID},
	})
	if err != nil {
		return nil, fmt.Errorf("describe EC2 build instance %q: %w", ref.InstanceID, err)
	}
	instance, ok := firstEC2Instance(out)
	if !ok {
		return nil, fmt.Errorf("EC2 build instance %q was not found", ref.InstanceID)
	}

	state := instance.State.Name
	switch state {
	case ec2types.InstanceStateNamePending:
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseBooting,
			Message:      "AWS build instance is pending",
		}, nil
	case ec2types.InstanceStateNameRunning:
		if len(req.Provisioners) > 0 && ref.ProvisionerIndex < len(req.Provisioners) {
			return c.reconcileProvisioning(ctx, req, ref)
		}
		return c.stopBuildInstance(ctx, ref)
	case ec2types.InstanceStateNameStopping:
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseSanitizing,
			Message:      "AWS build instance is stopping",
		}, nil
	case ec2types.InstanceStateNameStopped:
		if len(req.Provisioners) > 0 && ref.ProvisionerIndex < len(req.Provisioners) {
			return c.failRemoteBuild(ctx, ref, "EC2 build instance %q stopped before provisioning completed", ref.InstanceID)
		}
		imageID, err := c.createImage(ctx, req, ref.InstanceID)
		if err != nil {
			return c.failRemoteBuild(ctx, ref, "%v", err)
		}
		ref.ImageID = imageID
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseRegistering,
			Message:      "AWS AMI creation started",
		}, nil
	case ec2types.InstanceStateNameShuttingDown, ec2types.InstanceStateNameTerminated:
		return c.failRemoteBuild(ctx, ref, "EC2 build instance %q reached terminal state %q before image creation", ref.InstanceID, state)
	default:
		return nil, fmt.Errorf("EC2 build instance %q has unsupported state %q", ref.InstanceID, state)
	}
}

func (c *awsSDKRemoteBuildClient) reconcileProvisioning(ctx context.Context, req awsRemoteBuildRequest, ref awsRemoteOperationRef) (*awsRemoteBuildState, error) {
	ready, err := c.ssmReady(ctx, ref.InstanceID)
	if err != nil {
		return nil, err
	}
	if !ready {
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseReadiness,
			Message:      "waiting for AWS SSM readiness",
		}, nil
	}

	if ref.ProvisionerIndex >= len(req.Provisioners) {
		return c.stopBuildInstance(ctx, ref)
	}
	spec := req.Provisioners[ref.ProvisionerIndex]
	if ref.CommandID == "" {
		commandID, err := c.startProvisionerCommand(ctx, req, ref, spec)
		if err != nil {
			return c.failRemoteBuild(ctx, ref, "%v", err)
		}
		ref.CommandID = commandID
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseProvisioning,
			Message:      fmt.Sprintf("AWS SSM provisioner %d started", ref.ProvisionerIndex),
		}, nil
	}

	status, err := c.ssm.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
		CommandId:  awssdk.String(ref.CommandID),
		InstanceId: awssdk.String(ref.InstanceID),
	})
	if err != nil {
		return nil, fmt.Errorf("get AWS SSM command invocation for provisioner %d: %w", ref.ProvisionerIndex, err)
	}
	switch status.Status {
	case ssmtypes.CommandInvocationStatusPending, ssmtypes.CommandInvocationStatusInProgress, ssmtypes.CommandInvocationStatusDelayed:
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseProvisioning,
			Message:      fmt.Sprintf("AWS SSM provisioner %d is %s", ref.ProvisionerIndex, status.Status),
		}, nil
	case ssmtypes.CommandInvocationStatusSuccess:
		ref.ProvisionerIndex++
		ref.CommandID = ""
		if ref.ProvisionerIndex >= len(req.Provisioners) {
			return c.stopBuildInstance(ctx, ref)
		}
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseProvisioning,
			Message:      fmt.Sprintf("AWS SSM provisioner %d completed", ref.ProvisionerIndex-1),
		}, nil
	case ssmtypes.CommandInvocationStatusCancelled, ssmtypes.CommandInvocationStatusCancelling, ssmtypes.CommandInvocationStatusFailed, ssmtypes.CommandInvocationStatusTimedOut:
		return c.failRemoteBuild(ctx, ref, "AWS SSM provisioner %d failed with status %q", ref.ProvisionerIndex, status.Status)
	default:
		return c.failRemoteBuild(ctx, ref, "AWS SSM provisioner %d has unsupported status %q", ref.ProvisionerIndex, status.Status)
	}
}

func (c *awsSDKRemoteBuildClient) ssmReady(ctx context.Context, instanceID string) (bool, error) {
	if c.ssm == nil {
		return false, fmt.Errorf("AWS SSM client is not configured")
	}
	out, err := c.ssm.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
		Filters: []ssmtypes.InstanceInformationStringFilter{
			{Key: awssdk.String("InstanceIds"), Values: []string{instanceID}},
		},
	})
	if err != nil {
		return false, fmt.Errorf("describe AWS SSM instance information for %q: %w", instanceID, err)
	}
	for _, info := range out.InstanceInformationList {
		if info.InstanceId != nil && *info.InstanceId == instanceID && info.PingStatus == ssmtypes.PingStatusOnline {
			return true, nil
		}
	}
	return false, nil
}

func (c *awsSDKRemoteBuildClient) startProvisionerCommand(ctx context.Context, req awsRemoteBuildRequest, ref awsRemoteOperationRef, spec v1alpha1.ProvisionerSpec) (string, error) {
	documentName, commands, err := ssmProvisionerCommand(req, spec)
	if err != nil {
		return "", err
	}
	out, err := c.ssm.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName: awssdk.String(documentName),
		InstanceIds:  []string{ref.InstanceID},
		Parameters:   map[string][]string{"commands": commands},
		Comment:      awssdk.String(fmt.Sprintf("imagebuilder %s provisioner %d", req.BuildID, ref.ProvisionerIndex)),
	})
	if err != nil {
		return "", fmt.Errorf("send AWS SSM command for provisioner %d: %w", ref.ProvisionerIndex, err)
	}
	if out.Command == nil || out.Command.CommandId == nil || *out.Command.CommandId == "" {
		return "", fmt.Errorf("send AWS SSM command for provisioner %d: AWS returned no command ID", ref.ProvisionerIndex)
	}
	return *out.Command.CommandId, nil
}

func (c *awsSDKRemoteBuildClient) stopBuildInstance(ctx context.Context, ref awsRemoteOperationRef) (*awsRemoteBuildState, error) {
	if _, err := c.ec2.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{ref.InstanceID}}); err != nil {
		return nil, fmt.Errorf("stop EC2 build instance %q: %w", ref.InstanceID, err)
	}
	return &awsRemoteBuildState{
		OperationRef: ref.String(),
		Phase:        platform.RemoteBuildPhaseSanitizing,
		Message:      "AWS build instance stopped for image finalization",
	}, nil
}

func (c *awsSDKRemoteBuildClient) createImage(ctx context.Context, req awsRemoteBuildRequest, instanceID string) (string, error) {
	existingImageID, err := c.findExistingRemoteImage(ctx, req)
	if err != nil {
		return "", err
	}
	if existingImageID != "" {
		return existingImageID, nil
	}

	out, err := c.ec2.CreateImage(ctx, &ec2.CreateImageInput{
		InstanceId:        awssdk.String(instanceID),
		Name:              awssdk.String(remoteImageName(req)),
		Description:       awssdk.String("Image Builder remote build " + req.BuildID),
		NoReboot:          awssdk.Bool(true),
		TagSpecifications: remoteBuildImageTagSpecifications(req),
	})
	if err != nil {
		return "", fmt.Errorf("create AWS AMI from instance %q: %w", instanceID, err)
	}
	if out.ImageId == nil || *out.ImageId == "" {
		return "", fmt.Errorf("create AWS AMI from instance %q: AWS returned no image ID", instanceID)
	}
	return *out.ImageId, nil
}

func (c *awsSDKRemoteBuildClient) findExistingRemoteImage(ctx context.Context, req awsRemoteBuildRequest) (string, error) {
	out, err := c.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"self"},
		Filters: []ec2types.Filter{
			{Name: awssdk.String("name"), Values: []string{remoteImageName(req)}},
			{Name: awssdk.String("tag:imagebuilder.io/build-id"), Values: []string{req.BuildID}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("describe existing AWS AMI for build %q: %w", req.BuildID, err)
	}
	for _, image := range out.Images {
		if image.ImageId != nil && *image.ImageId != "" {
			return *image.ImageId, nil
		}
	}
	return "", nil
}

func (c *awsSDKRemoteBuildClient) reconcileImage(ctx context.Context, req awsRemoteBuildRequest, ref awsRemoteOperationRef) (*awsRemoteBuildState, error) {
	out, err := c.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{
		ImageIds: []string{ref.ImageID},
	})
	if err != nil {
		return nil, fmt.Errorf("describe AWS AMI %q: %w", ref.ImageID, err)
	}
	if len(out.Images) == 0 {
		return nil, fmt.Errorf("AWS AMI %q was not found", ref.ImageID)
	}
	image := out.Images[0]
	switch image.State {
	case ec2types.ImageStateAvailable:
		if ref.InstanceID != "" {
			if _, err := c.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{ref.InstanceID}}); err != nil {
				return nil, fmt.Errorf("terminate EC2 build instance %q after AMI availability: %w", ref.InstanceID, err)
			}
		}
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseReady,
			Message:      "AWS AMI is available",
			Done:         true,
			AMIID:        ref.ImageID,
			ImageName:    remoteImageName(req),
		}, nil
	case ec2types.ImageStatePending:
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseRegistering,
			Message:      "AWS AMI is pending",
		}, nil
	case ec2types.ImageStateFailed, ec2types.ImageStateError:
		return c.failRemoteBuild(ctx, ref, "AWS AMI %q failed with state %q", ref.ImageID, image.State)
	default:
		return &awsRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseRegistering,
			Message:      fmt.Sprintf("AWS AMI state is %q", image.State),
		}, nil
	}
}

func (c *awsSDKRemoteBuildClient) failRemoteBuild(ctx context.Context, ref awsRemoteOperationRef, format string, args ...interface{}) (*awsRemoteBuildState, error) {
	buildErr := fmt.Errorf(format, args...)
	if err := c.cleanupRemoteBuild(ctx, ref); err != nil {
		return nil, fmt.Errorf("%w; cleanup failed: %v", buildErr, err)
	}
	return nil, buildErr
}

func (c *awsSDKRemoteBuildClient) cleanupRemoteBuild(ctx context.Context, ref awsRemoteOperationRef) error {
	var errs []error
	if ref.ImageID != "" {
		if err := c.cleanupRemoteImage(ctx, ref.ImageID); err != nil {
			errs = append(errs, err)
		}
	}
	if ref.InstanceID != "" {
		if _, err := c.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{ref.InstanceID}}); err != nil {
			errs = append(errs, fmt.Errorf("terminate EC2 build instance %q: %w", ref.InstanceID, err))
		}
	}
	return errors.Join(errs...)
}

func (c *awsSDKRemoteBuildClient) cleanupRemoteResourcesByBuildID(ctx context.Context, req awsRemoteBuildRequest) error {
	var errs []error
	out, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: awssdk.String("tag:imagebuilder.io/build-id"), Values: []string{req.BuildID}},
			{Name: awssdk.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("describe remote build instances for build %q: %w", req.BuildID, err))
	} else {
		var instanceIDs []string
		for _, reservation := range out.Reservations {
			for _, instance := range reservation.Instances {
				if instance.InstanceId != nil && *instance.InstanceId != "" {
					instanceIDs = append(instanceIDs, *instance.InstanceId)
				}
			}
		}
		if len(instanceIDs) > 0 {
			if _, err := c.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: instanceIDs}); err != nil {
				errs = append(errs, fmt.Errorf("terminate remote build instances for build %q: %w", req.BuildID, err))
			}
		}
	}

	imageID, err := c.findExistingRemoteImage(ctx, req)
	if err != nil {
		errs = append(errs, err)
	} else if imageID != "" {
		if err := c.cleanupRemoteImage(ctx, imageID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *awsSDKRemoteBuildClient) cleanupRemoteImage(ctx context.Context, imageID string) error {
	var snapshotIDs []string
	out, err := c.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{imageID}})
	if err != nil {
		return fmt.Errorf("describe AWS AMI %q for cleanup: %w", imageID, err)
	}
	for _, image := range out.Images {
		for _, mapping := range image.BlockDeviceMappings {
			if mapping.Ebs != nil && mapping.Ebs.SnapshotId != nil && *mapping.Ebs.SnapshotId != "" {
				snapshotIDs = append(snapshotIDs, *mapping.Ebs.SnapshotId)
			}
		}
	}
	var errs []error
	if _, err := c.ec2.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: awssdk.String(imageID)}); err != nil {
		errs = append(errs, fmt.Errorf("deregister AWS AMI %q: %w", imageID, err))
	}
	for _, snapshotID := range snapshotIDs {
		if _, err := c.ec2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: awssdk.String(snapshotID)}); err != nil {
			errs = append(errs, fmt.Errorf("delete AWS snapshot %q: %w", snapshotID, err))
		}
	}
	return errors.Join(errs...)
}

func (c *awsSDKRemoteBuildClient) validateRemoteBuildSettings(ctx context.Context, req awsRemoteBuildRequest) error {
	if err := c.settings.validate(req); err != nil {
		return err
	}
	if err := c.validateSecurityGroups(ctx); err != nil {
		return err
	}
	return nil
}

type awsRemoteOperationRef struct {
	BuildID          string
	InstanceID       string
	ImageID          string
	ProvisionerIndex int
	CommandID        string
}

func (r awsRemoteOperationRef) String() string {
	values := url.Values{}
	if r.InstanceID != "" {
		values.Set("instanceId", r.InstanceID)
	}
	if r.ImageID != "" {
		values.Set("imageId", r.ImageID)
	}
	if r.ProvisionerIndex > 0 {
		values.Set("provisionerIndex", strconv.Itoa(r.ProvisionerIndex))
	}
	if r.CommandID != "" {
		values.Set("commandId", r.CommandID)
	}
	u := url.URL{Scheme: "aws", Host: "remote-build", Path: "/" + r.BuildID, RawQuery: values.Encode()}
	return u.String()
}

func parseAWSRemoteOperationRef(value string) (awsRemoteOperationRef, error) {
	if value == "" {
		return awsRemoteOperationRef{}, nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return awsRemoteOperationRef{}, fmt.Errorf("parse AWS remote operation ref: %w", err)
	}
	if u.Scheme != "aws" || u.Host != "remote-build" {
		return awsRemoteOperationRef{}, fmt.Errorf("invalid AWS remote operation ref %q", value)
	}
	index := 0
	if rawIndex := u.Query().Get("provisionerIndex"); rawIndex != "" {
		parsed, err := strconv.Atoi(rawIndex)
		if err != nil || parsed < 0 {
			return awsRemoteOperationRef{}, fmt.Errorf("invalid AWS remote operation ref provisionerIndex %q", rawIndex)
		}
		index = parsed
	}
	return awsRemoteOperationRef{
		BuildID:          strings.TrimPrefix(u.Path, "/"),
		InstanceID:       u.Query().Get("instanceId"),
		ImageID:          u.Query().Get("imageId"),
		ProvisionerIndex: index,
		CommandID:        u.Query().Get("commandId"),
	}, nil
}

func ssmProvisionerCommand(req awsRemoteBuildRequest, spec v1alpha1.ProvisionerSpec) (string, []string, error) {
	switch spec.Type {
	case "shell":
		if req.OSFamily != platform.OSFamilyLinux {
			return "", nil, fmt.Errorf("shell provisioner requires linux OS family for AWS SSM remote build")
		}
		if strings.TrimSpace(spec.Inline) == "" {
			return "", nil, fmt.Errorf("shell provisioner requires inline content for AWS SSM remote build")
		}
		return "AWS-RunShellScript", []string{spec.Inline}, nil
	case "powershell":
		if req.OSFamily != platform.OSFamilyWindows {
			return "", nil, fmt.Errorf("powershell provisioner requires windows OS family for AWS SSM remote build")
		}
		if strings.TrimSpace(spec.Inline) == "" {
			return "", nil, fmt.Errorf("powershell provisioner requires inline content for AWS SSM remote build")
		}
		return "AWS-RunPowerShellScript", []string{spec.Inline}, nil
	case "file":
		if strings.TrimSpace(spec.Inline) == "" {
			return "", nil, fmt.Errorf("file provisioner requires inline content for AWS SSM remote build")
		}
		if len(spec.Args) != 1 || strings.TrimSpace(spec.Args[0]) == "" {
			return "", nil, fmt.Errorf("file provisioner requires destination path in args[0] for AWS SSM remote build")
		}
		return ssmFileProvisionerCommand(req, spec.Inline, spec.Args[0])
	default:
		return "", nil, fmt.Errorf("provisioner type %q is not supported by AWS SSM remote build", spec.Type)
	}
}

func ssmFileProvisionerCommand(req awsRemoteBuildRequest, content, destination string) (string, []string, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	switch req.OSFamily {
	case platform.OSFamilyLinux:
		dir := path.Dir(destination)
		command := strings.Join([]string{
			"set -eu",
			"install -d -m 0755 " + shellQuote(dir),
			"base64 -d > " + shellQuote(destination) + " <<'__IMAGEBUILDER_FILE__'",
			encoded,
			"__IMAGEBUILDER_FILE__",
			"chmod 0600 " + shellQuote(destination),
		}, "\n")
		return "AWS-RunShellScript", []string{command}, nil
	case platform.OSFamilyWindows:
		command := strings.Join([]string{
			"$destination = " + powershellQuote(destination),
			"$parent = Split-Path -Parent $destination",
			"if ($parent) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }",
			"$bytes = [Convert]::FromBase64String(" + powershellQuote(encoded) + ")",
			"[IO.File]::WriteAllBytes($destination, $bytes)",
		}, "\n")
		return "AWS-RunPowerShellScript", []string{command}, nil
	default:
		return "", nil, fmt.Errorf("file provisioner requires linux or windows OS family for AWS SSM remote build")
	}
}

func remoteBuildSettingsFromExtra(extra map[string]string) awsRemoteBuildSettings {
	return awsRemoteBuildSettings{
		InstanceType:       firstNonEmpty(extra["remote.instanceType"], extra["instanceType"]),
		SubnetID:           firstNonEmpty(extra["remote.subnetId"], extra["subnetId"]),
		SecurityGroupIDs:   splitCSV(firstNonEmpty(extra["remote.securityGroupIds"], extra["securityGroupIds"])),
		IAMProfileName:     firstNonEmpty(extra["remote.iamInstanceProfile"], extra["iamInstanceProfile"]),
		KeyName:            firstNonEmpty(extra["remote.keyName"], extra["keyName"]),
		AllowSSHKey:        parseBool(firstNonEmpty(extra["remote.allowSshKey"], extra["allowSshKey"])),
		AllowPublicIngress: parseBool(firstNonEmpty(extra["remote.allowPublicIngress"], extra["allowPublicIngress"])),
		KMSKeyID:           firstNonEmpty(extra["remote.kmsKeyId"], extra["kmsKeyId"]),
		RootVolumeSizeGiB:  parseInt32(firstNonEmpty(extra["remote.rootVolumeSizeGiB"], extra["rootVolumeSizeGiB"])),
	}
}

func (s awsRemoteBuildSettings) validate(req awsRemoteBuildRequest) error {
	if s.InstanceType == "" {
		return fmt.Errorf("AWS remote build requires ProviderConfig extra remote.instanceType")
	}
	if s.SubnetID == "" {
		return fmt.Errorf("AWS remote build requires ProviderConfig extra remote.subnetId")
	}
	if len(s.SecurityGroupIDs) == 0 {
		return fmt.Errorf("AWS remote build requires ProviderConfig extra remote.securityGroupIds")
	}
	if s.KMSKeyID == "" {
		return fmt.Errorf("AWS remote build requires ProviderConfig extra remote.kmsKeyId")
	}
	if s.KeyName != "" && !s.AllowSSHKey {
		return fmt.Errorf("AWS remote build forbids remote.keyName unless remote.allowSshKey=true")
	}
	if len(req.Provisioners) > 0 && s.IAMProfileName == "" {
		return fmt.Errorf("AWS remote build requires ProviderConfig extra remote.iamInstanceProfile when provisioners are configured")
	}
	return nil
}

func (c *awsSDKRemoteBuildClient) validateSecurityGroups(ctx context.Context) error {
	if c.settings.AllowPublicIngress {
		return nil
	}
	out, err := c.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: append([]string(nil), c.settings.SecurityGroupIDs...),
	})
	if err != nil {
		return fmt.Errorf("describe AWS security groups: %w", err)
	}
	for _, group := range out.SecurityGroups {
		groupID := ""
		if group.GroupId != nil {
			groupID = *group.GroupId
		}
		for _, permission := range group.IpPermissions {
			if exposesRemoteAdminPort(permission) && hasPublicCIDR(permission) {
				return fmt.Errorf("AWS remote build security group %q exposes SSH/WinRM to the public internet", groupID)
			}
		}
	}
	return nil
}

func exposesRemoteAdminPort(permission ec2types.IpPermission) bool {
	if permission.IpProtocol != nil && *permission.IpProtocol == "-1" {
		return true
	}
	if permission.FromPort == nil || permission.ToPort == nil {
		return false
	}
	for _, port := range []int32{22, 5985, 5986} {
		if *permission.FromPort <= port && *permission.ToPort >= port {
			return true
		}
	}
	return false
}

func hasPublicCIDR(permission ec2types.IpPermission) bool {
	for _, cidr := range permission.IpRanges {
		if cidr.CidrIp != nil && *cidr.CidrIp == "0.0.0.0/0" {
			return true
		}
	}
	for _, cidr := range permission.Ipv6Ranges {
		if cidr.CidrIpv6 != nil && *cidr.CidrIpv6 == "::/0" {
			return true
		}
	}
	return false
}

func remoteBuildTagSpecifications(req awsRemoteBuildRequest) []ec2types.TagSpecification {
	tags := remoteBuildTags(req)
	return []ec2types.TagSpecification{
		{ResourceType: ec2types.ResourceTypeInstance, Tags: tags},
		{ResourceType: ec2types.ResourceTypeVolume, Tags: tags},
	}
}

func remoteBuildImageTagSpecifications(req awsRemoteBuildRequest) []ec2types.TagSpecification {
	return []ec2types.TagSpecification{
		{ResourceType: ec2types.ResourceTypeImage, Tags: remoteBuildTags(req)},
		{ResourceType: ec2types.ResourceTypeSnapshot, Tags: remoteBuildTags(req)},
	}
}

func remoteBuildTags(req awsRemoteBuildRequest) []ec2types.Tag {
	tags := []ec2types.Tag{
		{Key: awssdk.String("imagebuilder.io/build-id"), Value: awssdk.String(req.BuildID)},
		{Key: awssdk.String("imagebuilder.io/namespace"), Value: awssdk.String(req.Namespace)},
		{Key: awssdk.String("imagebuilder.io/image-name"), Value: awssdk.String(req.ImageName)},
		{Key: awssdk.String("Name"), Value: awssdk.String(remoteImageName(req))},
	}
	for key, value := range req.Target.Tags {
		if strings.HasPrefix(strings.ToLower(key), "aws:") {
			continue
		}
		tags = append(tags, ec2types.Tag{Key: awssdk.String(key), Value: awssdk.String(value)})
	}
	return tags
}

func remoteImageName(req awsRemoteBuildRequest) string {
	return firstNonEmpty(req.ImageName, "imagebuilder") + "-" + sanitizeAWSName(req.BuildID)
}

func sanitizeAWSName(value string) string {
	replacer := strings.NewReplacer("/", "-", ":", "-", "_", "-")
	value = replacer.Replace(value)
	value = strings.Trim(value, "-")
	if value == "" {
		return "build"
	}
	return value
}

func firstEC2Instance(out *ec2.DescribeInstancesOutput) (ec2types.Instance, bool) {
	for _, reservation := range out.Reservations {
		for _, instance := range reservation.Instances {
			return instance, true
		}
	}
	return ec2types.Instance{}, false
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseInt32(value string) int32 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed < 0 {
		return 0
	}
	return int32(parsed)
}

func parseBool(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && parsed
}

func idempotencyToken(prefix, buildID string) string {
	sum := sha256.Sum256([]byte(buildID))
	return prefix + "-" + hex.EncodeToString(sum[:24])
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func powershellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
