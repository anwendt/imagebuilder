package openstack

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/remotecli"
	provisionersource "github.com/anwendt/imagebuilder/pkg/provisioner/source"
)

type openStackRemoteBuildInput struct {
	BuildID            string
	OperationRef       string
	ImageName          string
	SourceType         string
	SourceRef          string
	SourceMarketplace  *v1alpha1.MarketplaceRef
	SourceChecksum     string
	OSFamily           platform.OSFamily
	OSArch             string
	Format             platform.ImageFormat
	Tags               map[string]string
	ProviderConfigName string
	Provisioners       []v1alpha1.ProvisionerSpec
	GuestAccess        *v1alpha1.GuestAccessSpec
}

type openStackRemoteBuildState struct {
	OperationRef string
	Phase        platform.RemoteBuildPhase
	Message      string
	Done         bool
	Image        *platform.ImageRef
	Hygiene      *platform.RemoteHygieneResult
}

type openStackServerCreateInput struct {
	Name           string
	ImageRef       string
	FlavorRef      string
	KeyName        string
	SecurityGroups []string
	UserData       string
	Networks       any
	Metadata       map[string]string
	ConfigDrive    *bool
}

type openStackServer struct {
	ID        string
	Name      string
	Status    string
	AccessIP  string
	Addresses map[string]any
}

func (c *gophercloudClient) ReconcileRemoteBuild(ctx context.Context, input openStackRemoteBuildInput) (*openStackRemoteBuildState, error) {
	expanded, cleanup, err := expandOpenStackRemoteProvisioners(ctx, input)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	input = expanded
	if input.SourceType != "cloud-image" && input.SourceType != "marketplace" && input.SourceType != "snapshot" {
		return nil, fmt.Errorf("OpenStack remote build supports source type cloud-image, marketplace, or snapshot, got %q", input.SourceType)
	}
	if input.SourceType == "marketplace" && input.SourceRef == "" && input.SourceMarketplace != nil {
		sourceRef, err := c.resolveMarketplaceImage(ctx, input)
		if err != nil {
			return nil, err
		}
		input.SourceRef = sourceRef
	}
	if input.SourceRef == "" {
		return nil, fmt.Errorf("OpenStack remote build source providerRef is required")
	}
	if input.Format != platform.FormatQCOW2 && input.Format != platform.FormatRaw {
		return nil, fmt.Errorf("OpenStack remote build requires target format qcow2 or raw, got %q", input.Format)
	}
	if err := validateOpenStackRemoteProvisioners(input); err != nil {
		return nil, err
	}
	settings := openStackRemoteSettingsFromConfig(c.cfg)
	if err := settings.validate(input, c.cfg); err != nil {
		return nil, err
	}
	ref, err := parseOpenStackRemoteOperationRef(input.OperationRef)
	if err != nil {
		return nil, err
	}
	if ref.ImageID != "" {
		image, err := c.GetImage(ctx, ref.ImageID)
		if err != nil {
			return nil, fmt.Errorf("read completed OpenStack remote image %q: %w", ref.ImageID, err)
		}
		return &openStackRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseReady,
			Message:      "OpenStack remote image is ready",
			Done:         true,
			Image:        image,
			Hygiene:      openStackRemoteHygiene(input, ref.ImageID),
		}, nil
	}
	if ref.ServerID == "" {
		return c.startOpenStackRemoteServer(ctx, input, settings)
	}
	if ref.ProvisionerIndex < len(input.Provisioners) {
		if err := c.runOpenStackProvisioner(ctx, input, settings, ref, input.Provisioners[ref.ProvisionerIndex]); err != nil {
			_ = c.cleanupOpenStackRemoteResources(ctx, ref)
			return nil, err
		}
		ref.ProvisionerIndex++
		return &openStackRemoteBuildState{
			OperationRef: ref.String(),
			Phase:        platform.RemoteBuildPhaseProvisioning,
			Message:      fmt.Sprintf("OpenStack SSH provisioner %d completed", ref.ProvisionerIndex-1),
		}, nil
	}
	return c.finishOpenStackRemoteServer(ctx, input, ref)
}

func (c *gophercloudClient) resolveMarketplaceImage(ctx context.Context, input openStackRemoteBuildInput) (string, error) {
	ref := input.SourceMarketplace
	if ref == nil {
		return "", fmt.Errorf("OpenStack marketplace source requires source.marketplaceRef or source.providerRef")
	}
	if strings.TrimSpace(ref.Publisher) == "" || strings.TrimSpace(ref.Offer) == "" || strings.TrimSpace(ref.SKU) == "" || strings.TrimSpace(ref.Version) == "" {
		return "", fmt.Errorf("OpenStack marketplace source requires source.marketplaceRef publisher, offer, sku, and version")
	}
	page, err := images.List(c.image, images.ListOpts{
		Visibility: images.ImageVisibilityPublic,
		Status:     images.ImageStatusActive,
		Sort:       "updated_at:desc",
		Limit:      100,
	}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("list OpenStack marketplace source images: %w", err)
	}
	list, err := images.ExtractImages(page)
	if err != nil {
		return "", fmt.Errorf("extract OpenStack marketplace source images: %w", err)
	}
	image := selectOpenStackMarketplaceImage(input, list)
	if image == nil {
		return "", fmt.Errorf("OpenStack marketplace source image was not found for publisher=%q offer=%q sku=%q version=%q", ref.Publisher, ref.Offer, ref.SKU, ref.Version)
	}
	return image.ID, nil
}

func selectOpenStackMarketplaceImage(input openStackRemoteBuildInput, list []images.Image) *images.Image {
	matches := make([]images.Image, 0, len(list))
	for _, image := range list {
		if openStackMarketplaceImageMatches(input, image) {
			matches = append(matches, image)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if !matches[i].UpdatedAt.Equal(matches[j].UpdatedAt) {
			return matches[i].UpdatedAt.After(matches[j].UpdatedAt)
		}
		return matches[i].CreatedAt.After(matches[j].CreatedAt)
	})
	return &matches[0]
}

func openStackMarketplaceImageMatches(input openStackRemoteBuildInput, image images.Image) bool {
	ref := input.SourceMarketplace
	if ref == nil || image.ID == "" || image.Status != images.ImageStatusActive {
		return false
	}
	version := strings.TrimSpace(ref.Version)
	name := normalizeOpenStackMarketplaceText(image.Name)
	properties := normalizeOpenStackMarketplaceText(openStackImagePropertyText(image.Properties))
	haystack := strings.TrimSpace(name + " " + properties)
	if haystack == "" {
		return false
	}
	offer := normalizeOpenStackMarketplaceText(ref.Offer)
	sku := normalizeOpenStackMarketplaceText(ref.SKU)
	if !strings.Contains(haystack, offer) || !strings.Contains(haystack, sku) {
		return false
	}
	if input.OSArch != "" && !openStackMarketplaceArchMatches(input.OSArch, haystack) {
		return false
	}
	if strings.EqualFold(version, "latest") {
		return true
	}
	return strings.Contains(haystack, normalizeOpenStackMarketplaceText(version))
}

func openStackImagePropertyText(properties map[string]any) string {
	if len(properties) == 0 {
		return ""
	}
	parts := make([]string, 0, len(properties))
	for key, value := range properties {
		parts = append(parts, key, fmt.Sprint(value))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func normalizeOpenStackMarketplaceText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func openStackMarketplaceArchMatches(arch, haystack string) bool {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "amd64", "x86_64", "x64":
		return strings.Contains(haystack, "amd64") || strings.Contains(haystack, "x86 64") || strings.Contains(haystack, "x64") || !strings.Contains(haystack, "arm64")
	case "arm64", "aarch64":
		return strings.Contains(haystack, "arm64") || strings.Contains(haystack, "aarch64")
	default:
		return true
	}
}

func expandOpenStackRemoteProvisioners(ctx context.Context, input openStackRemoteBuildInput) (openStackRemoteBuildInput, func(), error) {
	if !provisionersource.HasSources(input.Provisioners) {
		return input, func() {}, nil
	}
	workspace, err := os.MkdirTemp("", "imagebuilder-openstack-provisioners-*")
	if err != nil {
		return input, func() {}, fmt.Errorf("create OpenStack remote provisioner source workspace: %w", err)
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

func (c *gophercloudClient) CleanupRemoteBuild(ctx context.Context, input openStackRemoteBuildInput) error {
	ref, err := parseOpenStackRemoteOperationRef(input.OperationRef)
	if err != nil {
		return err
	}
	if ref.BuildID == "" {
		ref.BuildID = input.BuildID
	}
	if ref.ServerName == "" && input.BuildID != "" {
		ref.ServerName = openStackRemoteServerName(input.BuildID)
	}
	return c.cleanupOpenStackRemoteResources(ctx, ref)
}

func (c *gophercloudClient) startOpenStackRemoteServer(ctx context.Context, input openStackRemoteBuildInput, settings openStackRemoteSettings) (*openStackRemoteBuildState, error) {
	ref := openStackRemoteOperationRef{
		BuildID:    input.BuildID,
		ServerName: openStackRemoteServerName(input.BuildID),
	}
	configDrive := settings.ConfigDrive
	server, err := c.createServer(ctx, openStackServerCreateInput{
		Name:           ref.ServerName,
		ImageRef:       input.SourceRef,
		FlavorRef:      settings.FlavorRef,
		KeyName:        settings.KeyName,
		SecurityGroups: settings.SecurityGroups,
		Networks:       settings.networks(),
		ConfigDrive:    &configDrive,
		Metadata: map[string]string{
			"imagebuilder_build_id": input.BuildID,
			"imagebuilder_managed":  "true",
		},
	})
	if err != nil {
		_ = c.cleanupOpenStackRemoteResources(ctx, ref)
		return nil, err
	}
	ref.ServerID = server.ID
	if err := c.waitServerStatus(ctx, server.ID, "ACTIVE"); err != nil {
		_ = c.cleanupOpenStackRemoteResources(ctx, ref)
		return nil, fmt.Errorf("wait for OpenStack remote build server %q ACTIVE: %w", server.ID, err)
	}
	return &openStackRemoteBuildState{
		OperationRef: ref.String(),
		Phase:        platform.RemoteBuildPhaseBooting,
		Message:      "OpenStack remote build server started",
	}, nil
}

func (c *gophercloudClient) runOpenStackProvisioner(ctx context.Context, input openStackRemoteBuildInput, settings openStackRemoteSettings, ref openStackRemoteOperationRef, spec v1alpha1.ProvisionerSpec) error {
	server, err := c.getServer(ctx, ref.ServerID)
	if err != nil {
		return fmt.Errorf("read OpenStack remote build server %q: %w", ref.ServerID, err)
	}
	address := firstNonEmpty(server.AccessIP, openStackServerAddress(server, settings.NetworkName))
	if address == "" {
		return fmt.Errorf("OpenStack remote build server %q has no reachable IPv4 address", ref.ServerID)
	}
	workspace, cleanup, err := openStackSSHWorkspace(c.cfg.remotePrivateKey)
	if err != nil {
		return err
	}
	defer cleanup()
	req := &provisioner.RunRequest{
		WorkspaceDir: workspace,
		VMAddress:    address,
		VMUser:       settings.SSHUser,
		Protocol:     "ssh",
		SSHPort:      settings.SSHPort,
		SSHKeyPath:   path.Join(workspace, "id_ed25519"),
		OS:           string(input.OSFamily),
		Spec:         spec,
	}
	command, upload, err := openStackProvisionerCommand(input, spec)
	if err != nil {
		return err
	}
	if upload {
		if _, err := remotecli.UploadInline(ctx, nil, req, spec.Inline, "openstack-file", spec.Args[0]); err != nil {
			return fmt.Errorf("upload OpenStack file provisioner %d: %w", ref.ProvisionerIndex, err)
		}
		return nil
	}
	if err := remotecli.RunSSH(ctx, nil, req, command); err != nil {
		return fmt.Errorf("run OpenStack provisioner %d: %w", ref.ProvisionerIndex, err)
	}
	return nil
}

func (c *gophercloudClient) finishOpenStackRemoteServer(ctx context.Context, input openStackRemoteBuildInput, ref openStackRemoteOperationRef) (*openStackRemoteBuildState, error) {
	if err := c.stopServer(ctx, ref.ServerID); err != nil {
		_ = c.cleanupOpenStackRemoteResources(ctx, ref)
		return nil, fmt.Errorf("stop OpenStack remote build server %q: %w", ref.ServerID, err)
	}
	if err := c.waitServerStatus(ctx, ref.ServerID, "SHUTOFF"); err != nil {
		_ = c.cleanupOpenStackRemoteResources(ctx, ref)
		return nil, fmt.Errorf("wait for OpenStack remote build server %q SHUTOFF: %w", ref.ServerID, err)
	}
	imageID, err := c.createServerImage(ctx, ref.ServerID, input.ImageName, map[string]string{
		"imagebuilder_build_id": input.BuildID,
		"imagebuilder_managed":  "true",
	})
	if err != nil {
		_ = c.cleanupOpenStackRemoteResources(ctx, ref)
		return nil, fmt.Errorf("create OpenStack image from server %q: %w", ref.ServerID, err)
	}
	ref.ImageID = imageID
	image, err := c.GetImage(ctx, imageID)
	if err != nil {
		_ = c.cleanupOpenStackRemoteResources(ctx, ref)
		return nil, fmt.Errorf("wait for OpenStack remote image %q: %w", imageID, err)
	}
	if err := c.cleanupOpenStackRemoteResources(ctx, ref); err != nil {
		return nil, fmt.Errorf("cleanup OpenStack remote build server after image creation: %w", err)
	}
	return &openStackRemoteBuildState{
		OperationRef: ref.String(),
		Phase:        platform.RemoteBuildPhaseReady,
		Message:      "OpenStack remote image created from provisioned server",
		Done:         true,
		Image:        image,
		Hygiene:      openStackRemoteHygiene(input, image.ID),
	}, nil
}

func (c *gophercloudClient) cleanupOpenStackRemoteResources(ctx context.Context, ref openStackRemoteOperationRef) error {
	var errs []error
	if ref.ServerID != "" {
		if err := c.deleteServer(ctx, ref.ServerID); err != nil {
			errs = append(errs, fmt.Errorf("delete OpenStack remote build server %q: %w", ref.ServerID, err))
		}
	}
	return errors.Join(errs...)
}

type openStackRemoteSettings struct {
	FlavorRef      string
	NetworkID      string
	NetworkName    string
	KeyName        string
	SSHUser        string
	SSHPort        int
	SecurityGroups []string
	ConfigDrive    bool
}

func openStackRemoteSettingsFromConfig(cfg openStackConfig) openStackRemoteSettings {
	return openStackRemoteSettings{
		FlavorRef:      firstNonEmpty(cfg.extraConfig["remote.flavorRef"], cfg.extraConfig["remote.flavorID"], cfg.extraConfig["flavorRef"]),
		NetworkID:      firstNonEmpty(cfg.extraConfig["remote.networkID"], cfg.extraConfig["remote.networkId"], cfg.extraConfig["networkID"]),
		NetworkName:    cfg.extraConfig["remote.networkName"],
		KeyName:        firstNonEmpty(cfg.extraConfig["remote.keyName"], cfg.extraConfig["keyName"]),
		SSHUser:        firstNonEmpty(cfg.extraConfig["remote.sshUser"], cfg.extraConfig["sshUser"]),
		SSHPort:        parseIntDefault(firstNonEmpty(cfg.extraConfig["remote.sshPort"], cfg.extraConfig["sshPort"]), 22),
		SecurityGroups: splitCSV(firstNonEmpty(cfg.extraConfig["remote.securityGroups"], cfg.extraConfig["securityGroups"])),
		ConfigDrive:    parseBoolDefault(firstNonEmpty(cfg.extraConfig["remote.configDrive"], cfg.extraConfig["configDrive"]), false),
	}
}

func (s openStackRemoteSettings) validate(input openStackRemoteBuildInput, cfg openStackConfig) error {
	if input.BuildID == "" {
		return fmt.Errorf("OpenStack remote build requires build ID")
	}
	if s.FlavorRef == "" {
		return fmt.Errorf("OpenStack remote build requires ProviderConfig extra remote.flavorRef")
	}
	if openStackRemoteRequiresSSH(input) {
		if s.KeyName == "" {
			return fmt.Errorf("OpenStack remote build with provisioners requires ProviderConfig extra remote.keyName")
		}
		if s.SSHUser == "" {
			return fmt.Errorf("OpenStack remote build with provisioners requires ProviderConfig extra remote.sshUser")
		}
		if strings.TrimSpace(cfg.remotePrivateKey) == "" {
			return fmt.Errorf("OpenStack remote build with provisioners requires credential key remotePrivateKey")
		}
	}
	return nil
}

func (s openStackRemoteSettings) networks() any {
	if s.NetworkID == "" {
		return "auto"
	}
	return []servers.Network{{UUID: s.NetworkID}}
}

func openStackRemoteRequiresSSH(input openStackRemoteBuildInput) bool {
	return len(input.Provisioners) > 0
}

func validateOpenStackRemoteProvisioners(input openStackRemoteBuildInput) error {
	for _, spec := range input.Provisioners {
		switch spec.Type {
		case "shell":
			if input.OSFamily != platform.OSFamilyLinux {
				return fmt.Errorf("shell provisioner requires Linux for OpenStack remote build")
			}
			if strings.TrimSpace(spec.Inline) == "" {
				return fmt.Errorf("shell provisioner requires inline content for OpenStack remote build")
			}
		case "file":
			if input.OSFamily != platform.OSFamilyLinux {
				return fmt.Errorf("file provisioner currently requires Linux for OpenStack remote build")
			}
			if strings.TrimSpace(spec.Inline) == "" {
				return fmt.Errorf("file provisioner requires inline content for OpenStack remote build")
			}
			if len(spec.Args) != 1 || strings.TrimSpace(spec.Args[0]) == "" {
				return fmt.Errorf("file provisioner requires destination path in args[0] for OpenStack remote build")
			}
		default:
			return fmt.Errorf("provisioner type %q is not supported by OpenStack remote build", spec.Type)
		}
	}
	return nil
}

func openStackProvisionerCommand(input openStackRemoteBuildInput, spec v1alpha1.ProvisionerSpec) (string, bool, error) {
	switch spec.Type {
	case "shell":
		return spec.Inline, false, nil
	case "file":
		return "", true, nil
	default:
		return "", false, fmt.Errorf("provisioner type %q is not supported by OpenStack remote build", spec.Type)
	}
}

func openStackSSHWorkspace(privateKey string) (string, func(), error) {
	workspace, err := os.MkdirTemp("", "imagebuilder-openstack-ssh-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create OpenStack SSH workspace: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	keyPath := path.Join(workspace, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte(strings.TrimSpace(privateKey)+"\n"), 0o600); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write OpenStack remote SSH key: %w", err)
	}
	return workspace, cleanup, nil
}

type openStackRemoteOperationRef struct {
	BuildID          string
	ServerID         string
	ServerName       string
	ImageID          string
	ProvisionerIndex int
}

func (r openStackRemoteOperationRef) String() string {
	values := url.Values{}
	if r.ServerID != "" {
		values.Set("serverId", r.ServerID)
	}
	if r.ServerName != "" {
		values.Set("serverName", r.ServerName)
	}
	if r.ImageID != "" {
		values.Set("imageId", r.ImageID)
	}
	if r.ProvisionerIndex > 0 {
		values.Set("provisionerIndex", strconv.Itoa(r.ProvisionerIndex))
	}
	u := url.URL{Scheme: "openstack", Host: "remote-build", Path: "/" + r.BuildID, RawQuery: values.Encode()}
	return u.String()
}

func parseOpenStackRemoteOperationRef(value string) (openStackRemoteOperationRef, error) {
	if value == "" {
		return openStackRemoteOperationRef{}, nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return openStackRemoteOperationRef{}, fmt.Errorf("parse OpenStack remote operation ref: %w", err)
	}
	if u.Scheme != "openstack" || u.Host != "remote-build" {
		return openStackRemoteOperationRef{}, fmt.Errorf("invalid OpenStack remote operation ref %q", value)
	}
	index := 0
	if rawIndex := u.Query().Get("provisionerIndex"); rawIndex != "" {
		parsed, err := strconv.Atoi(rawIndex)
		if err != nil || parsed < 0 {
			return openStackRemoteOperationRef{}, fmt.Errorf("invalid OpenStack remote operation ref provisionerIndex %q", rawIndex)
		}
		index = parsed
	}
	return openStackRemoteOperationRef{
		BuildID:          strings.TrimPrefix(u.Path, "/"),
		ServerID:         u.Query().Get("serverId"),
		ServerName:       u.Query().Get("serverName"),
		ImageID:          u.Query().Get("imageId"),
		ProvisionerIndex: index,
	}, nil
}

func openStackRemoteServerName(buildID string) string {
	return "ib-" + sanitizeOpenStackName(buildID) + "-remote"
}

func openStackRemoteHygiene(input openStackRemoteBuildInput, resultRef string) *platform.RemoteHygieneResult {
	checks := []string{"openstack-server-snapshot", "openstack-temporary-server-deleted"}
	if len(input.Provisioners) > 0 {
		checks = append(checks, "openstack-ssh-provisioners-completed")
	}
	return &platform.RemoteHygieneResult{
		Status:    "passed",
		Message:   "OpenStack remote build completed through Nova server snapshot",
		Checks:    checks,
		ResultRef: resultRef,
	}
}

func openStackServerFromNova(server *servers.Server) *openStackServer {
	if server == nil {
		return nil
	}
	return &openStackServer{
		ID:        server.ID,
		Name:      server.Name,
		Status:    server.Status,
		AccessIP:  firstNonEmpty(server.AccessIPv4, server.AccessIPv6),
		Addresses: server.Addresses,
	}
}

func openStackServerAddress(server *openStackServer, preferredNetwork string) string {
	if server == nil {
		return ""
	}
	if preferredNetwork != "" {
		if address := addressFromPool(server.Addresses[preferredNetwork]); address != "" {
			return address
		}
	}
	for _, pool := range server.Addresses {
		if address := addressFromPool(pool); address != "" {
			return address
		}
	}
	return ""
}

func addressFromPool(pool any) string {
	values, ok := pool.([]any)
	if !ok {
		return ""
	}
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if version, _ := item["version"].(float64); version != 0 && int(version) != 4 {
			continue
		}
		if addr, _ := item["addr"].(string); addr != "" {
			return addr
		}
	}
	return ""
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			out = append(out, item)
		}
	}
	return out
}
