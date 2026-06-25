package aws

import (
	"context"
	"errors"
	"strings"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

type fakeEC2RemoteBuildAPI struct {
	runInput               *ec2.RunInstancesInput
	stopInput              *ec2.StopInstancesInput
	terminateInput         *ec2.TerminateInstancesInput
	createImageInput       *ec2.CreateImageInput
	describeImagesInput    *ec2.DescribeImagesInput
	marketplaceImagesInput *ec2.DescribeImagesInput
	describeSGInput        *ec2.DescribeSecurityGroupsInput
	deregisterInput        *ec2.DeregisterImageInput
	deleteSnapshotInputs   []*ec2.DeleteSnapshotInput

	instanceState  ec2types.InstanceStateName
	imageState     ec2types.ImageState
	existingImage  bool
	instanceID     string
	imageID        string
	snapshotIDs    []string
	createImageErr error
	securityGroups []ec2types.SecurityGroup
	sourcePlatform ec2types.PlatformValues
	sourceArch     ec2types.ArchitectureValues
}

type fakeSSMRemoteBuildAPI struct {
	online             bool
	sendCommandInput   *ssm.SendCommandInput
	getInvocationInput *ssm.GetCommandInvocationInput
	commandID          string
	status             ssmtypes.CommandInvocationStatus
}

func (f *fakeEC2RemoteBuildAPI) RunInstances(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.runInput = params
	instanceID := firstNonEmpty(f.instanceID, "i-0123456789abcdef0")
	return &ec2.RunInstancesOutput{
		Instances: []ec2types.Instance{{InstanceId: awssdk.String(instanceID)}},
	}, nil
}

func (f *fakeEC2RemoteBuildAPI) DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	state := f.instanceState
	if state == "" {
		state = ec2types.InstanceStateNameRunning
	}
	instanceID := firstNonEmpty(f.instanceID, "i-0123456789abcdef0")
	return &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{
			{
				Instances: []ec2types.Instance{
					{
						InstanceId: awssdk.String(instanceID),
						State:      &ec2types.InstanceState{Name: state},
					},
				},
			},
		},
	}, nil
}

func (f *fakeEC2RemoteBuildAPI) StopInstances(ctx context.Context, params *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	f.stopInput = params
	return &ec2.StopInstancesOutput{}, nil
}

func (f *fakeEC2RemoteBuildAPI) TerminateInstances(ctx context.Context, params *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.terminateInput = params
	return &ec2.TerminateInstancesOutput{}, nil
}

func (f *fakeEC2RemoteBuildAPI) CreateImage(ctx context.Context, params *ec2.CreateImageInput, optFns ...func(*ec2.Options)) (*ec2.CreateImageOutput, error) {
	f.createImageInput = params
	if f.createImageErr != nil {
		return nil, f.createImageErr
	}
	imageID := firstNonEmpty(f.imageID, "ami-0123456789abcdef0")
	return &ec2.CreateImageOutput{ImageId: awssdk.String(imageID)}, nil
}

func (f *fakeEC2RemoteBuildAPI) DescribeImages(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	f.describeImagesInput = params
	if len(params.ImageIds) == 1 && params.ImageIds[0] == "ami-source" {
		arch := f.sourceArch
		if arch == "" {
			arch = ec2types.ArchitectureValuesX8664
		}
		return &ec2.DescribeImagesOutput{
			Images: []ec2types.Image{{
				ImageId:      awssdk.String("ami-source"),
				State:        ec2types.ImageStateAvailable,
				Platform:     f.sourcePlatform,
				Architecture: arch,
			}},
		}, nil
	}
	if len(params.ImageIds) == 0 && len(params.Filters) > 0 && len(params.Owners) == 1 && params.Owners[0] == "099720109477" {
		f.marketplaceImagesInput = params
		return &ec2.DescribeImagesOutput{
			Images: []ec2types.Image{
				{
					ImageId:      awssdk.String("ami-old"),
					State:        ec2types.ImageStateAvailable,
					Architecture: ec2types.ArchitectureValuesX8664,
					CreationDate: awssdk.String("2026-01-01T00:00:00.000Z"),
				},
				{
					ImageId:      awssdk.String("ami-marketplace"),
					State:        ec2types.ImageStateAvailable,
					Architecture: ec2types.ArchitectureValuesX8664,
					CreationDate: awssdk.String("2026-02-01T00:00:00.000Z"),
				},
			},
		}, nil
	}
	if len(params.ImageIds) == 0 && !f.existingImage {
		return &ec2.DescribeImagesOutput{}, nil
	}
	state := f.imageState
	if state == "" {
		state = ec2types.ImageStateAvailable
	}
	imageID := firstNonEmpty(f.imageID, "ami-0123456789abcdef0")
	mappings := make([]ec2types.BlockDeviceMapping, 0, len(f.snapshotIDs))
	for _, snapshotID := range f.snapshotIDs {
		mappings = append(mappings, ec2types.BlockDeviceMapping{
			Ebs: &ec2types.EbsBlockDevice{SnapshotId: awssdk.String(snapshotID)},
		})
	}
	return &ec2.DescribeImagesOutput{
		Images: []ec2types.Image{{ImageId: awssdk.String(imageID), State: state, BlockDeviceMappings: mappings}},
	}, nil
}

func (f *fakeEC2RemoteBuildAPI) DescribeSecurityGroups(ctx context.Context, params *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	f.describeSGInput = params
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: f.securityGroups}, nil
}

func (f *fakeEC2RemoteBuildAPI) DeregisterImage(ctx context.Context, params *ec2.DeregisterImageInput, optFns ...func(*ec2.Options)) (*ec2.DeregisterImageOutput, error) {
	f.deregisterInput = params
	return &ec2.DeregisterImageOutput{}, nil
}

func (f *fakeEC2RemoteBuildAPI) DeleteSnapshot(ctx context.Context, params *ec2.DeleteSnapshotInput, optFns ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error) {
	f.deleteSnapshotInputs = append(f.deleteSnapshotInputs, params)
	return &ec2.DeleteSnapshotOutput{}, nil
}

func (f *fakeSSMRemoteBuildAPI) DescribeInstanceInformation(ctx context.Context, params *ssm.DescribeInstanceInformationInput, optFns ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error) {
	if !f.online {
		return &ssm.DescribeInstanceInformationOutput{}, nil
	}
	return &ssm.DescribeInstanceInformationOutput{
		InstanceInformationList: []ssmtypes.InstanceInformation{
			{InstanceId: awssdk.String("i-build"), PingStatus: ssmtypes.PingStatusOnline},
		},
	}, nil
}

func (f *fakeSSMRemoteBuildAPI) SendCommand(ctx context.Context, params *ssm.SendCommandInput, optFns ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	f.sendCommandInput = params
	commandID := firstNonEmpty(f.commandID, "cmd-1")
	return &ssm.SendCommandOutput{
		Command: &ssmtypes.Command{CommandId: awssdk.String(commandID)},
	}, nil
}

func (f *fakeSSMRemoteBuildAPI) GetCommandInvocation(ctx context.Context, params *ssm.GetCommandInvocationInput, optFns ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	f.getInvocationInput = params
	status := f.status
	if status == "" {
		status = ssmtypes.CommandInvocationStatusSuccess
	}
	return &ssm.GetCommandInvocationOutput{Status: status}, nil
}

func TestAWSRemoteBuildClient_StartsInstance(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{instanceID: "i-build"}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.Phase != platform.RemoteBuildPhaseBooting {
		t.Errorf("Phase = %q", state.Phase)
	}
	if state.OperationRef != "aws://remote-build/build-123?instanceId=i-build" {
		t.Errorf("OperationRef = %q", state.OperationRef)
	}
	if ec2api.runInput == nil {
		t.Fatal("RunInstances was not called")
	}
	if *ec2api.runInput.ImageId != "ami-source" {
		t.Errorf("ImageId = %q", *ec2api.runInput.ImageId)
	}
	if ec2api.runInput.ClientToken == nil || *ec2api.runInput.ClientToken == "" {
		t.Fatal("RunInstances should set a deterministic ClientToken")
	}
	if ec2api.runInput.MetadataOptions == nil || ec2api.runInput.MetadataOptions.HttpTokens != ec2types.HttpTokensStateRequired {
		t.Fatal("RunInstances should enforce IMDSv2")
	}
	if ec2api.runInput.SubnetId == nil || *ec2api.runInput.SubnetId != "subnet-123" {
		t.Errorf("SubnetId = %#v", ec2api.runInput.SubnetId)
	}
	if len(ec2api.runInput.SecurityGroupIds) != 2 {
		t.Errorf("SecurityGroupIds len = %d", len(ec2api.runInput.SecurityGroupIds))
	}
	if ec2api.describeSGInput == nil {
		t.Fatal("DescribeSecurityGroups was not called")
	}
}

func TestAWSRemoteBuildClient_ResolvesMarketplaceRef(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{instanceID: "i-build"}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceType = "marketplace"
	req.SourceProviderRef = ""
	req.SourceMarketplace = &v1alpha1.MarketplaceRef{
		Publisher: "Canonical",
		Offer:     "ubuntu-24_04-lts",
		SKU:       "server",
		Version:   "latest",
	}

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.Phase != platform.RemoteBuildPhaseBooting {
		t.Errorf("Phase = %q", state.Phase)
	}
	if ec2api.runInput == nil || awssdk.ToString(ec2api.runInput.ImageId) != "ami-marketplace" {
		t.Fatalf("RunInstances input = %#v, want ami-marketplace", ec2api.runInput)
	}
	if ec2api.marketplaceImagesInput == nil || len(ec2api.marketplaceImagesInput.Owners) != 1 || ec2api.marketplaceImagesInput.Owners[0] != "099720109477" {
		t.Fatalf("Marketplace DescribeImages input = %#v, want Canonical owner", ec2api.marketplaceImagesInput)
	}
}

func TestAWSRemoteBuildClient_RunningInstanceIsStopped(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{
		instanceID:    "i-build",
		instanceState: ec2types.InstanceStateNameRunning,
	}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OperationRef = "aws://remote-build/build-123?instanceId=i-build"

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.Phase != platform.RemoteBuildPhaseSanitizing {
		t.Errorf("Phase = %q", state.Phase)
	}
	if ec2api.stopInput == nil || ec2api.stopInput.InstanceIds[0] != "i-build" {
		t.Fatalf("StopInstances input = %#v", ec2api.stopInput)
	}
}

func TestAWSRemoteBuildClient_StoppedInstanceCreatesImage(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{
		instanceID:    "i-build",
		imageID:       "ami-new",
		instanceState: ec2types.InstanceStateNameStopped,
	}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OperationRef = "aws://remote-build/build-123?instanceId=i-build"

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.Phase != platform.RemoteBuildPhaseRegistering {
		t.Errorf("Phase = %q", state.Phase)
	}
	if state.OperationRef != "aws://remote-build/build-123?imageId=ami-new&instanceId=i-build" {
		t.Errorf("OperationRef = %q", state.OperationRef)
	}
	if ec2api.createImageInput == nil || *ec2api.createImageInput.InstanceId != "i-build" {
		t.Fatalf("CreateImage input = %#v", ec2api.createImageInput)
	}
}

func TestAWSRemoteBuildClient_StoppedInstanceReusesExistingImage(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{
		instanceID:    "i-build",
		imageID:       "ami-existing",
		existingImage: true,
		instanceState: ec2types.InstanceStateNameStopped,
	}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OperationRef = "aws://remote-build/build-123?instanceId=i-build"

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.OperationRef != "aws://remote-build/build-123?imageId=ami-existing&instanceId=i-build" {
		t.Errorf("OperationRef = %q", state.OperationRef)
	}
	if ec2api.createImageInput != nil {
		t.Fatal("CreateImage should not be called when an existing build AMI is found")
	}
}

func TestAWSRemoteBuildClient_AvailableImageCompletes(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{
		imageID:    "ami-new",
		imageState: ec2types.ImageStateAvailable,
	}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OperationRef = "aws://remote-build/build-123?imageId=ami-new&instanceId=i-build"

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if !state.Done {
		t.Fatal("Done should be true")
	}
	if state.AMIID != "ami-new" {
		t.Errorf("AMIID = %q", state.AMIID)
	}
	if state.Phase != platform.RemoteBuildPhaseReady {
		t.Errorf("Phase = %q", state.Phase)
	}
	if ec2api.describeImagesInput == nil || ec2api.describeImagesInput.ImageIds[0] != "ami-new" {
		t.Fatalf("DescribeImages input = %#v", ec2api.describeImagesInput)
	}
	if ec2api.terminateInput == nil || ec2api.terminateInput.InstanceIds[0] != "i-build" {
		t.Fatalf("TerminateInstances input = %#v", ec2api.terminateInput)
	}
	if state.Hygiene == nil || state.Hygiene.Status != "passed" {
		t.Fatalf("Hygiene = %#v, want passed", state.Hygiene)
	}
	wantChecks := map[string]bool{}
	for _, check := range state.Hygiene.Checks {
		wantChecks[check] = true
	}
	for _, check := range []string{"aws-ami-available", "aws-imdsv2-required", "aws-kms-key-configured", "aws-no-ssh-key"} {
		if !wantChecks[check] {
			t.Fatalf("Hygiene checks = %#v, missing %q", state.Hygiene.Checks, check)
		}
	}
}

func TestAWSRemoteBuildClient_HygieneFailsWhenSSHKeyAllowed(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{
		imageID:    "ami-new",
		imageState: ec2types.ImageStateAvailable,
	}
	client := testAWSRemoteBuildClient(ec2api)
	client.settings.AllowSSHKey = true
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OperationRef = "aws://remote-build/build-123?imageId=ami-new&instanceId=i-build"

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.Hygiene == nil || state.Hygiene.Status != "failed" {
		t.Fatalf("Hygiene = %#v, want failed", state.Hygiene)
	}
}

func TestAWSRemoteBuildClient_RunningInstanceWaitsForSSMReadiness(t *testing.T) {
	client := testAWSRemoteBuildClientWithSSM(
		&fakeEC2RemoteBuildAPI{instanceID: "i-build", instanceState: ec2types.InstanceStateNameRunning},
		&fakeSSMRemoteBuildAPI{online: false},
	)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.Provisioners = append(req.Provisioners, v1alpha1.ProvisionerSpec{Type: "shell", Inline: "echo ok"})
	req.OperationRef = "aws://remote-build/build-123?instanceId=i-build"

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.Phase != platform.RemoteBuildPhaseReadiness {
		t.Errorf("Phase = %q", state.Phase)
	}
}

func TestAWSRemoteBuildClient_StartsShellProvisionerViaSSM(t *testing.T) {
	ssmapi := &fakeSSMRemoteBuildAPI{online: true, commandID: "cmd-shell"}
	client := testAWSRemoteBuildClientWithSSM(
		&fakeEC2RemoteBuildAPI{instanceID: "i-build", instanceState: ec2types.InstanceStateNameRunning},
		ssmapi,
	)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.Provisioners = append(req.Provisioners, v1alpha1.ProvisionerSpec{Type: "shell", Inline: "echo ok"})
	req.OperationRef = "aws://remote-build/build-123?instanceId=i-build"

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.OperationRef != "aws://remote-build/build-123?commandId=cmd-shell&instanceId=i-build" {
		t.Errorf("OperationRef = %q", state.OperationRef)
	}
	if ssmapi.sendCommandInput == nil {
		t.Fatal("SendCommand was not called")
	}
	if *ssmapi.sendCommandInput.DocumentName != "AWS-RunShellScript" {
		t.Errorf("DocumentName = %q", *ssmapi.sendCommandInput.DocumentName)
	}
	if got := ssmapi.sendCommandInput.Parameters["commands"][0]; got != "echo ok" {
		t.Errorf("command = %q", got)
	}
}

func TestAWSRemoteBuildClient_CommandSuccessStopsAfterLastProvisioner(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{instanceID: "i-build", instanceState: ec2types.InstanceStateNameRunning}
	client := testAWSRemoteBuildClientWithSSM(ec2api, &fakeSSMRemoteBuildAPI{
		online: true,
		status: ssmtypes.CommandInvocationStatusSuccess,
	})
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.Provisioners = append(req.Provisioners, v1alpha1.ProvisionerSpec{Type: "shell", Inline: "echo ok"})
	req.OperationRef = "aws://remote-build/build-123?commandId=cmd-shell&instanceId=i-build"

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.Phase != platform.RemoteBuildPhaseSanitizing {
		t.Errorf("Phase = %q", state.Phase)
	}
	if state.OperationRef != "aws://remote-build/build-123?instanceId=i-build&provisionerIndex=1" {
		t.Errorf("OperationRef = %q", state.OperationRef)
	}
	if ec2api.stopInput == nil {
		t.Fatal("StopInstances was not called")
	}
}

func TestAWSRemoteBuildClient_CommandFailureReturnsError(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{instanceID: "i-build", instanceState: ec2types.InstanceStateNameRunning}
	client := testAWSRemoteBuildClientWithSSM(
		ec2api,
		&fakeSSMRemoteBuildAPI{online: true, status: ssmtypes.CommandInvocationStatusFailed},
	)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.Provisioners = append(req.Provisioners, v1alpha1.ProvisionerSpec{Type: "shell", Inline: "echo ok"})
	req.OperationRef = "aws://remote-build/build-123?commandId=cmd-shell&instanceId=i-build"

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should return an error for failed SSM command")
	}
	if ec2api.terminateInput == nil || ec2api.terminateInput.InstanceIds[0] != "i-build" {
		t.Fatalf("TerminateInstances input = %#v", ec2api.terminateInput)
	}
}

func TestAWSRemoteBuildClient_CreateImageFailureTerminatesInstance(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{
		instanceID:     "i-build",
		instanceState:  ec2types.InstanceStateNameStopped,
		createImageErr: errors.New("create failed"),
	}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OperationRef = "aws://remote-build/build-123?instanceId=i-build"

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should return an error for failed CreateImage")
	}
	if ec2api.terminateInput == nil || ec2api.terminateInput.InstanceIds[0] != "i-build" {
		t.Fatalf("TerminateInstances input = %#v", ec2api.terminateInput)
	}
}

func TestAWSRemoteBuildClient_FailedImageCleansAMIAndSnapshots(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{
		imageID:     "ami-new",
		imageState:  ec2types.ImageStateFailed,
		snapshotIDs: []string{"snap-1", "snap-2"},
	}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OperationRef = "aws://remote-build/build-123?imageId=ami-new&instanceId=i-build"

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should return an error for failed AMI")
	}
	if ec2api.deregisterInput == nil || *ec2api.deregisterInput.ImageId != "ami-new" {
		t.Fatalf("DeregisterImage input = %#v", ec2api.deregisterInput)
	}
	if len(ec2api.deleteSnapshotInputs) != 2 {
		t.Fatalf("DeleteSnapshot calls = %d, want 2", len(ec2api.deleteSnapshotInputs))
	}
	if ec2api.terminateInput == nil || ec2api.terminateInput.InstanceIds[0] != "i-build" {
		t.Fatalf("TerminateInstances input = %#v", ec2api.terminateInput)
	}
}

func TestSSMProvisionerCommand_MapsPowerShell(t *testing.T) {
	req := awsRemoteRequestFromPlatform(remoteBuildRequest())
	req.OSFamily = platform.OSFamilyWindows

	document, commands, err := ssmProvisionerCommand(req, v1alpha1.ProvisionerSpec{Type: "powershell", Inline: "Write-Host ok"})
	if err != nil {
		t.Fatalf("ssmProvisionerCommand returned error: %v", err)
	}
	if document != "AWS-RunPowerShellScript" {
		t.Errorf("document = %q", document)
	}
	if len(commands) != 1 || commands[0] != "Write-Host ok" {
		t.Errorf("commands = %#v", commands)
	}
}

func TestSSMProvisionerCommand_MapsLinuxFile(t *testing.T) {
	req := awsRemoteRequestFromPlatform(remoteBuildRequest())

	document, commands, err := ssmProvisionerCommand(req, v1alpha1.ProvisionerSpec{
		Type:   "file",
		Inline: "hello",
		Args:   []string{"/etc/imagebuilder/test.txt"},
	})
	if err != nil {
		t.Fatalf("ssmProvisionerCommand returned error: %v", err)
	}
	if document != "AWS-RunShellScript" {
		t.Errorf("document = %q", document)
	}
	if len(commands) != 1 || !strings.Contains(commands[0], "aGVsbG8=") {
		t.Errorf("commands = %#v", commands)
	}
}

func TestSSMProvisionerCommand_RejectsUnsupportedType(t *testing.T) {
	req := awsRemoteRequestFromPlatform(remoteBuildRequest())

	_, _, err := ssmProvisionerCommand(req, v1alpha1.ProvisionerSpec{Type: "ansible"})
	if err == nil {
		t.Fatal("ssmProvisionerCommand should reject unsupported provisioner type")
	}
}

func TestAWSRemoteBuildClient_RequiresKMSKey(t *testing.T) {
	client := testAWSRemoteBuildClient(&fakeEC2RemoteBuildAPI{})
	client.settings.KMSKeyID = ""
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should require remote.kmsKeyId")
	}
}

func TestAWSRemoteBuildClient_ForbidsSSHKeyByDefault(t *testing.T) {
	client := testAWSRemoteBuildClient(&fakeEC2RemoteBuildAPI{})
	client.settings.KeyName = "debug-key"
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should reject remote.keyName by default")
	}
}

func TestAWSRemoteBuildClient_RequiresExplicitKeyForSSHGuestAccess(t *testing.T) {
	client := testAWSRemoteBuildClient(&fakeEC2RemoteBuildAPI{})
	client.settings.AllowSSHKey = true
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.GuestAccess = &v1alpha1.GuestAccessSpec{Protocol: "ssh"}

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should require remote.keyName for SSH guest access")
	}
	if !strings.Contains(err.Error(), "remote.keyName") {
		t.Fatalf("error = %v", err)
	}
}

func TestAWSRemoteBuildClient_RejectsPublicRemoteAdminSecurityGroup(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{
		securityGroups: []ec2types.SecurityGroup{
			{
				GroupId: awssdk.String("sg-public"),
				IpPermissions: []ec2types.IpPermission{
					{
						IpProtocol: awssdk.String("tcp"),
						FromPort:   awssdk.Int32(22),
						ToPort:     awssdk.Int32(22),
						IpRanges:   []ec2types.IpRange{{CidrIp: awssdk.String("0.0.0.0/0")}},
					},
				},
			},
		},
	}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should reject public SSH ingress")
	}
}

func TestAWSRemoteBuildClient_RejectsWindowsSpecWithLinuxSourceAMI(t *testing.T) {
	client := testAWSRemoteBuildClient(&fakeEC2RemoteBuildAPI{})
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OSFamily = platform.OSFamilyWindows
	req.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "powershell", Inline: "Write-Host ok"}}

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should reject Windows builds from non-Windows AMIs")
	}
	if !strings.Contains(err.Error(), "not a Windows AMI") {
		t.Fatalf("error = %v", err)
	}
}

func TestAWSRemoteBuildClient_RejectsLinuxSpecWithWindowsSourceAMI(t *testing.T) {
	client := testAWSRemoteBuildClient(&fakeEC2RemoteBuildAPI{sourcePlatform: ec2types.PlatformValuesWindows})
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should reject Linux builds from Windows AMIs")
	}
	if !strings.Contains(err.Error(), "spec.os.family is linux") {
		t.Fatalf("error = %v", err)
	}
}

func TestAWSRemoteBuildClient_RejectsArchitectureMismatch(t *testing.T) {
	client := testAWSRemoteBuildClient(&fakeEC2RemoteBuildAPI{sourceArch: ec2types.ArchitectureValuesArm64})
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OSArch = "amd64"

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should reject source AMI architecture mismatches")
	}
	if !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("error = %v", err)
	}
}

func TestAWSRemoteBuildClient_RejectsShellProvisionerForWindowsBeforeStart(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{sourcePlatform: ec2types.PlatformValuesWindows}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OSFamily = platform.OSFamilyWindows
	req.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo wrong"}}

	_, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err == nil {
		t.Fatal("ReconcileRemoteBuild should reject shell provisioners for Windows")
	}
	if ec2api.runInput != nil {
		t.Fatal("RunInstances should not be called when provisioners are invalid")
	}
}

func TestAWSRemoteBuildClient_StartsWindowsPowerShellBuild(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{instanceID: "i-win", sourcePlatform: ec2types.PlatformValuesWindows}
	client := testAWSRemoteBuildClient(ec2api)
	req := remoteBuildRequest()
	req.SourceProviderRef = "ami-source"
	req.OSFamily = platform.OSFamilyWindows
	req.OSDistribution = "windows-server"
	req.OSVersion = "2022"
	req.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "powershell", Inline: "Write-Host ok"}}

	state, err := client.ReconcileRemoteBuild(context.Background(), awsRemoteRequestFromPlatform(req))
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if state.OperationRef != "aws://remote-build/build-123?instanceId=i-win" {
		t.Errorf("OperationRef = %q", state.OperationRef)
	}
	if ec2api.runInput == nil {
		t.Fatal("RunInstances was not called")
	}
}

func TestAWSRemoteBuildClient_CleanupRemoteBuild_RemovesKnownAndDiscoveredResources(t *testing.T) {
	ec2api := &fakeEC2RemoteBuildAPI{
		instanceID:    "i-build",
		imageID:       "ami-build",
		existingImage: true,
		snapshotIDs:   []string{"snap-1"},
	}
	client := testAWSRemoteBuildClient(ec2api)
	err := client.CleanupRemoteBuild(context.Background(), awsRemoteBuildRequest{
		BuildID:      "build-123",
		OperationRef: "aws://remote-build/build-123?instanceId=i-build&imageId=ami-build",
		ImageName:    "ubuntu-remote",
		Target:       v1alpha1.TargetSpec{Tags: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("CleanupRemoteBuild returned error: %v", err)
	}
	if ec2api.terminateInput == nil || len(ec2api.terminateInput.InstanceIds) == 0 || ec2api.terminateInput.InstanceIds[0] != "i-build" {
		t.Fatalf("TerminateInstances input = %#v", ec2api.terminateInput)
	}
	if ec2api.deregisterInput == nil || ec2api.deregisterInput.ImageId == nil || *ec2api.deregisterInput.ImageId != "ami-build" {
		t.Fatalf("DeregisterImage input = %#v", ec2api.deregisterInput)
	}
	if len(ec2api.deleteSnapshotInputs) == 0 || ec2api.deleteSnapshotInputs[0].SnapshotId == nil || *ec2api.deleteSnapshotInputs[0].SnapshotId != "snap-1" {
		t.Fatalf("DeleteSnapshot inputs = %#v", ec2api.deleteSnapshotInputs)
	}
}

func testAWSRemoteBuildClient(ec2api ec2RemoteBuildAPI) *awsSDKRemoteBuildClient {
	return testAWSRemoteBuildClientWithSSM(ec2api, &fakeSSMRemoteBuildAPI{online: true})
}

func testAWSRemoteBuildClientWithSSM(ec2api ec2RemoteBuildAPI, ssmapi ssmRemoteBuildAPI) *awsSDKRemoteBuildClient {
	return &awsSDKRemoteBuildClient{
		region: "eu-central-1",
		ec2:    ec2api,
		ssm:    ssmapi,
		settings: awsRemoteBuildSettings{
			InstanceType:      "t3.micro",
			SubnetID:          "subnet-123",
			SecurityGroupIDs:  []string{"sg-1", "sg-2"},
			IAMProfileName:    "imagebuilder-remote",
			RootVolumeSizeGiB: 16,
			KMSKeyID:          "alias/imagebuilder",
		},
	}
}

func awsRemoteRequestFromPlatform(req *platform.RemoteBuildRequest) awsRemoteBuildRequest {
	return awsRemoteBuildRequest{
		BuildID:           req.BuildID,
		OperationRef:      req.OperationRef,
		ImageName:         req.ImageName,
		Namespace:         req.Namespace,
		Region:            "eu-central-1",
		SourceType:        req.SourceType,
		SourceURL:         req.SourceURL,
		SourceProviderRef: req.SourceProviderRef,
		SourceMarketplace: req.SourceMarketplace,
		SourceChecksum:    req.SourceChecksum,
		OSFamily:          req.OSFamily,
		OSDistribution:    req.OSDistribution,
		OSVersion:         req.OSVersion,
		OSArch:            req.OSArch,
		Target:            req.Target,
		Provisioners:      req.Provisioners,
		GuestAccess:       req.GuestAccess,
		Timeout:           req.Timeout,
	}
}
