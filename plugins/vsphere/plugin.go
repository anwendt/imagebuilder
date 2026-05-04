package vsphere

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf/importer"
	"github.com/vmware/govmomi/vapi/library"
	"github.com/vmware/govmomi/vapi/rest"
	"github.com/vmware/govmomi/vim25/soap"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func init() {
	if err := plugin.Register(&Plugin{}); err != nil {
		panic(fmt.Sprintf("vsphere plugin: %v", err))
	}
}

type Plugin struct {
	log    *slog.Logger
	config config
	client client
}

var _ platform.RemoteBuildCleanupPlugin = (*Plugin)(nil)

type config struct {
	providerConfigName string
	endpoint           string
	insecure           bool
	username           string
	password           string
	guestUsername      string
	guestPassword      string
	datacenter         string
	datastore          string
	folder             string
	cluster            string
	resourcePool       string
	host               string
	network            string
	ovfNetworkName     string
	diskProvisioning   string
	deployment         string
	ipAllocationPolicy string
	ipProtocol         string
	annotation         string
	markAsTemplate     bool
	contentLibrary     string
	contentLibraryID   string
	requireManifest    bool
	uploadPathPrefix   string
	extraConfig        map[string]string
}

type client interface {
	UploadArtifact(ctx context.Context, input uploadInput) (*platform.UploadResult, error)
	RegisterImage(ctx context.Context, input registerInput) (*platform.ImageRef, error)
	ReconcileRemoteBuild(ctx context.Context, input vsphereRemoteBuildInput) (*vsphereRemoteBuildState, error)
	CleanupRemoteBuild(ctx context.Context, input vsphereRemoteBuildInput) error
	Cleanup(ctx context.Context, metadata map[string]string) error
	HealthCheck(ctx context.Context) error
}

type uploadInput struct {
	ArtifactPath  string
	Datastore     string
	Datacenter    string
	DatastorePath string
	Format        platform.ImageFormat
	Checksum      string
	SizeBytes     int64
	BuildID       string
	ImageName     string
}

type registerInput struct {
	ProviderRef  string
	ArtifactPath string
	ImageName    string
	Datacenter   string
	Datastore    string
	Format       platform.ImageFormat
	Checksum     string
	Tags         map[string]string
	Metadata     map[string]string
}

func (p *Plugin) Name() string    { return "vsphere" }
func (p *Plugin) Version() string { return "v0.1.0" }

func (p *Plugin) SupportedFormats() []platform.ImageFormat {
	return []platform.ImageFormat{
		platform.FormatOVA,
		platform.FormatOVF,
		platform.FormatVMDK,
	}
}

func (p *Plugin) SupportedOS() []platform.OSFamily {
	return []platform.OSFamily{
		platform.OSFamilyLinux,
		platform.OSFamilyWindows,
	}
}

func (p *Plugin) SupportedBuildModes() []string {
	return []string{v1alpha1.BuildModeLocal, v1alpha1.BuildModeRemote}
}

func (p *Plugin) Init(ctx context.Context, cfg platform.PluginConfig) error {
	p.log = slog.Default().With(slog.String("plugin", p.Name()))
	p.config = config{
		providerConfigName: cfg.ProviderConfigName,
		endpoint:           strings.TrimSpace(cfg.Endpoint),
		insecure:           cfg.Insecure,
		username:           firstSecretValue(cfg.SecretData, "username", "user"),
		password:           firstSecretValue(cfg.SecretData, "password", "pass"),
		guestUsername:      firstSecretValue(cfg.SecretData, "guestUsername", "remoteGuestUsername"),
		guestPassword:      firstSecretValue(cfg.SecretData, "guestPassword", "remoteGuestPassword"),
		datacenter:         strings.TrimSpace(cfg.Extra["datacenter"]),
		datastore:          strings.TrimSpace(cfg.Extra["datastore"]),
		folder:             strings.TrimSpace(cfg.Extra["folder"]),
		cluster:            strings.TrimSpace(cfg.Extra["cluster"]),
		resourcePool:       strings.TrimSpace(cfg.Extra["resourcePool"]),
		host:               strings.TrimSpace(cfg.Extra["host"]),
		network:            strings.TrimSpace(cfg.Extra["network"]),
		ovfNetworkName:     strings.TrimSpace(cfg.Extra["ovfNetworkName"]),
		diskProvisioning:   strings.TrimSpace(firstNonEmpty(cfg.Extra["diskProvisioning"], "thin")),
		deployment:         strings.TrimSpace(cfg.Extra["deployment"]),
		ipAllocationPolicy: strings.TrimSpace(cfg.Extra["ipAllocationPolicy"]),
		ipProtocol:         strings.TrimSpace(cfg.Extra["ipProtocol"]),
		annotation:         strings.TrimSpace(cfg.Extra["annotation"]),
		markAsTemplate:     boolFromExtra(cfg.Extra, "markAsTemplate", true),
		contentLibrary:     strings.TrimSpace(cfg.Extra["contentLibrary"]),
		contentLibraryID:   strings.TrimSpace(cfg.Extra["contentLibraryID"]),
		requireManifest:    boolFromExtra(cfg.Extra, "requireManifest", false),
		uploadPathPrefix:   strings.TrimSpace(firstNonEmpty(cfg.Extra["uploadPathPrefix"], cfg.Extra["uploadPath"], "imagebuilder")),
		extraConfig:        cloneStringMap(cfg.Extra),
	}
	if err := p.config.validate(); err != nil {
		return err
	}
	if p.client == nil {
		c, err := newGovmomiClient(ctx, p.config)
		if err != nil {
			return fmt.Errorf("vsphere plugin: initialise govmomi client: %w", err)
		}
		p.client = c
	}
	p.log.Info("vsphere plugin initialised",
		slog.String("endpoint", p.config.endpoint),
		slog.String("datacenter", p.config.datacenter),
		slog.String("datastore", p.config.datastore),
	)
	return nil
}

func (c config) validate() error {
	if c.endpoint == "" {
		return fmt.Errorf("vsphere plugin: endpoint is required in ProviderConfig")
	}
	if c.username == "" || c.password == "" {
		return fmt.Errorf("vsphere plugin: secret must contain username and password")
	}
	if c.datacenter == "" {
		return fmt.Errorf("vsphere plugin: ProviderConfig extra datacenter is required")
	}
	if c.datastore == "" {
		return fmt.Errorf("vsphere plugin: ProviderConfig extra datastore is required")
	}
	return nil
}

func (p *Plugin) Validate(_ context.Context, spec v1alpha1.TargetSpec) error {
	switch platform.ImageFormat(spec.Format) {
	case platform.FormatOVA, platform.FormatOVF, platform.FormatVMDK:
		return nil
	default:
		return fmt.Errorf("vsphere plugin: unsupported format %q; use ova, ovf, or vmdk", spec.Format)
	}
}

func (p *Plugin) Upload(ctx context.Context, artifact *platform.BuildArtifact) (*platform.UploadResult, error) {
	if artifact == nil {
		return nil, fmt.Errorf("vsphere plugin: artifact is required")
	}
	if artifact.Path == "" {
		return nil, fmt.Errorf("vsphere plugin: artifact path is required")
	}
	if !isSupportedFormat(artifact.Format) {
		return nil, fmt.Errorf("vsphere plugin: unsupported artifact format %q", artifact.Format)
	}
	buildID := artifact.Metadata["buildID"]
	if buildID == "" {
		return nil, fmt.Errorf("vsphere plugin: artifact metadata missing required key 'buildID'")
	}
	client := p.client
	if client == nil {
		return nil, fmt.Errorf("vsphere plugin: client is not initialised")
	}
	imageName := firstNonEmpty(
		artifact.Metadata["imageName"],
		artifact.Metadata["vmimage"],
		p.config.extraConfig["imageName"],
		"imagebuilder-"+sanitizeName(buildID),
	)
	datastorePath := artifactDatastorePath(p.config.uploadPathPrefix, buildID, artifact.Path, artifact.Format)
	p.log.Info("uploading artifact to vSphere datastore",
		slog.String("path", artifact.Path),
		slog.String("datastore", p.config.datastore),
		slog.String("datastorePath", datastorePath),
	)
	result, err := client.UploadArtifact(ctx, uploadInput{
		ArtifactPath:  artifact.Path,
		Datastore:     p.config.datastore,
		Datacenter:    p.config.datacenter,
		DatastorePath: datastorePath,
		Format:        artifact.Format,
		Checksum:      artifact.Checksum,
		SizeBytes:     artifact.SizeBytes,
		BuildID:       buildID,
		ImageName:     imageName,
	})
	if err != nil {
		return nil, fmt.Errorf("vsphere plugin: upload artifact: %w", err)
	}
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["buildID"] = buildID
	result.Metadata["imageName"] = imageName
	result.Metadata["format"] = string(artifact.Format)
	result.Metadata["checksum"] = artifact.Checksum
	result.Metadata["datacenter"] = p.config.datacenter
	result.Metadata["datastore"] = p.config.datastore
	result.Metadata["datastorePath"] = datastorePath
	result.Metadata["artifactPath"] = artifact.Path

	if artifact.Metadata == nil {
		artifact.Metadata = map[string]string{}
	}
	artifact.Metadata["vsphere.datastore"] = p.config.datastore
	artifact.Metadata["vsphere.datastorePath"] = datastorePath
	artifact.Metadata["vsphere.providerRef"] = result.ProviderRef
	return result, nil
}

func (p *Plugin) Register(ctx context.Context, result *platform.UploadResult) (*platform.ImageRef, error) {
	if result == nil {
		return nil, fmt.Errorf("vsphere plugin: upload result is required")
	}
	client := p.client
	if client == nil {
		return nil, fmt.Errorf("vsphere plugin: client is not initialised")
	}
	input := registerInput{
		ProviderRef:  result.ProviderRef,
		ArtifactPath: firstNonEmpty(result.Metadata["artifactPath"], result.Metadata["localPath"]),
		ImageName:    firstNonEmpty(result.Metadata["imageName"], p.config.extraConfig["imageName"]),
		Datacenter:   firstNonEmpty(result.Metadata["datacenter"], p.config.datacenter),
		Datastore:    firstNonEmpty(result.Metadata["datastore"], p.config.datastore),
		Format:       platform.ImageFormat(result.Metadata["format"]),
		Checksum:     result.Metadata["checksum"],
		Metadata:     cloneStringMap(result.Metadata),
	}
	ref, err := client.RegisterImage(ctx, input)
	if err != nil {
		cleanupErr := client.Cleanup(ctx, result.Metadata)
		if cleanupErr != nil {
			return nil, fmt.Errorf("vsphere plugin: register image: %w; cleanup failed: %v", err, cleanupErr)
		}
		return nil, fmt.Errorf("vsphere plugin: register image: %w", err)
	}
	return ref, nil
}

func (p *Plugin) Cleanup(ctx context.Context, artifact *platform.BuildArtifact) error {
	if artifact == nil || artifact.Metadata == nil || p.client == nil {
		return nil
	}
	metadata := map[string]string{
		"providerRef":   firstNonEmpty(artifact.Metadata["vsphere.providerRef"], artifact.Metadata["providerRef"]),
		"datastore":     firstNonEmpty(artifact.Metadata["vsphere.datastore"], artifact.Metadata["datastore"], p.config.datastore),
		"datastorePath": firstNonEmpty(artifact.Metadata["vsphere.datastorePath"], artifact.Metadata["datastorePath"]),
	}
	if metadata["datastorePath"] == "" && artifact.Metadata["buildID"] != "" && artifact.Path != "" {
		metadata["datastorePath"] = artifactDatastorePath(p.config.uploadPathPrefix, artifact.Metadata["buildID"], artifact.Path, artifact.Format)
	}
	return p.client.Cleanup(ctx, metadata)
}

func (p *Plugin) HealthCheck(ctx context.Context) error {
	if p.client == nil {
		return nil
	}
	return p.client.HealthCheck(ctx)
}

func (p *Plugin) ReconcileRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) (*platform.RemoteBuildResult, error) {
	if req == nil {
		return nil, fmt.Errorf("vsphere plugin: remote build request is required")
	}
	if !isSupportedFormat(platform.ImageFormat(req.Target.Format)) {
		return nil, fmt.Errorf("vsphere plugin: unsupported remote target format %q; use ova, ovf, or vmdk", req.Target.Format)
	}
	sourceRef := strings.TrimSpace(firstNonEmpty(req.SourceProviderRef, req.SourceURL))
	if sourceRef == "" {
		return nil, fmt.Errorf("vsphere plugin: remote source requires source providerRef")
	}
	if p.client == nil {
		return nil, fmt.Errorf("vsphere plugin: client is not initialised")
	}
	state, err := p.client.ReconcileRemoteBuild(ctx, vsphereRemoteBuildInput{
		BuildID:            req.BuildID,
		OperationRef:       req.OperationRef,
		ImageName:          firstNonEmpty(req.ImageName, "imagebuilder-"+sanitizeName(req.BuildID)),
		SourceType:         strings.ToLower(strings.TrimSpace(req.SourceType)),
		SourceRef:          sourceRef,
		SourceChecksum:     req.SourceChecksum,
		OSFamily:           req.OSFamily,
		Format:             platform.ImageFormat(req.Target.Format),
		Tags:               req.Target.Tags,
		ProviderConfigName: req.Target.ProviderConfigRef.Name,
		Provisioners:       req.Provisioners,
		GuestAccess:        req.GuestAccess,
	})
	if err != nil {
		return nil, err
	}
	result := &platform.RemoteBuildResult{
		OperationRef: state.OperationRef,
		Phase:        state.Phase,
		Message:      state.Message,
		Done:         state.Done,
		Hygiene:      state.Hygiene,
	}
	if state.Done {
		if state.Image == nil || state.Image.ID == "" {
			return nil, fmt.Errorf("vsphere plugin: completed remote build did not return an image reference")
		}
		result.Images = []platform.RemoteImageRef{{
			Provider:       p.Name(),
			ProviderConfig: req.Target.ProviderConfigRef.Name,
			ImageRef:       *state.Image,
			Format:         platform.ImageFormat(req.Target.Format),
			Checksum:       req.SourceChecksum,
		}}
	}
	return result, nil
}

func (p *Plugin) CleanupRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) error {
	if req == nil || p.client == nil {
		return nil
	}
	sourceRef := strings.TrimSpace(firstNonEmpty(req.SourceProviderRef, req.SourceURL))
	return p.client.CleanupRemoteBuild(ctx, vsphereRemoteBuildInput{
		BuildID:      req.BuildID,
		OperationRef: req.OperationRef,
		ImageName:    firstNonEmpty(req.ImageName, "imagebuilder-"+sanitizeName(req.BuildID)),
		SourceType:   strings.ToLower(strings.TrimSpace(req.SourceType)),
		SourceRef:    sourceRef,
		OSFamily:     req.OSFamily,
		Format:       platform.ImageFormat(req.Target.Format),
		Tags:         req.Target.Tags,
		Provisioners: req.Provisioners,
		GuestAccess:  req.GuestAccess,
	})
}

type govmomiClient struct {
	cfg         config
	vc          *govmomi.Client
	finder      *find.Finder
	fileManager *object.FileManager
}

func newGovmomiClient(ctx context.Context, cfg config) (client, error) {
	u, err := soap.ParseURL(cfg.endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	u.User = url.UserPassword(cfg.username, cfg.password)
	vc, err := govmomi.NewClient(ctx, u, cfg.insecure)
	if err != nil {
		return nil, fmt.Errorf("connect to vCenter: %w", err)
	}
	finder := find.NewFinder(vc.Client, true)
	return &govmomiClient{
		cfg:         cfg,
		vc:          vc,
		finder:      finder,
		fileManager: object.NewFileManager(vc.Client),
	}, nil
}

func (c *govmomiClient) UploadArtifact(ctx context.Context, input uploadInput) (*platform.UploadResult, error) {
	dc, ds, err := c.resolveDatastore(ctx, input.Datacenter, input.Datastore)
	if err != nil {
		return nil, err
	}
	if err := c.fileManager.MakeDirectory(ctx, datastoreDirectory(input.Datastore, path.Dir(input.DatastorePath)), dc, true); err != nil {
		return nil, fmt.Errorf("create datastore upload directory: %w", err)
	}
	if err := ds.UploadFile(ctx, input.ArtifactPath, input.DatastorePath, nil); err != nil {
		return nil, fmt.Errorf("upload file to datastore: %w", err)
	}
	ref := datastoreReference(input.Datastore, input.DatastorePath)
	return &platform.UploadResult{
		ProviderRef: ref,
		Metadata: map[string]string{
			"providerRef":   ref,
			"datacenter":    input.Datacenter,
			"datastore":     input.Datastore,
			"datastorePath": input.DatastorePath,
			"artifactPath":  input.ArtifactPath,
			"format":        string(input.Format),
			"checksum":      input.Checksum,
			"buildID":       input.BuildID,
			"imageName":     input.ImageName,
		},
	}, nil
}

func (c *govmomiClient) RegisterImage(ctx context.Context, input registerInput) (*platform.ImageRef, error) {
	if input.ProviderRef == "" {
		return nil, fmt.Errorf("provider reference is required")
	}
	if input.Format == platform.FormatOVA || input.Format == platform.FormatOVF {
		return c.importOVF(ctx, input)
	}
	name := firstNonEmpty(input.ImageName, path.Base(input.ProviderRef))
	tags := cloneStringMap(input.Tags)
	tags["imagebuilder.io/provider"] = "vsphere"
	tags["imagebuilder.io/format"] = string(input.Format)
	tags["imagebuilder.io/registration-mode"] = "datastore-artifact"
	if input.Checksum != "" {
		tags["imagebuilder.io/checksum"] = input.Checksum
	}
	return &platform.ImageRef{
		ID:       input.ProviderRef,
		Name:     name,
		Location: input.Datacenter,
		Tags:     tags,
	}, nil
}

func (c *govmomiClient) importOVF(ctx context.Context, input registerInput) (*platform.ImageRef, error) {
	if input.ArtifactPath == "" {
		return nil, fmt.Errorf("artifact path is required to import %s as a vSphere template", input.Format)
	}
	if c.cfg.contentLibrary != "" || c.cfg.contentLibraryID != "" {
		return c.publishContentLibraryItem(ctx, input)
	}
	dc, ds, err := c.resolveDatastore(ctx, input.Datacenter, input.Datastore)
	if err != nil {
		return nil, err
	}
	finder := find.NewFinder(c.vc.Client, true)
	finder.SetDatacenter(dc)
	rp, err := c.resolveResourcePool(ctx, finder)
	if err != nil {
		return nil, err
	}
	host, err := c.resolveHost(ctx, finder)
	if err != nil {
		return nil, err
	}
	folder, err := finder.FolderOrDefault(ctx, firstNonEmpty(c.cfg.folder, "vm"))
	if err != nil {
		return nil, fmt.Errorf("find VM folder %q: %w", firstNonEmpty(c.cfg.folder, "vm"), err)
	}
	opts := importer.Options{
		DiskProvisioning:   c.cfg.diskProvisioning,
		Deployment:         c.cfg.deployment,
		IPAllocationPolicy: c.cfg.ipAllocationPolicy,
		IPProtocol:         c.cfg.ipProtocol,
		Annotation:         c.cfg.annotation,
		Name:               &input.ImageName,
		MarkAsTemplate:     c.cfg.markAsTemplate,
	}
	if opts.Name == nil || *opts.Name == "" {
		name := strings.TrimSuffix(filepath.Base(input.ArtifactPath), filepath.Ext(input.ArtifactPath))
		opts.Name = &name
	}
	archive, fpath := artifactArchive(input.ArtifactPath, input.Format)
	imp := importer.Importer{
		Log:          func(msg string) (int, error) { c.logImportWarning(msg); return len(msg), nil },
		Name:         *opts.Name,
		Client:       c.vc.Client,
		Finder:       finder,
		Datacenter:   dc,
		Datastore:    ds,
		ResourcePool: rp,
		Host:         host,
		Folder:       folder,
		Archive:      archive,
	}
	if c.cfg.network != "" {
		networks, err := importer.Spec(fpath, archive, false, false)
		if err != nil {
			return nil, fmt.Errorf("read OVF network spec: %w", err)
		}
		opts.NetworkMapping = networks.NetworkMapping
		if len(opts.NetworkMapping) == 0 && c.cfg.ovfNetworkName != "" {
			opts.NetworkMapping = []importer.Network{{Name: c.cfg.ovfNetworkName}}
		}
		if len(opts.NetworkMapping) == 0 {
			return nil, fmt.Errorf("ProviderConfig extra network requires OVF network mapping; set ovfNetworkName when the descriptor has no network section")
		}
		for i := range opts.NetworkMapping {
			opts.NetworkMapping[i].Network = c.cfg.network
		}
	}
	moref, err := imp.Import(ctx, fpath, opts)
	if err != nil {
		return nil, fmt.Errorf("import OVF/OVA: %w", err)
	}
	vm := object.NewVirtualMachine(c.vc.Client, *moref)
	if c.cfg.markAsTemplate {
		if err := vm.MarkAsTemplate(ctx); err != nil {
			return nil, fmt.Errorf("mark imported VM as template: %w", err)
		}
	}
	tags := cloneStringMap(input.Tags)
	tags["imagebuilder.io/provider"] = "vsphere"
	tags["imagebuilder.io/format"] = string(input.Format)
	tags["imagebuilder.io/registration-mode"] = "template"
	if input.Checksum != "" {
		tags["imagebuilder.io/checksum"] = input.Checksum
	}
	return &platform.ImageRef{
		ID:       moref.String(),
		Name:     *opts.Name,
		Location: input.Datacenter,
		Tags:     tags,
	}, nil
}

func (c *govmomiClient) publishContentLibraryItem(ctx context.Context, input registerInput) (*platform.ImageRef, error) {
	archive, fpath := artifactArchive(input.ArtifactPath, input.Format)
	itemName := firstNonEmpty(input.ImageName, strings.TrimSuffix(filepath.Base(input.ArtifactPath), filepath.Ext(input.ArtifactPath)))
	manifest := map[string]*library.Checksum{}
	mf := strings.Replace(filepath.Base(input.ArtifactPath), filepath.Ext(input.ArtifactPath), ".mf", 1)
	if input.Format == platform.FormatOVA {
		mf = "*.mf"
	}
	if f, _, err := archive.Open(mf); err == nil {
		sums, readErr := library.ReadManifest(f)
		_ = f.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read OVA/OVF manifest: %w", readErr)
		}
		manifest = sums
	} else if c.cfg.requireManifest {
		return nil, fmt.Errorf("required OVA/OVF manifest %q is missing: %w", mf, err)
	}

	restClient := rest.NewClient(c.vc.Client)
	if err := restClient.Login(ctx, url.UserPassword(c.cfg.username, c.cfg.password)); err != nil {
		return nil, fmt.Errorf("login to vSphere REST API: %w", err)
	}
	manager := library.NewManager(restClient)
	libraryID := c.cfg.contentLibraryID
	if libraryID == "" {
		lib, err := manager.GetLibraryByName(ctx, c.cfg.contentLibrary)
		if err != nil {
			return nil, fmt.Errorf("find content library %q: %w", c.cfg.contentLibrary, err)
		}
		libraryID = lib.ID
	}
	itemID, err := manager.CreateLibraryItem(ctx, library.Item{
		Name:      itemName,
		LibraryID: libraryID,
		Type:      library.ItemTypeOVF,
	})
	if err != nil {
		return nil, fmt.Errorf("create content library item: %w", err)
	}
	session, err := manager.CreateLibraryItemUpdateSession(ctx, library.Session{LibraryItemID: itemID})
	if err != nil {
		return nil, fmt.Errorf("create content library update session: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = manager.CancelLibraryItemUpdateSession(context.Background(), session)
		}
	}()
	if err := c.uploadLibraryArchiveFile(ctx, restClient, manager, session, archive, fpath, manifest); err != nil {
		return nil, err
	}
	o, err := importer.ReadOvf(fpath, archive)
	if err != nil {
		return nil, fmt.Errorf("read OVF descriptor: %w", err)
	}
	envelope, err := importer.ReadEnvelope(o)
	if err != nil {
		return nil, fmt.Errorf("parse OVF descriptor: %w", err)
	}
	for i := range envelope.References {
		if err := c.uploadLibraryArchiveFile(ctx, restClient, manager, session, archive, envelope.References[i].Href, manifest); err != nil {
			return nil, err
		}
	}
	if err := manager.CompleteLibraryItemUpdateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("complete content library update session: %w", err)
	}
	if err := manager.WaitOnLibraryItemUpdateSession(ctx, session, 3*time.Second, nil); err != nil {
		return nil, fmt.Errorf("wait for content library update session: %w", err)
	}
	complete = true
	tags := cloneStringMap(input.Tags)
	tags["imagebuilder.io/provider"] = "vsphere"
	tags["imagebuilder.io/format"] = string(input.Format)
	tags["imagebuilder.io/registration-mode"] = "content-library"
	if input.Checksum != "" {
		tags["imagebuilder.io/checksum"] = input.Checksum
	}
	return &platform.ImageRef{
		ID:       itemID,
		Name:     itemName,
		Location: firstNonEmpty(c.cfg.contentLibrary, c.cfg.contentLibraryID),
		Tags:     tags,
	}, nil
}

func (c *govmomiClient) uploadLibraryArchiveFile(ctx context.Context, restClient *rest.Client, manager *library.Manager, session string, archive importer.Archive, name string, manifest map[string]*library.Checksum) error {
	file, size, err := archive.Open(name)
	if err != nil {
		return fmt.Errorf("open library file %q: %w", name, err)
	}
	defer file.Close()
	if entry, ok := file.(*importer.TapeArchiveEntry); ok {
		name = entry.Name
	}
	update, err := manager.AddLibraryItemFile(ctx, session, library.UpdateFile{
		Name:       name,
		SourceType: "PUSH",
		Checksum:   manifest[name],
		Size:       size,
	})
	if err != nil {
		return fmt.Errorf("add content library file %q: %w", name, err)
	}
	uploadURL, err := url.Parse(update.UploadEndpoint.URI)
	if err != nil {
		return fmt.Errorf("parse content library upload endpoint: %w", err)
	}
	params := soap.DefaultUpload
	params.ContentLength = size
	if err := restClient.Upload(ctx, file, uploadURL, &params); err != nil {
		return fmt.Errorf("upload content library file %q: %w", name, err)
	}
	return nil
}

func (c *govmomiClient) resolveResourcePool(ctx context.Context, finder *find.Finder) (*object.ResourcePool, error) {
	if c.cfg.resourcePool != "" {
		rp, err := finder.ResourcePool(ctx, c.cfg.resourcePool)
		if err != nil {
			return nil, fmt.Errorf("find resource pool %q: %w", c.cfg.resourcePool, err)
		}
		return rp, nil
	}
	if c.cfg.host != "" {
		host, err := finder.HostSystem(ctx, c.cfg.host)
		if err != nil {
			return nil, fmt.Errorf("find host %q: %w", c.cfg.host, err)
		}
		rp, err := host.ResourcePool(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve host resource pool: %w", err)
		}
		return rp, nil
	}
	if c.cfg.cluster != "" {
		cluster, err := finder.ClusterComputeResource(ctx, c.cfg.cluster)
		if err != nil {
			return nil, fmt.Errorf("find cluster %q: %w", c.cfg.cluster, err)
		}
		rp, err := cluster.ResourcePool(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve cluster resource pool: %w", err)
		}
		return rp, nil
	}
	rp, err := finder.DefaultResourcePool(ctx)
	if err != nil {
		return nil, fmt.Errorf("find default resource pool: %w", err)
	}
	return rp, nil
}

func (c *govmomiClient) resolveHost(ctx context.Context, finder *find.Finder) (*object.HostSystem, error) {
	if c.cfg.host == "" {
		return nil, nil
	}
	host, err := finder.HostSystem(ctx, c.cfg.host)
	if err != nil {
		return nil, fmt.Errorf("find host %q: %w", c.cfg.host, err)
	}
	return host, nil
}

func (c *govmomiClient) logImportWarning(msg string) {
	if c == nil {
		return
	}
	slog.Default().With(slog.String("plugin", "vsphere")).Warn("vsphere import warning", slog.String("message", strings.TrimSpace(msg)))
}

func (c *govmomiClient) Cleanup(ctx context.Context, metadata map[string]string) error {
	if metadata == nil {
		return nil
	}
	datastore := firstNonEmpty(metadata["datastore"], c.cfg.datastore)
	datastorePath := strings.TrimSpace(metadata["datastorePath"])
	if datastorePath == "" {
		providerRef := strings.TrimSpace(metadata["providerRef"])
		if providerRef == "" {
			return nil
		}
		var ok bool
		datastore, datastorePath, ok = parseDatastoreReference(providerRef)
		if !ok {
			return nil
		}
	}
	dc, _, err := c.resolveDatastore(ctx, firstNonEmpty(metadata["datacenter"], c.cfg.datacenter), datastore)
	if err != nil {
		return err
	}
	task, err := c.fileManager.DeleteDatastoreFile(ctx, datastoreReference(datastore, datastorePath), dc)
	if err != nil {
		return fmt.Errorf("delete datastore file: %w", err)
	}
	if task == nil {
		return nil
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("wait for datastore delete: %w", err)
	}
	return nil
}

func (c *govmomiClient) HealthCheck(ctx context.Context) error {
	_, _, err := c.resolveDatastore(ctx, c.cfg.datacenter, c.cfg.datastore)
	if err != nil {
		return fmt.Errorf("resolve vSphere datastore: %w", err)
	}
	return nil
}

func (c *govmomiClient) resolveDatastore(ctx context.Context, datacenter, datastore string) (*object.Datacenter, *object.Datastore, error) {
	dc, err := c.finder.Datacenter(ctx, datacenter)
	if err != nil {
		return nil, nil, fmt.Errorf("find datacenter %q: %w", datacenter, err)
	}
	finder := find.NewFinder(c.vc.Client, true)
	finder.SetDatacenter(dc)
	ds, err := finder.Datastore(ctx, datastore)
	if err != nil {
		return nil, nil, fmt.Errorf("find datastore %q: %w", datastore, err)
	}
	return dc, ds, nil
}

func isSupportedFormat(format platform.ImageFormat) bool {
	switch format {
	case platform.FormatOVA, platform.FormatOVF, platform.FormatVMDK:
		return true
	default:
		return false
	}
}

func artifactDatastorePath(prefix, buildID, artifactPath string, format platform.ImageFormat) string {
	name := filepath.Base(artifactPath)
	if name == "." || name == "/" || name == "" {
		name = "image." + string(format)
	}
	return path.Join(strings.Trim(prefix, "/"), sanitizeName(buildID), name)
}

func artifactArchive(artifactPath string, format platform.ImageFormat) (importer.Archive, string) {
	if format == platform.FormatOVA {
		return &importer.TapeArchive{Path: artifactPath}, "*.ovf"
	}
	return &importer.FileArchive{Path: artifactPath}, artifactPath
}

func datastoreDirectory(datastore, dir string) string {
	return datastoreReference(datastore, strings.Trim(dir, "/"))
}

func datastoreReference(datastore, datastorePath string) string {
	return fmt.Sprintf("[%s] %s", datastore, strings.TrimLeft(datastorePath, "/"))
}

func parseDatastoreReference(ref string) (string, string, bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "[") {
		return "", "", false
	}
	end := strings.Index(ref, "]")
	if end <= 1 {
		return "", "", false
	}
	datastore := strings.TrimSpace(ref[1:end])
	datastorePath := strings.TrimSpace(ref[end+1:])
	if datastore == "" || datastorePath == "" {
		return "", "", false
	}
	return datastore, datastorePath, true
}

func firstSecretValue(secret map[string][]byte, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(string(secret[key])); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func boolFromExtra(extra map[string]string, key string, defaultValue bool) bool {
	value := strings.TrimSpace(extra[key])
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

func cloneStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func sanitizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	result := strings.Trim(b.String(), "-_.")
	if result == "" {
		return "image"
	}
	return result
}
