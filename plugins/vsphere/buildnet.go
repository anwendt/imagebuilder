package vsphere

import (
	"fmt"
	"net"
	"strings"
)

// buildNetwork is the optional static IPv4 configuration of the build VM for
// networks without DHCP. It is applied through a volatile netplan file in
// /run, so it never becomes part of the template.
type buildNetwork struct {
	Address string
	Prefix  int
	Gateway string
	DNS     []string
}

// parseBuildNetwork reads buildIPv4 (CIDR), buildGateway and buildDNS. No
// buildIPv4 means DHCP and returns nil.
func parseBuildNetwork(extra map[string]string) (*buildNetwork, error) {
	cidr := strings.TrimSpace(extra["buildIPv4"])
	if cidr == "" {
		return nil, nil
	}
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("vsphere plugin: buildIPv4 %q must be an IPv4 address in CIDR notation", cidr)
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf("vsphere plugin: buildIPv4 %q must be an IPv4 address", cidr)
	}
	gateway := strings.TrimSpace(extra["buildGateway"])
	if gateway == "" {
		return nil, fmt.Errorf("vsphere plugin: buildGateway is required together with buildIPv4")
	}
	gw := net.ParseIP(gateway)
	if gw == nil || gw.To4() == nil {
		return nil, fmt.Errorf("vsphere plugin: buildGateway %q is not an IPv4 address", gateway)
	}
	if !ipNet.Contains(gw) {
		return nil, fmt.Errorf("vsphere plugin: buildGateway %q is outside the subnet %s", gateway, ipNet)
	}
	prefix, _ := ipNet.Mask.Size()
	out := &buildNetwork{Address: ip.String(), Prefix: prefix, Gateway: gw.String()}
	for _, raw := range strings.Split(extra["buildDNS"], ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if p := net.ParseIP(raw); p == nil || p.To4() == nil {
			return nil, fmt.Errorf("vsphere plugin: buildDNS entry %q is not an IPv4 address", raw)
		}
		out.DNS = append(out.DNS, raw)
	}
	return out, nil
}

// guestScript configures the build VM. It first probes the address with an ARP
// request (RFC 5227 style, sender 0.0.0.0) and refuses to use an address that
// answers, then writes a volatile netplan file and applies it.
func (n *buildNetwork) guestScript() string {
	dns := ""
	if len(n.DNS) > 0 {
		dns = "      nameservers:\n        addresses: [" + strings.Join(n.DNS, ", ") + "]\n"
	}
	return fmt.Sprintf(`set -eu
IFACE=$(ls /sys/class/net | grep -v '^lo$' | head -n 1)
cat > /tmp/arp_probe.py <<'PYEOF'
import socket, struct, sys, time
iface, target = sys.argv[1], sys.argv[2]
s = socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(0x0806))
s.bind((iface, 0))
s.settimeout(0.3)
mac = bytes.fromhex(open('/sys/class/net/%%s/address' %% iface).read().strip().replace(':', ''))
tip = socket.inet_aton(target)
frame = b'\xff' * 6 + mac + b'\x08\x06' + struct.pack('!HHBBH', 1, 0x0800, 6, 4, 1) + mac + b'\0\0\0\0' + b'\0' * 6 + tip
for _ in range(4):
    s.send(frame)
    end = time.time() + 1.0
    while time.time() < end:
        try:
            pkt = s.recv(2048)
        except socket.timeout:
            continue
        if len(pkt) >= 42 and pkt[12:14] == b'\x08\x06' and struct.unpack('!H', pkt[20:22])[0] == 2 and pkt[28:32] == tip:
            print('IMAGEBUILDER_ADDRESS_IN_USE %%s answered from %%s' %% (target, ':'.join('%%02x' %% b for b in pkt[22:28])))
            sys.exit(3)
print('address %%s is not answered on the segment' %% target)
PYEOF
arp_probe() { python3 /tmp/arp_probe.py "$IFACE" "%[1]s"; }
arp_probe
rm -f /tmp/arp_probe.py
install -d -m 0755 /run/netplan
cat > /run/netplan/90-imagebuilder-build.yaml <<NETEOF
network:
  version: 2
  ethernets:
    $IFACE:
      dhcp4: false
      dhcp6: false
      addresses: [%[1]s/%[2]d]
      routes:
        - to: default
          via: %[3]s
%[4]sNETEOF
chmod 600 /run/netplan/90-imagebuilder-build.yaml
netplan apply
i=0
while [ "$i" -lt 30 ]; do
  ip -4 -br addr show dev "$IFACE" | grep -q '%[1]s' && break
  i=$((i+1)); sleep 1
done
ip -4 -br addr show dev "$IFACE"
`, n.Address, n.Prefix, n.Gateway, dns)
}
