package azure

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	provisionersource "github.com/anwendt/imagebuilder/pkg/provisioner/source"
)

type azureRemoteBuildInput struct {
	BuildID            string
	OperationRef       string
	ImageName          string
	SourceType         string
	SourceRef          string
	SourceMarketplace  *v1alpha1.MarketplaceRef
	SourceChecksum     string
	OSFamily           platform.OSFamily
	Format             platform.ImageFormat
	Tags               map[string]string
	ProviderConfigName string
	Provisioners       []v1alpha1.ProvisionerSpec
	GuestAccess        *v1alpha1.GuestAccessSpec
	Evidence           *v1alpha1.EvidenceSpec
	EvidenceCosignKey  string
}

type azureRemoteBuildState struct {
	OperationRef string
	Phase        platform.RemoteBuildPhase
	Message      string
	Done         bool
	Image        *platform.ImageRef
	Hygiene      *platform.RemoteHygieneResult
	Evidence     *platform.RemoteEvidenceResult
}

const azureRemoteBuildPublicKey = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDDzch/BPwsvvCVPklQJaRRO7gsqw4mLjnJiLHTQ5o0mBi7BLInDe12I5C9Qw2QU+mh46eaDzqa9vggfoZxWCENfhJ7zsdYReh27XzmpFD36THxci5awdPVmiF+kQ0LlxGZgtkf11lLt9hpSSqeXcIsnQRO9LAEXqBtMAR50zSBgeHcyXRNxeiR4D1c/FtsGcm+6GJu4eL+T1GPvNTr77dhVaOAYfba0QUTfnDOW3j0A8YqtH+0Aagzh6w2yAxP//NV1TtL0g1l0PTa8jTqjvBi13PI6RgpHP5HDsxyQPj7UoNiHLP2no/X3MguOYoqPZWDRdOzsAbL4+ufCOJu41bN imagebuilder-azure-remote-build"

func (c *sdkClient) ReconcileRemoteBuild(ctx context.Context, input azureRemoteBuildInput) (*azureRemoteBuildState, error) {
	expandedInput, cleanup, err := expandAzureRemoteProvisioners(ctx, input)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	input = expandedInput
	sourceType := strings.ToLower(strings.TrimSpace(input.SourceType))
	input.SourceType = sourceType
	if sourceType != "snapshot" && sourceType != "managed-disk" && sourceType != "manageddisk" && sourceType != "disk" && sourceType != "marketplace" {
		return nil, fmt.Errorf("azure remote build supports source type snapshot, managed-disk, or marketplace, got %q", input.SourceType)
	}
	if sourceType == "marketplace" {
		if err := validateMarketplaceRef(input.SourceMarketplace); err != nil {
			return nil, err
		}
	} else if input.SourceRef == "" {
		return nil, fmt.Errorf("azure remote build source providerRef is required")
	}
	if input.Format != platform.FormatVHD {
		return nil, fmt.Errorf("azure remote build requires target format %q, got %q", platform.FormatVHD, input.Format)
	}
	if err := validateAzureRemoteProvisioners(input); err != nil {
		return nil, err
	}
	settings := azureRemoteSettingsFromExtra(c.cfg.extraConfig)
	if err := settings.validate(input); err != nil {
		return nil, err
	}
	input.EvidenceCosignKey = settings.EvidenceCosignKey
	ref, err := parseAzureRemoteOperationRef(input.OperationRef)
	if err != nil {
		return nil, err
	}
	if ref.ImageID != "" {
		return &azureRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseReady,
			Message:      "Azure remote image is ready",
			Done:         true,
			Image: &platform.ImageRef{
				ID:       ref.ImageID,
				Name:     input.ImageName,
				Location: c.cfg.location,
				Tags:     input.Tags,
			},
			Hygiene:  azureRemoteHygiene(input, ref.ImageID),
			Evidence: ref.evidence(),
		}, nil
	}
	if ref.VMName == "" {
		return c.startRemoteBuildVM(ctx, input, settings)
	}
	if ref.ProvisionerIndex < len(input.Provisioners) {
		evidence, err := c.runAzureProvisioner(ctx, input, ref, input.Provisioners[ref.ProvisionerIndex])
		if err != nil {
			_ = c.cleanupAzureRemoteResources(ctx, ref)
			return nil, err
		}
		if evidence != nil {
			ref.setEvidence(evidence)
		}
		ref.ProvisionerIndex++
		return &azureRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseProvisioning,
			Message:      fmt.Sprintf("Azure Run Command provisioner %d completed", ref.ProvisionerIndex-1),
			Evidence:     ref.evidence(),
		}, nil
	}
	return c.finishRemoteBuildVM(ctx, input, ref)
}

func expandAzureRemoteProvisioners(ctx context.Context, input azureRemoteBuildInput) (azureRemoteBuildInput, func(), error) {
	if !provisionersource.HasSources(input.Provisioners) {
		return input, func() {}, nil
	}
	workspace, err := os.MkdirTemp("", "imagebuilder-azure-provisioners-*")
	if err != nil {
		return input, func() {}, fmt.Errorf("create Azure remote provisioner source workspace: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	provisioners, err := provisionersource.ExpandProvisioners(ctx, workspace, input.Provisioners)
	if err != nil {
		cleanup()
		return input, func() {}, err
	}
	input.Provisioners = provisioners
	return input, cleanup, nil
}

func (c *sdkClient) CleanupRemoteBuild(ctx context.Context, input azureRemoteBuildInput) error {
	ref, err := parseAzureRemoteOperationRef(input.OperationRef)
	if err != nil {
		return err
	}
	if ref.BuildID == "" {
		ref.BuildID = input.BuildID
	}
	if ref.VMName == "" && input.BuildID != "" {
		ref.VMName = azureRemoteVMName(input.BuildID)
		ref.DiskName = azureRemoteDiskName(input.BuildID)
	}
	return c.cleanupAzureRemoteResources(ctx, ref)
}

func (c *sdkClient) startRemoteBuildVM(ctx context.Context, input azureRemoteBuildInput, settings azureRemoteSettings) (*azureRemoteBuildState, error) {
	ref := azureRemoteOperationRef{
		BuildID: input.BuildID,
		VMName:  azureRemoteVMName(input.BuildID),
	}
	sourceType := strings.ToLower(strings.TrimSpace(input.SourceType))
	storageProfile := &armcompute.StorageProfile{}
	if sourceType == "marketplace" {
		storageProfile.ImageReference = azureMarketplaceImageReference(input.SourceMarketplace)
		storageProfile.OSDisk = &armcompute.OSDisk{
			Name:         to.Ptr(azureRemoteDiskName(input.BuildID)),
			CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
			OSType:       to.Ptr(azureOSType(input.OSFamily)),
			Caching:      to.Ptr(armcompute.CachingTypesReadWrite),
			DeleteOption: to.Ptr(armcompute.DiskDeleteOptionTypesDelete),
		}
		if c.cfg.storageAccountType != "" {
			storageProfile.OSDisk.ManagedDisk = &armcompute.ManagedDiskParameters{
				StorageAccountType: to.Ptr(c.cfg.storageAccountType),
			}
		}
		if c.cfg.diskSizeGiB > 0 {
			storageProfile.OSDisk.DiskSizeGB = to.Ptr(c.cfg.diskSizeGiB)
		}
	} else {
		ref.DiskName = azureRemoteDiskName(input.BuildID)
		disk, err := c.createRemoteOSDisk(ctx, input, ref.DiskName, sourceType)
		if err != nil {
			return nil, err
		}
		diskID := value(disk.ID)
		if diskID == "" {
			diskID = managedDiskID(c.cfg.subscriptionID, c.cfg.resourceGroup, ref.DiskName)
		}
		storageProfile.OSDisk = &armcompute.OSDisk{
			Name:         to.Ptr(ref.DiskName),
			CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesAttach),
			ManagedDisk:  &armcompute.ManagedDiskParameters{ID: to.Ptr(diskID)},
			OSType:       to.Ptr(azureOSType(input.OSFamily)),
			Caching:      to.Ptr(armcompute.CachingTypesReadWrite),
			DeleteOption: to.Ptr(armcompute.DiskDeleteOptionTypesDetach),
		}
	}
	vm := armcompute.VirtualMachine{
		Location: to.Ptr(c.cfg.location),
		Tags:     azureTags(input.Tags),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{
				VMSize: to.Ptr(armcompute.VirtualMachineSizeTypes(settings.VMSize)),
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkInterfaces: []*armcompute.NetworkInterfaceReference{{
					ID: to.Ptr(settings.NetworkInterfaceID),
					Properties: &armcompute.NetworkInterfaceReferenceProperties{
						Primary:      to.Ptr(true),
						DeleteOption: to.Ptr(armcompute.DeleteOptionsDetach),
					},
				}},
			},
			StorageProfile: storageProfile,
		},
	}
	if settings.ManagedIdentityID != "" {
		vm.Identity = &armcompute.VirtualMachineIdentity{
			Type: to.Ptr(armcompute.ResourceIdentityTypeUserAssigned),
			UserAssignedIdentities: map[string]*armcompute.UserAssignedIdentitiesValue{
				settings.ManagedIdentityID: {},
			},
		}
	}
	if sourceType == "marketplace" {
		vm.Properties.OSProfile = azureRemoteOSProfile(input)
	}
	poller, err := c.vms.BeginCreateOrUpdate(ctx, c.cfg.resourceGroup, ref.VMName, vm, nil)
	if err != nil {
		_ = c.cleanupAzureRemoteResources(ctx, ref)
		return nil, fmt.Errorf("create Azure remote build VM %q: %w", ref.VMName, err)
	}
	created, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		_ = c.cleanupAzureRemoteResources(ctx, ref)
		return nil, fmt.Errorf("wait for Azure remote build VM %q: %w", ref.VMName, err)
	}
	ref.VMID = firstNonEmpty(value(created.ID), virtualMachineID(c.cfg.subscriptionID, c.cfg.resourceGroup, ref.VMName))
	return &azureRemoteBuildState{
		OperationRef: ref.String(),
		Phase:        platform.RemoteBuildPhaseBooting,
		Message:      "Azure remote build VM started",
	}, nil
}

func (c *sdkClient) createRemoteOSDisk(ctx context.Context, input azureRemoteBuildInput, diskName, sourceType string) (armcompute.Disk, error) {
	disk := armcompute.Disk{
		Location: to.Ptr(c.cfg.location),
		Tags:     azureTags(input.Tags),
		SKU:      &armcompute.DiskSKU{Name: to.Ptr(armcompute.DiskStorageAccountTypes(c.cfg.storageAccountType))},
		Properties: &armcompute.DiskProperties{
			CreationData: &armcompute.CreationData{
				CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceResourceID: to.Ptr(input.SourceRef),
			},
			OSType: to.Ptr(azureOSType(input.OSFamily)),
		},
	}
	if c.cfg.diskSizeGiB > 0 {
		disk.Properties.DiskSizeGB = to.Ptr(c.cfg.diskSizeGiB)
	}
	poller, err := c.disks.BeginCreateOrUpdate(ctx, c.cfg.resourceGroup, diskName, disk, nil)
	if err != nil {
		return armcompute.Disk{}, fmt.Errorf("create Azure remote OS disk from %s: %w", sourceType, err)
	}
	created, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return armcompute.Disk{}, fmt.Errorf("wait for Azure remote OS disk %q: %w", diskName, err)
	}
	return created.Disk, nil
}

func (c *sdkClient) runAzureProvisioner(ctx context.Context, input azureRemoteBuildInput, ref azureRemoteOperationRef, spec v1alpha1.ProvisionerSpec) (*platform.RemoteEvidenceResult, error) {
	commandID, script, err := azureRunCommand(input, spec)
	if err != nil {
		return nil, err
	}
	poller, err := c.vms.BeginRunCommand(ctx, c.cfg.resourceGroup, ref.VMName, armcompute.RunCommandInput{
		CommandID: to.Ptr(commandID),
		Script:    stringPtrs(script),
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("start Azure Run Command provisioner %d: %w", ref.ProvisionerIndex, err)
	}
	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("wait for Azure Run Command provisioner %d: %w", ref.ProvisionerIndex, err)
	}
	if err := azureRunCommandError(result.Value); err != nil {
		return nil, fmt.Errorf("azure Run Command provisioner %d failed: %w", ref.ProvisionerIndex, err)
	}
	if spec.Type != "evidence" {
		return nil, nil
	}
	evidence, err := azureEvidenceFromRunCommand(result.Value)
	if err != nil {
		return nil, fmt.Errorf("Azure evidence provisioner %d: %w", ref.ProvisionerIndex, err)
	}
	return evidence, nil
}

func (c *sdkClient) finishRemoteBuildVM(ctx context.Context, input azureRemoteBuildInput, ref azureRemoteOperationRef) (*azureRemoteBuildState, error) {
	if input.Evidence != nil && input.Evidence.Required {
		if err := validateAzureEvidence(input.Evidence, ref.evidence()); err != nil {
			_ = c.cleanupAzureRemoteResources(ctx, ref)
			return nil, err
		}
	}
	poller, err := c.vms.BeginDeallocate(ctx, c.cfg.resourceGroup, ref.VMName, nil)
	if err != nil {
		_ = c.cleanupAzureRemoteResources(ctx, ref)
		return nil, fmt.Errorf("deallocate Azure remote build VM %q: %w", ref.VMName, err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		_ = c.cleanupAzureRemoteResources(ctx, ref)
		return nil, fmt.Errorf("wait for Azure remote build VM %q deallocation: %w", ref.VMName, err)
	}
	if c.cfg.osState == armcompute.OperatingSystemStateTypesGeneralized {
		if _, err := c.vms.Generalize(ctx, c.cfg.resourceGroup, ref.VMName, nil); err != nil {
			_ = c.cleanupAzureRemoteResources(ctx, ref)
			return nil, fmt.Errorf("generalize Azure remote build VM %q: %w", ref.VMName, err)
		}
	}
	vmID := firstNonEmpty(ref.VMID, virtualMachineID(c.cfg.subscriptionID, c.cfg.resourceGroup, ref.VMName))
	image, err := c.RegisterImage(ctx, registerInput{
		ResourceGroup:          c.cfg.resourceGroup,
		Location:               c.cfg.location,
		ImageName:              input.ImageName,
		Format:                 input.Format,
		OS:                     input.OSFamily,
		Checksum:               input.SourceChecksum,
		Tags:                   input.Tags,
		HyperVGeneration:       c.cfg.hyperVGeneration,
		OSState:                c.cfg.osState,
		DiskSizeGiB:            c.cfg.diskSizeGiB,
		StorageAccountType:     c.cfg.storageAccountType,
		GalleryName:            c.cfg.galleryName,
		GalleryImageName:       c.cfg.galleryImageName,
		GalleryVersion:         c.cfg.galleryVersion,
		ReplicaCount:           c.cfg.replicaCount,
		TargetRegions:          c.cfg.targetRegions,
		SourceVirtualMachineID: vmID,
	})
	if err != nil {
		_ = c.cleanupAzureRemoteResources(ctx, ref)
		return nil, err
	}
	ref.ImageID = image.ID
	if err := c.cleanupAzureRemoteResources(ctx, ref); err != nil {
		return nil, fmt.Errorf("cleanup Azure remote build resources after image registration: %w", err)
	}
	return &azureRemoteBuildState{
		OperationRef: ref.String(),
		Phase:        platform.RemoteBuildPhaseReady,
		Message:      "Azure remote image registered from provisioned VM",
		Done:         true,
		Image:        image,
		Hygiene:      azureRemoteHygiene(input, image.ID),
		Evidence:     ref.evidence(),
	}, nil
}

func (c *sdkClient) cleanupAzureRemoteResources(ctx context.Context, ref azureRemoteOperationRef) error {
	var errs []error
	if ref.VMName != "" {
		poller, err := c.vms.BeginDelete(ctx, c.cfg.resourceGroup, ref.VMName, nil)
		if err != nil {
			if !isHTTPStatus(err, 404) {
				errs = append(errs, fmt.Errorf("delete Azure remote build VM %q: %w", ref.VMName, err))
			}
		} else if _, err := poller.PollUntilDone(ctx, nil); err != nil && !isHTTPStatus(err, 404) {
			errs = append(errs, fmt.Errorf("wait for Azure remote build VM %q delete: %w", ref.VMName, err))
		}
	}
	if ref.DiskName != "" {
		poller, err := c.disks.BeginDelete(ctx, c.cfg.resourceGroup, ref.DiskName, nil)
		if err != nil {
			if !isHTTPStatus(err, 404) {
				errs = append(errs, fmt.Errorf("delete Azure remote build disk %q: %w", ref.DiskName, err))
			}
		} else if _, err := poller.PollUntilDone(ctx, nil); err != nil && !isHTTPStatus(err, 404) {
			errs = append(errs, fmt.Errorf("wait for Azure remote build disk %q delete: %w", ref.DiskName, err))
		}
	}
	return errors.Join(errs...)
}

type azureRemoteSettings struct {
	VMSize             string
	NetworkInterfaceID string
	ManagedIdentityID  string
	EvidenceCosignKey  string
}

func azureRemoteSettingsFromExtra(extra map[string]string) azureRemoteSettings {
	return azureRemoteSettings{
		VMSize:             firstNonEmpty(extra["remote.vmSize"], extra["vmSize"], "Standard_B2s"),
		NetworkInterfaceID: firstNonEmpty(extra["remote.networkInterfaceId"], extra["networkInterfaceId"]),
		ManagedIdentityID:  strings.TrimSpace(extra["remote.managedIdentityId"]),
		EvidenceCosignKey:  strings.TrimSpace(extra["remote.evidence.cosignKeyRef"]),
	}
}

func (s azureRemoteSettings) validate(input azureRemoteBuildInput) error {
	if s.VMSize == "" {
		return fmt.Errorf("azure remote build requires ProviderConfig extra remote.vmSize")
	}
	if azureRemoteRequiresNetwork(input) && s.NetworkInterfaceID == "" {
		return fmt.Errorf("azure remote build with SSH or provisioners requires ProviderConfig extra remote.networkInterfaceId")
	}
	if input.BuildID == "" {
		return fmt.Errorf("azure remote build requires build ID")
	}
	if input.Evidence != nil && input.Evidence.Required {
		if s.ManagedIdentityID == "" {
			return fmt.Errorf("Azure evidence requires ProviderConfig extra remote.managedIdentityId")
		}
		if s.EvidenceCosignKey == "" {
			return fmt.Errorf("Azure evidence requires ProviderConfig extra remote.evidence.cosignKeyRef")
		}
	}
	return nil
}

func azureRemoteRequiresNetwork(input azureRemoteBuildInput) bool {
	if strings.EqualFold(strings.TrimSpace(input.SourceType), "marketplace") {
		return true
	}
	if len(input.Provisioners) > 0 {
		return true
	}
	return input.GuestAccess != nil && strings.EqualFold(strings.TrimSpace(input.GuestAccess.Protocol), "ssh")
}

func validateMarketplaceRef(ref *v1alpha1.MarketplaceRef) error {
	if ref == nil {
		return fmt.Errorf("azure remote marketplace source requires source.marketplaceRef")
	}
	if strings.TrimSpace(ref.Publisher) == "" || strings.TrimSpace(ref.Offer) == "" || strings.TrimSpace(ref.SKU) == "" || strings.TrimSpace(ref.Version) == "" {
		return fmt.Errorf("azure remote marketplace source requires source.marketplaceRef publisher, offer, sku, and version")
	}
	return nil
}

func azureMarketplaceImageReference(ref *v1alpha1.MarketplaceRef) *armcompute.ImageReference {
	if ref == nil {
		return nil
	}
	return &armcompute.ImageReference{
		Publisher: to.Ptr(ref.Publisher),
		Offer:     to.Ptr(ref.Offer),
		SKU:       to.Ptr(ref.SKU),
		Version:   to.Ptr(ref.Version),
	}
}

func azureRemoteOSProfile(input azureRemoteBuildInput) *armcompute.OSProfile {
	const adminUser = "imagebuilder"
	profile := &armcompute.OSProfile{
		AdminUsername: to.Ptr(adminUser),
		ComputerName:  to.Ptr(azureRemoteComputerName(input.BuildID)),
	}
	if input.OSFamily != platform.OSFamilyWindows {
		profile.LinuxConfiguration = &armcompute.LinuxConfiguration{
			DisablePasswordAuthentication: to.Ptr(true),
			ProvisionVMAgent:              to.Ptr(true),
			SSH: &armcompute.SSHConfiguration{
				PublicKeys: []*armcompute.SSHPublicKey{{
					Path:    to.Ptr("/home/" + adminUser + "/.ssh/authorized_keys"),
					KeyData: to.Ptr(azureRemoteBuildPublicKey),
				}},
			},
		}
	}
	return profile
}

func validateAzureRemoteProvisioners(input azureRemoteBuildInput) error {
	for index, provisioner := range input.Provisioners {
		switch provisioner.Type {
		case "shell":
			if input.OSFamily == platform.OSFamilyWindows {
				return fmt.Errorf("shell provisioner is not supported for Azure Windows remote builds; use powershell or file")
			}
		case "powershell":
			if input.OSFamily == platform.OSFamilyLinux {
				return fmt.Errorf("powershell provisioner is not supported for Azure Linux remote builds; use shell or file")
			}
		case "file":
		case "evidence":
			if index != len(input.Provisioners)-1 {
				return fmt.Errorf("evidence provisioner must be the final Azure remote provisioner")
			}
			if input.Evidence == nil {
				return fmt.Errorf("evidence provisioner requires spec.evidence")
			}
		default:
			return fmt.Errorf("provisioner type %q is not supported by Azure Run Command remote build", provisioner.Type)
		}
	}
	return nil
}

func azureRunCommand(input azureRemoteBuildInput, spec v1alpha1.ProvisionerSpec) (string, []string, error) {
	switch spec.Type {
	case "shell":
		if input.OSFamily != platform.OSFamilyLinux {
			return "", nil, fmt.Errorf("shell provisioner requires linux OS family for Azure remote build")
		}
		if strings.TrimSpace(spec.Inline) == "" {
			return "", nil, fmt.Errorf("shell provisioner requires inline content for Azure remote build")
		}
		return "RunShellScript", []string{spec.Inline}, nil
	case "powershell":
		if input.OSFamily != platform.OSFamilyWindows {
			return "", nil, fmt.Errorf("powershell provisioner requires windows OS family for Azure remote build")
		}
		if strings.TrimSpace(spec.Inline) == "" {
			return "", nil, fmt.Errorf("powershell provisioner requires inline content for Azure remote build")
		}
		return "RunPowerShellScript", []string{spec.Inline}, nil
	case "file":
		if strings.TrimSpace(spec.Inline) == "" {
			return "", nil, fmt.Errorf("file provisioner requires inline content for Azure remote build")
		}
		if len(spec.Args) != 1 || strings.TrimSpace(spec.Args[0]) == "" {
			return "", nil, fmt.Errorf("file provisioner requires destination path in args[0] for Azure remote build")
		}
		return azureFileProvisionerCommand(input, spec.Inline, spec.Args[0])
	case "evidence":
		if strings.TrimSpace(spec.Inline) == "" {
			return "", nil, fmt.Errorf("evidence provisioner requires inline content after source expansion")
		}
		if input.Evidence == nil {
			return "", nil, fmt.Errorf("evidence provisioner requires evidence policy")
		}
		if input.OSFamily == platform.OSFamilyLinux {
			return "RunShellScript", []string{azureLinuxEvidenceCommand(input, spec.Inline)}, nil
		}
		if input.OSFamily == platform.OSFamilyWindows {
			return "RunPowerShellScript", []string{azureWindowsEvidenceCommand(input, spec.Inline)}, nil
		}
		return "", nil, fmt.Errorf("evidence provisioner requires linux or windows OS family")
	default:
		return "", nil, fmt.Errorf("provisioner type %q is not supported by Azure remote build", spec.Type)
	}
}

func azureLinuxEvidenceCommand(input azureRemoteBuildInput, script string) string {
	values := map[string]string{
		"IMAGEBUILDER_BUILD_ID":                       input.BuildID,
		"IMAGEBUILDER_IMAGE_NAME":                     input.ImageName,
		"IMAGEBUILDER_SOURCE_REF":                     azureEvidenceSourceRef(input),
		"IMAGEBUILDER_EVIDENCE_REGISTRY_REPOSITORY":   input.Evidence.RegistryRepository,
		"IMAGEBUILDER_EVIDENCE_SBOM_FORMAT":           input.Evidence.SBOMFormat,
		"IMAGEBUILDER_EVIDENCE_VULNERABILITY_SCANNER": input.Evidence.VulnerabilityScanner,
		"IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY":      strings.Join(input.Evidence.FailOnSeverity, ","),
		"IMAGEBUILDER_EVIDENCE_COSIGN_KEY_REF":        input.EvidenceCosignKey,
	}
	keys := []string{
		"IMAGEBUILDER_BUILD_ID",
		"IMAGEBUILDER_IMAGE_NAME",
		"IMAGEBUILDER_SOURCE_REF",
		"IMAGEBUILDER_EVIDENCE_REGISTRY_REPOSITORY",
		"IMAGEBUILDER_EVIDENCE_SBOM_FORMAT",
		"IMAGEBUILDER_EVIDENCE_VULNERABILITY_SCANNER",
		"IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY",
		"IMAGEBUILDER_EVIDENCE_COSIGN_KEY_REF",
	}
	lines := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		lines = append(lines, "export "+key+"="+shellQuoteAzure(values[key]))
	}
	lines = append(lines, script)
	return strings.Join(lines, "\n")
}

func azureWindowsEvidenceCommand(input azureRemoteBuildInput, script string) string {
	values := map[string]string{
		"IMAGEBUILDER_BUILD_ID":                       input.BuildID,
		"IMAGEBUILDER_IMAGE_NAME":                     input.ImageName,
		"IMAGEBUILDER_SOURCE_REF":                     azureEvidenceSourceRef(input),
		"IMAGEBUILDER_EVIDENCE_REGISTRY_REPOSITORY":   input.Evidence.RegistryRepository,
		"IMAGEBUILDER_EVIDENCE_SBOM_FORMAT":           input.Evidence.SBOMFormat,
		"IMAGEBUILDER_EVIDENCE_VULNERABILITY_SCANNER": input.Evidence.VulnerabilityScanner,
		"IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY":      strings.Join(input.Evidence.FailOnSeverity, ","),
		"IMAGEBUILDER_EVIDENCE_COSIGN_KEY_REF":        input.EvidenceCosignKey,
	}
	keys := []string{
		"IMAGEBUILDER_BUILD_ID",
		"IMAGEBUILDER_IMAGE_NAME",
		"IMAGEBUILDER_SOURCE_REF",
		"IMAGEBUILDER_EVIDENCE_REGISTRY_REPOSITORY",
		"IMAGEBUILDER_EVIDENCE_SBOM_FORMAT",
		"IMAGEBUILDER_EVIDENCE_VULNERABILITY_SCANNER",
		"IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY",
		"IMAGEBUILDER_EVIDENCE_COSIGN_KEY_REF",
	}
	lines := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		lines = append(lines, "$env:"+key+" = "+powershellQuoteAzure(values[key]))
	}
	lines = append(lines, script)
	return strings.Join(lines, "\n")
}

func azureEvidenceSourceRef(input azureRemoteBuildInput) string {
	if strings.TrimSpace(input.SourceRef) != "" {
		return strings.TrimSpace(input.SourceRef)
	}
	if input.SourceMarketplace != nil {
		return fmt.Sprintf(
			"azure-marketplace://%s/%s/%s/%s",
			input.SourceMarketplace.Publisher,
			input.SourceMarketplace.Offer,
			input.SourceMarketplace.SKU,
			input.SourceMarketplace.Version,
		)
	}
	return "unknown"
}

func azureFileProvisionerCommand(input azureRemoteBuildInput, content, destination string) (string, []string, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	switch input.OSFamily {
	case platform.OSFamilyLinux:
		dir := path.Dir(destination)
		command := strings.Join([]string{
			"set -eu",
			"install -d -m 0755 " + shellQuoteAzure(dir),
			"base64 -d > " + shellQuoteAzure(destination) + " <<'__IMAGEBUILDER_FILE__'",
			encoded,
			"__IMAGEBUILDER_FILE__",
			"chmod 0600 " + shellQuoteAzure(destination),
		}, "\n")
		return "RunShellScript", []string{command}, nil
	case platform.OSFamilyWindows:
		command := strings.Join([]string{
			"$destination = " + powershellQuoteAzure(destination),
			"$parent = Split-Path -Parent $destination",
			"if ($parent) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }",
			"$bytes = [Convert]::FromBase64String(" + powershellQuoteAzure(encoded) + ")",
			"[IO.File]::WriteAllBytes($destination, $bytes)",
		}, "\n")
		return "RunPowerShellScript", []string{command}, nil
	default:
		return "", nil, fmt.Errorf("file provisioner requires linux or windows OS family for Azure remote build")
	}
}

func azureRunCommandError(statuses []*armcompute.InstanceViewStatus) error {
	for _, status := range statuses {
		code := strings.ToLower(value(status.Code))
		level := value((*string)(nil))
		if status.Level != nil {
			level = strings.ToLower(string(*status.Level))
		}
		if strings.Contains(code, "failed") || strings.Contains(code, "error") || level == "error" {
			return errors.New(firstNonEmpty(value(status.Message), value(status.DisplayStatus), value(status.Code)))
		}
	}
	return nil
}

const azureEvidenceMarker = "IMAGEBUILDER_EVIDENCE_V1="

func azureEvidenceFromRunCommand(statuses []*armcompute.InstanceViewStatus) (*platform.RemoteEvidenceResult, error) {
	for _, status := range statuses {
		message := value(status.Message)
		markerIndex := strings.LastIndex(message, azureEvidenceMarker)
		if markerIndex < 0 {
			continue
		}
		encoded := strings.TrimSpace(message[markerIndex+len(azureEvidenceMarker):])
		if end := strings.IndexAny(encoded, "\r\n \t"); end >= 0 {
			encoded = encoded[:end]
		}
		payload, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode evidence result marker: %w", err)
		}
		result := &platform.RemoteEvidenceResult{}
		if err := json.Unmarshal(payload, result); err != nil {
			return nil, fmt.Errorf("decode evidence result JSON: %w", err)
		}
		if len(result.Message) > 256 {
			result.Message = result.Message[:256]
		}
		return result, nil
	}
	return nil, fmt.Errorf("evidence result marker %q was not returned by Azure Run Command", azureEvidenceMarker)
}

func validateAzureEvidence(policy *v1alpha1.EvidenceSpec, evidence *platform.RemoteEvidenceResult) error {
	if policy == nil || !policy.Required {
		return nil
	}
	if evidence == nil {
		return fmt.Errorf("required evidence was not returned before Azure image registration")
	}
	if !strings.EqualFold(strings.TrimSpace(evidence.Status), "passed") {
		return fmt.Errorf("required evidence status is %q, want passed", evidence.Status)
	}
	refs := map[string]string{
		"sbomRef":                evidence.SBOMRef,
		"vulnerabilityReportRef": evidence.VulnerabilityReportRef,
		"provenanceRef":          evidence.ProvenanceRef,
		"signatureRef":           evidence.SignatureRef,
	}
	prefix := strings.TrimSuffix(policy.RegistryRepository, "/")
	for field, ref := range refs {
		if !immutableAzureEvidenceRef(ref) {
			return fmt.Errorf("required evidence %s must be an immutable oci:// reference pinned by sha256 digest", field)
		}
		refRepository := strings.SplitN(ref, "@sha256:", 2)[0]
		if refRepository != prefix && !strings.HasPrefix(refRepository, prefix+"/") {
			return fmt.Errorf("required evidence %s is outside registry repository %q", field, policy.RegistryRepository)
		}
	}
	return nil
}

func immutableAzureEvidenceRef(ref string) bool {
	if !strings.HasPrefix(ref, "oci://") {
		return false
	}
	parts := strings.Split(ref, "@sha256:")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "oci://" || len(parts[1]) != 64 {
		return false
	}
	_, err := hex.DecodeString(parts[1])
	return err == nil
}

type azureRemoteOperationRef struct {
	BuildID          string
	VMName           string
	VMID             string
	DiskName         string
	ImageID          string
	ProvisionerIndex int
	EvidenceStatus   string
	EvidenceMessage  string
	SBOMRef          string
	VulnerabilityRef string
	ProvenanceRef    string
	SignatureRef     string
}

func (r azureRemoteOperationRef) String() string {
	values := url.Values{}
	if r.VMName != "" {
		values.Set("vmName", r.VMName)
	}
	if r.VMID != "" {
		values.Set("vmId", r.VMID)
	}
	if r.DiskName != "" {
		values.Set("diskName", r.DiskName)
	}
	if r.ImageID != "" {
		values.Set("imageId", r.ImageID)
	}
	if r.ProvisionerIndex > 0 {
		values.Set("provisionerIndex", strconv.Itoa(r.ProvisionerIndex))
	}
	if r.EvidenceStatus != "" {
		values.Set("evidenceStatus", r.EvidenceStatus)
		values.Set("evidenceMessage", r.EvidenceMessage)
		values.Set("sbomRef", r.SBOMRef)
		values.Set("vulnerabilityRef", r.VulnerabilityRef)
		values.Set("provenanceRef", r.ProvenanceRef)
		values.Set("signatureRef", r.SignatureRef)
	}
	u := url.URL{Scheme: "azure", Host: "remote-build", Path: "/" + r.BuildID, RawQuery: values.Encode()}
	return u.String()
}

func parseAzureRemoteOperationRef(value string) (azureRemoteOperationRef, error) {
	if value == "" {
		return azureRemoteOperationRef{}, nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return azureRemoteOperationRef{}, fmt.Errorf("parse Azure remote operation ref: %w", err)
	}
	if u.Scheme != "azure" || u.Host != "remote-build" {
		return azureRemoteOperationRef{}, fmt.Errorf("invalid Azure remote operation ref %q", value)
	}
	index := 0
	if rawIndex := u.Query().Get("provisionerIndex"); rawIndex != "" {
		parsed, err := strconv.Atoi(rawIndex)
		if err != nil || parsed < 0 {
			return azureRemoteOperationRef{}, fmt.Errorf("invalid Azure remote operation ref provisionerIndex %q", rawIndex)
		}
		index = parsed
	}
	return azureRemoteOperationRef{
		BuildID:          strings.TrimPrefix(u.Path, "/"),
		VMName:           u.Query().Get("vmName"),
		VMID:             u.Query().Get("vmId"),
		DiskName:         u.Query().Get("diskName"),
		ImageID:          u.Query().Get("imageId"),
		ProvisionerIndex: index,
		EvidenceStatus:   u.Query().Get("evidenceStatus"),
		EvidenceMessage:  u.Query().Get("evidenceMessage"),
		SBOMRef:          u.Query().Get("sbomRef"),
		VulnerabilityRef: u.Query().Get("vulnerabilityRef"),
		ProvenanceRef:    u.Query().Get("provenanceRef"),
		SignatureRef:     u.Query().Get("signatureRef"),
	}, nil
}

func (r *azureRemoteOperationRef) setEvidence(evidence *platform.RemoteEvidenceResult) {
	if r == nil || evidence == nil {
		return
	}
	r.EvidenceStatus = evidence.Status
	r.EvidenceMessage = evidence.Message
	r.SBOMRef = evidence.SBOMRef
	r.VulnerabilityRef = evidence.VulnerabilityReportRef
	r.ProvenanceRef = evidence.ProvenanceRef
	r.SignatureRef = evidence.SignatureRef
}

func (r azureRemoteOperationRef) evidence() *platform.RemoteEvidenceResult {
	if r.EvidenceStatus == "" {
		return nil
	}
	return &platform.RemoteEvidenceResult{
		Status:                 r.EvidenceStatus,
		Message:                r.EvidenceMessage,
		SBOMRef:                r.SBOMRef,
		VulnerabilityReportRef: r.VulnerabilityRef,
		ProvenanceRef:          r.ProvenanceRef,
		SignatureRef:           r.SignatureRef,
	}
}

func azureRemoteVMName(buildID string) string {
	return "ib-" + sanitizeName(buildID) + "-vm"
}

func azureRemoteComputerName(buildID string) string {
	name := sanitizeName(buildID)
	if len(name) > 64 {
		name = name[:64]
	}
	return firstNonEmpty(name, "imagebuilder")
}

func azureRemoteDiskName(buildID string) string {
	return "ib-" + sanitizeName(buildID) + "-osdisk"
}

func virtualMachineID(subscriptionID, resourceGroup, vmName string) string {
	return "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup + "/providers/Microsoft.Compute/virtualMachines/" + vmName
}

func managedDiskID(subscriptionID, resourceGroup, diskName string) string {
	return "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup + "/providers/Microsoft.Compute/disks/" + diskName
}

func azureRemoteHygiene(input azureRemoteBuildInput, resultRef string) *platform.RemoteHygieneResult {
	checks := []string{"azure-run-command-provisioners", "azure-temporary-vm-deallocated"}
	if len(input.Provisioners) > 0 {
		checks = append(checks, "azure-provisioners-completed")
	}
	return &platform.RemoteHygieneResult{
		Status:    "passed",
		Message:   "Azure remote build completed through Run Command",
		Checks:    checks,
		ResultRef: resultRef,
	}
}

func stringPtrs(values []string) []*string {
	out := make([]*string, 0, len(values))
	for _, value := range values {
		out = append(out, to.Ptr(value))
	}
	return out
}

func shellQuoteAzure(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func powershellQuoteAzure(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
