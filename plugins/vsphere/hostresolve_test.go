package vsphere

import (
	"context"
	"errors"
	"net"
	"testing"
)

type fakeBaseDialer struct{ dialed []string }

func (f *fakeBaseDialer) DialContext(_ context.Context, _ string, addr string) (net.Conn, error) {
	f.dialed = append(f.dialed, addr)
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, nil
}

func TestInventoryDialerUsesInventoryAddressWhenDNSFails(t *testing.T) {
	base := &fakeBaseDialer{}
	d := &inventoryDialer{
		base:      base,
		dnsLookup: func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") },
		inventory: func(context.Context) (map[string]string, error) {
			return map[string]string{"esx01.example.com": "10.1.2.3"}, nil
		},
	}
	conn, err := d.DialContext(context.Background(), "tcp", "ESX01.example.com:443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn.Close()
	if len(base.dialed) != 1 || base.dialed[0] != "10.1.2.3:443" {
		t.Fatalf("dialed = %v", base.dialed)
	}
}

func TestInventoryDialerKeepsNameWhenDNSWorks(t *testing.T) {
	base := &fakeBaseDialer{}
	called := false
	d := &inventoryDialer{
		base:      base,
		dnsLookup: func(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil },
		inventory: func(context.Context) (map[string]string, error) { called = true; return nil, nil },
	}
	conn, err := d.DialContext(context.Background(), "tcp", "vcenter.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if called {
		t.Fatal("inventory must not be consulted when DNS works")
	}
	if base.dialed[0] != "vcenter.example.com:443" {
		t.Fatalf("dialed = %v", base.dialed)
	}
}

func TestInventoryDialerKeepsNameWhenUnknownEverywhere(t *testing.T) {
	base := &fakeBaseDialer{}
	d := &inventoryDialer{
		base:      base,
		dnsLookup: func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") },
		inventory: func(context.Context) (map[string]string, error) { return map[string]string{}, nil },
	}
	conn, err := d.DialContext(context.Background(), "tcp", "ghost.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if base.dialed[0] != "ghost.example.com:443" {
		t.Fatalf("dialed = %v", base.dialed)
	}
}

func TestInventoryDialerSkipsIPLiterals(t *testing.T) {
	base := &fakeBaseDialer{}
	d := &inventoryDialer{
		base:      base,
		dnsLookup: func(context.Context, string) ([]string, error) { t.Fatal("no DNS for IP literals"); return nil, nil },
		inventory: func(context.Context) (map[string]string, error) {
			t.Fatal("no inventory for IP literals")
			return nil, nil
		},
	}
	conn, err := d.DialContext(context.Background(), "tcp", "10.0.0.5:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestInventoryDialerRewrite(t *testing.T) {
	d := &inventoryDialer{
		dnsLookup: func(_ context.Context, host string) ([]string, error) {
			if host == "vcenter.example.com" {
				return []string{"192.0.2.1"}, nil
			}
			return nil, errors.New("no such host")
		},
		inventory: func(context.Context) (map[string]string, error) {
			return map[string]string{"esx01.example.com": "10.1.2.3"}, nil
		},
	}
	cases := map[string]string{
		"vcenter.example.com:443": "vcenter.example.com:443",
		"esx01.example.com:443":   "10.1.2.3:443",
		"unknown.example.com:443": "unknown.example.com:443",
		"10.9.9.9:443":            "10.9.9.9:443",
	}
	for in, want := range cases {
		if got := d.rewrite(context.Background(), in); got != want {
			t.Fatalf("rewrite(%q) = %q, want %q", in, got, want)
		}
	}
}
