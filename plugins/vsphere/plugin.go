package vsphere

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
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
	datacenter         string
	datastore          string
	folder             string
	uploadPathPrefix   string
	extraConfig        map[string]string
}

type client interface {
	UploadArtifact(ctx context.Context, input uploadInput) (*platform.UploadResult, error)
	RegisterImage(ctx context.Context, input registerInput) (*platform.ImageRef, error)
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
	ProviderRef string
	ImageName   string
	Datacenter  string
	Datastore   string
	Format      platform.ImageFormat
	Checksum    string
	Tags        map[string]string
	Metadata    map[string]string
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
	return []string{v1alpha1.BuildModeLocal}
}

func (p *Plugin) Init(ctx context.Context, cfg platform.PluginConfig) error {
	p.log = slog.Default().With(slog.String("plugin", p.Name()))
	p.config = config{
		providerConfigName: cfg.ProviderConfigName,
		endpoint:           strings.TrimSpace(cfg.Endpoint),
		insecure:           cfg.Insecure,
		username:           firstSecretValue(cfg.SecretData, "username", "user"),
		password:           firstSecretValue(cfg.SecretData, "password", "pass"),
		datacenter:         strings.TrimSpace(cfg.Extra["datacenter"]),
		datastore:          strings.TrimSpace(cfg.Extra["datastore"]),
		folder:             strings.TrimSpace(cfg.Extra["folder"]),
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
		ProviderRef: result.ProviderRef,
		ImageName:   firstNonEmpty(result.Metadata["imageName"], p.config.extraConfig["imageName"]),
		Datacenter:  firstNonEmpty(result.Metadata["datacenter"], p.config.datacenter),
		Datastore:   firstNonEmpty(result.Metadata["datastore"], p.config.datastore),
		Format:      platform.ImageFormat(result.Metadata["format"]),
		Checksum:    result.Metadata["checksum"],
		Metadata:    cloneStringMap(result.Metadata),
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

func (p *Plugin) ReconcileRemoteBuild(_ context.Context, _ *platform.RemoteBuildRequest) (*platform.RemoteBuildResult, error) {
	return nil, fmt.Errorf("vsphere plugin: remote build is not implemented; use build.mode=local")
}

func (p *Plugin) CleanupRemoteBuild(_ context.Context, _ *platform.RemoteBuildRequest) error {
	return nil
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
			"format":        string(input.Format),
			"checksum":      input.Checksum,
			"buildID":       input.BuildID,
			"imageName":     input.ImageName,
		},
	}, nil
}

func (c *govmomiClient) RegisterImage(_ context.Context, input registerInput) (*platform.ImageRef, error) {
	if input.ProviderRef == "" {
		return nil, fmt.Errorf("provider reference is required")
	}
	name := firstNonEmpty(input.ImageName, path.Base(input.ProviderRef))
	tags := cloneStringMap(input.Tags)
	tags["imagebuilder.io/provider"] = "vsphere"
	tags["imagebuilder.io/format"] = string(input.Format)
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
