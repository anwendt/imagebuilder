package openstack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	gcopenstack "github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imagedata"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"

	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

type gophercloudClient struct {
	cfg     openStackConfig
	image   *gophercloud.ServiceClient
	compute *gophercloud.ServiceClient
}

func newGophercloudClient(ctx context.Context, cfg openStackConfig) (*gophercloudClient, error) {
	auth := gophercloud.AuthOptions{
		IdentityEndpoint:            cfg.authURL,
		Username:                    cfg.username,
		UserID:                      cfg.userID,
		Password:                    cfg.password,
		TokenID:                     cfg.tokenID,
		TenantID:                    cfg.projectID,
		TenantName:                  cfg.projectName,
		DomainID:                    cfg.domainID,
		DomainName:                  cfg.domainName,
		ApplicationCredentialID:     cfg.appCredID,
		ApplicationCredentialName:   cfg.appCredName,
		ApplicationCredentialSecret: cfg.appCredSecret,
		AllowReauth:                 true,
	}
	provider, err := gcopenstack.NewClient(auth.IdentityEndpoint)
	if err != nil {
		return nil, fmt.Errorf("create provider client: %w", err)
	}
	httpClient, err := platform.HTTPClient(cfg.extraConfig)
	if err != nil {
		return nil, fmt.Errorf("configure provider proxy: %w", err)
	}
	httpClient.Timeout = 60 * time.Second
	provider.HTTPClient = *httpClient
	if err := gcopenstack.Authenticate(ctx, provider, auth); err != nil {
		return nil, fmt.Errorf("authenticate: %w", err)
	}
	endpoint := gophercloud.EndpointOpts{Region: cfg.region}
	imageClient, err := gcopenstack.NewImageV2(provider, endpoint)
	if err != nil {
		return nil, fmt.Errorf("create image client: %w", err)
	}
	computeClient, err := gcopenstack.NewComputeV2(provider, endpoint)
	if err != nil {
		return nil, fmt.Errorf("create compute client: %w", err)
	}
	return &gophercloudClient{cfg: cfg, image: imageClient, compute: computeClient}, nil
}

func (c *gophercloudClient) UploadImage(ctx context.Context, input openStackUploadInput, body io.Reader) (*platform.ImageRef, error) {
	visibility := images.ImageVisibility(input.Visibility)
	protected := input.Protected
	image, err := images.Create(ctx, c.image, images.CreateOpts{
		Name:            input.ImageName,
		Visibility:      &visibility,
		Tags:            openStackTagList(input.Tags),
		ContainerFormat: input.ContainerFormat,
		DiskFormat:      input.DiskFormat,
		MinDisk:         input.MinDiskGB,
		MinRAM:          input.MinRAMMB,
		Protected:       &protected,
		Properties:      input.Properties,
	}).Extract()
	if err != nil {
		return nil, fmt.Errorf("create queued Glance image: %w", err)
	}
	if err := imagedata.Upload(ctx, c.image, image.ID, body).ExtractErr(); err != nil {
		_ = images.Delete(ctx, c.image, image.ID).ExtractErr()
		return nil, fmt.Errorf("upload image data: %w", err)
	}
	ref, err := c.waitForImage(ctx, image.ID)
	if err != nil {
		_ = images.Delete(ctx, c.image, image.ID).ExtractErr()
		return nil, err
	}
	return ref, nil
}

func (c *gophercloudClient) GetImage(ctx context.Context, id string) (*platform.ImageRef, error) {
	return c.waitForImage(ctx, id)
}

func (c *gophercloudClient) DeleteImage(ctx context.Context, id string) error {
	err := images.Delete(ctx, c.image, id).ExtractErr()
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *gophercloudClient) HealthCheck(ctx context.Context) error {
	_, err := images.Get(ctx, c.image, "00000000-0000-0000-0000-000000000000").Extract()
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *gophercloudClient) waitForImage(ctx context.Context, id string) (*platform.ImageRef, error) {
	var image *images.Image
	err := gophercloud.WaitFor(ctx, func(ctx context.Context) (bool, error) {
		current, err := images.Get(ctx, c.image, id).Extract()
		if err != nil {
			return false, err
		}
		image = current
		switch current.Status {
		case images.ImageStatusActive:
			return true, nil
		case images.ImageStatusKilled, images.ImageStatusDeleted, images.ImageStatusDeactivated:
			return false, fmt.Errorf("glance image %q entered terminal status %q", id, current.Status)
		default:
			return false, nil
		}
	})
	if err != nil {
		return nil, err
	}
	return imageRefFromGlance(c.cfg, image), nil
}

func (c *gophercloudClient) createServer(ctx context.Context, input openStackServerCreateInput) (*openStackServer, error) {
	opts := openStackServerCreateOpts{
		CreateOpts: servers.CreateOpts{
			Name:           input.Name,
			ImageRef:       input.ImageRef,
			FlavorRef:      input.FlavorRef,
			SecurityGroups: input.SecurityGroups,
			UserData:       []byte(input.UserData),
			Networks:       input.Networks,
			Metadata:       input.Metadata,
			ConfigDrive:    input.ConfigDrive,
		},
		KeyName: input.KeyName,
	}
	server, err := servers.Create(ctx, c.compute, opts, nil).Extract()
	if err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}
	return openStackServerFromNova(server), nil
}

func (c *gophercloudClient) getServer(ctx context.Context, id string) (*openStackServer, error) {
	server, err := servers.Get(ctx, c.compute, id).Extract()
	if err != nil {
		return nil, err
	}
	return openStackServerFromNova(server), nil
}

func (c *gophercloudClient) waitServerStatus(ctx context.Context, id, status string) error {
	return servers.WaitForStatus(ctx, c.compute, id, status)
}

func (c *gophercloudClient) stopServer(ctx context.Context, id string) error {
	if err := servers.Stop(ctx, c.compute, id).ExtractErr(); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func (c *gophercloudClient) deleteServer(ctx context.Context, id string) error {
	if err := servers.Delete(ctx, c.compute, id).ExtractErr(); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func (c *gophercloudClient) createServerImage(ctx context.Context, serverID, imageName string, metadata map[string]string) (string, error) {
	return servers.CreateImage(ctx, c.compute, serverID, servers.CreateImageOpts{
		Name:     imageName,
		Metadata: metadata,
	}).ExtractImageID()
}

type openStackServerCreateOpts struct {
	servers.CreateOpts
	KeyName string
}

func (opts openStackServerCreateOpts) ToServerCreateMap() (map[string]any, error) {
	body, err := opts.CreateOpts.ToServerCreateMap()
	if err != nil {
		return nil, err
	}
	if opts.KeyName != "" {
		server, ok := body["server"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unexpected server create body")
		}
		server["key_name"] = opts.KeyName
	}
	return body, nil
}

func imageRefFromGlance(cfg openStackConfig, image *images.Image) *platform.ImageRef {
	if image == nil {
		return nil
	}
	return &platform.ImageRef{
		ID:       image.ID,
		Name:     image.Name,
		Location: cfg.region,
		Tags:     openStackTagsFromList(image.Tags),
	}
}

func openStackTagList(tags map[string]string) []string {
	out := make([]string, 0, len(tags))
	for key, value := range tags {
		if value == "" {
			out = append(out, sanitizeOpenStackTag(key))
			continue
		}
		out = append(out, sanitizeOpenStackTag(key+"="+value))
	}
	return out
}

func openStackTagsFromList(tags []string) map[string]string {
	out := map[string]string{}
	for _, tag := range tags {
		key, value, ok := strings.Cut(tag, "=")
		if ok {
			out[key] = value
		} else {
			out[tag] = "true"
		}
	}
	return out
}

func sanitizeOpenStackTag(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "/", "_")
	value = strings.ReplaceAll(value, " ", "_")
	if len(value) > 255 {
		return value[:255]
	}
	return value
}

func isNotFound(err error) bool {
	return gophercloud.ResponseCodeIs(err, http.StatusNotFound)
}
