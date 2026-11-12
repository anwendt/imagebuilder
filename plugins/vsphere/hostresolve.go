package vsphere

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
)

// vCenter hands out NFC upload URLs that name the ESXi host by its inventory
// name. Build pods are often unable to resolve those names (they exist in the
// vCenter inventory, not in the cluster DNS). inventoryDialer falls back to the
// management address vCenter reports for the host.
type contextDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

type inventoryDialer struct {
	base      contextDialer
	dnsLookup func(ctx context.Context, host string) ([]string, error)
	inventory func(ctx context.Context) (map[string]string, error)

	once  sync.Once
	cache map[string]string
}

func (d *inventoryDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.base.DialContext(ctx, network, d.rewrite(ctx, addr))
}

// rewrite returns the address to dial: the inventory management IP for a host
// name the DNS cannot resolve, the unchanged address otherwise.
func (d *inventoryDialer) rewrite(ctx context.Context, addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil {
		return addr
	}
	if _, lookupErr := d.dnsLookup(ctx, host); lookupErr == nil {
		return addr
	}
	d.once.Do(func() {
		if m, invErr := d.inventory(ctx); invErr == nil {
			d.cache = m
		}
	})
	if ip, ok := d.cache[strings.ToLower(host)]; ok {
		return net.JoinHostPort(ip, port)
	}
	return addr
}

// dialTLS dials a TLS connection that keeps verifying the certificate against
// the original host name even when the address was rewritten to an IP.
func (d *inventoryDialer) dialTLS(base func(ctx context.Context, network, addr string) (net.Conn, error), tlsConfig *tls.Config) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		rewritten := d.rewrite(ctx, addr)
		if rewritten == addr {
			return base(ctx, network, addr)
		}
		host, _, _ := net.SplitHostPort(addr)
		cfg := tlsConfig.Clone()
		cfg.ServerName = host
		return (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 30 * time.Second}, Config: cfg}).DialContext(ctx, network, rewritten)
	}
}

// esxManagementAddresses lists ESXi host inventory names with their first
// management (vmk) address.
func esxManagementAddresses(ctx context.Context, c *vim25.Client) (map[string]string, error) {
	m := view.NewManager(c)
	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"HostSystem"}, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = v.Destroy(ctx) }()
	var hosts []mo.HostSystem
	if err := v.Retrieve(ctx, []string{"HostSystem"}, []string{"name", "config.network.vnic"}, &hosts); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, h := range hosts {
		if h.Config == nil {
			continue
		}
		for _, nic := range h.Config.Network.Vnic {
			if nic.Spec.Ip != nil && nic.Spec.Ip.IpAddress != "" {
				out[strings.ToLower(h.Name)] = nic.Spec.Ip.IpAddress
				break
			}
		}
	}
	return out, nil
}
