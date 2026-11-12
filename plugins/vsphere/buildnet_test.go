package vsphere

import (
	"strings"
	"testing"
)

func TestParseBuildNetwork(t *testing.T) {
	tests := []struct {
		name    string
		extra   map[string]string
		want    *buildNetwork
		wantErr string
	}{
		{"unset means DHCP", map[string]string{}, nil, ""},
		{"complete", map[string]string{"buildIPv4": "192.0.2.9/27", "buildGateway": "192.0.2.1", "buildDNS": "192.0.2.5"},
			&buildNetwork{Address: "192.0.2.9", Prefix: 27, Gateway: "192.0.2.1", DNS: []string{"192.0.2.5"}}, ""},
		{"multiple dns", map[string]string{"buildIPv4": "10.0.0.5/24", "buildGateway": "10.0.0.1", "buildDNS": "10.0.0.2, 10.0.0.3"},
			&buildNetwork{Address: "10.0.0.5", Prefix: 24, Gateway: "10.0.0.1", DNS: []string{"10.0.0.2", "10.0.0.3"}}, ""},
		{"missing gateway", map[string]string{"buildIPv4": "10.0.0.5/24"}, nil, "buildGateway"},
		{"not cidr", map[string]string{"buildIPv4": "10.0.0.5", "buildGateway": "10.0.0.1"}, nil, "CIDR"},
		{"gateway outside subnet", map[string]string{"buildIPv4": "10.0.0.5/24", "buildGateway": "10.9.9.1"}, nil, "subnet"},
		{"bad dns", map[string]string{"buildIPv4": "10.0.0.5/24", "buildGateway": "10.0.0.1", "buildDNS": "nope"}, nil, "buildDNS"},
		{"ipv6 rejected", map[string]string{"buildIPv4": "fd00::5/64", "buildGateway": "fd00::1"}, nil, "IPv4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBuildNetwork(tt.extra)
			assertErrContains(t, err, tt.wantErr)
			if tt.wantErr != "" {
				return
			}
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if got != nil && (got.Address != tt.want.Address || got.Prefix != tt.want.Prefix || got.Gateway != tt.want.Gateway || strings.Join(got.DNS, ",") != strings.Join(tt.want.DNS, ",")) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBuildNetworkScript(t *testing.T) {
	n := &buildNetwork{Address: "192.0.2.9", Prefix: 27, Gateway: "192.0.2.1", DNS: []string{"192.0.2.5"}}
	script := n.guestScript()
	for _, want := range []string{
		"/run/netplan/90-imagebuilder-build.yaml", // volatile: gone after shutdown
		"192.0.2.9/27",
		"via: 192.0.2.1",
		"192.0.2.5",
		"dhcp4: false",
		"netplan apply",
		"IMAGEBUILDER_ADDRESS_IN_USE", // duplicate address detection marker
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "/etc/netplan/90-imagebuilder") {
		t.Fatal("netplan file must live in /run so it does not survive into the template")
	}
	// The conflict check must run before the address is assigned.
	if strings.Index(script, "arp_probe") > strings.Index(script, "netplan apply") {
		t.Fatal("duplicate address check must precede netplan apply")
	}
}
