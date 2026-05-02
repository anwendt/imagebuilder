package builder_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/builder"
)

func TestGuestAccessFromSpec_DefaultsSSH(t *testing.T) {
	access, enabled, err := builder.GuestAccessFromSpec(&v1alpha1.GuestAccessSpec{
		Protocol: "ssh",
		HostPort: 2222,
	})
	if err != nil {
		t.Fatalf("GuestAccessFromSpec returned error: %v", err)
	}
	if !enabled {
		t.Fatal("guest access should be enabled")
	}
	if access.Host != "127.0.0.1" || access.GuestPort != 22 || access.Timeout != 10*time.Minute {
		t.Fatalf("access defaults = %#v", access)
	}
}

func TestGuestAccessFromSpec_DefaultsWinRMHTTPS(t *testing.T) {
	access, enabled, err := builder.GuestAccessFromSpec(&v1alpha1.GuestAccessSpec{
		Protocol: "winrm",
		HostPort: 55986,
		Timeout:  &metav1.Duration{Duration: time.Minute},
	})
	if err != nil {
		t.Fatalf("GuestAccessFromSpec returned error: %v", err)
	}
	if !enabled {
		t.Fatal("guest access should be enabled")
	}
	if !access.WinRMHTTPS || access.GuestPort != 5986 || access.Timeout != time.Minute {
		t.Fatalf("winrm defaults = %#v", access)
	}
}

func TestNetworkGuestReadinessProbe_WaitsForSSHBanner(t *testing.T) {
	probe := builder.NetworkGuestReadinessProbe{
		PollInterval: time.Millisecond,
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return fakeConn{Reader: bytes.NewReader([]byte("SSH-2.0-OpenSSH_9.6\r\n"))}, nil
		},
	}
	if err := probe.Wait(context.Background(), builder.GuestAccess{
		Protocol: "ssh",
		Host:     "127.0.0.1",
		HostPort: 2222,
		Timeout:  time.Second,
	}); err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
}

func TestNetworkGuestReadinessProbe_WaitsForWinRMEndpoint(t *testing.T) {
	probe := builder.NetworkGuestReadinessProbe{
		PollInterval: time.Millisecond,
		HTTPClient: &http.Client{Transport: guestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/wsman" {
				t.Errorf("path = %q, want /wsman", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
				Request:    req,
			}, nil
		})},
	}
	if err := probe.Wait(context.Background(), builder.GuestAccess{
		Protocol:   "winrm",
		Host:       "127.0.0.1",
		HostPort:   55985,
		Timeout:    time.Second,
		WinRMHTTPS: false,
	}); err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
}

type fakeConn struct {
	*bytes.Reader
}

func (c fakeConn) Close() error                     { return nil }
func (c fakeConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (c fakeConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (c fakeConn) SetDeadline(time.Time) error      { return nil }
func (c fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c fakeConn) SetWriteDeadline(time.Time) error { return nil }
func (c fakeConn) Write(data []byte) (int, error)   { return len(data), nil }

type fakeAddr string

func (a fakeAddr) Network() string { return string(a) }
func (a fakeAddr) String() string  { return string(a) }

type guestRoundTripFunc func(*http.Request) (*http.Response, error)

func (f guestRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
