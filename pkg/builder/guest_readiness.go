package builder

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

const (
	guestProtocolSSH   = "ssh"
	guestProtocolWinRM = "winrm"

	defaultGuestHost     = "127.0.0.1"
	defaultGuestTimeout  = 10 * time.Minute
	defaultSSHGuestPort  = int32(22)
	defaultWinRMPortHTTP = int32(5985)
	defaultWinRMPortTLS  = int32(5986)
)

type GuestReadinessProbe interface {
	Wait(ctx context.Context, access GuestAccess) error
}

type GuestAccess struct {
	Protocol           string
	Host               string
	HostPort           int32
	User               string
	SSHKeyPath         string
	PasswordPath       string
	GuestPort          int32
	Timeout            time.Duration
	WinRMHTTPS         bool
	InsecureSkipVerify bool
}

type NetworkGuestReadinessProbe struct {
	Dialer       *net.Dialer
	DialContext  func(ctx context.Context, network, address string) (net.Conn, error)
	PollInterval time.Duration
	HTTPClient   *http.Client
}

func GuestAccessFromSpec(spec *v1alpha1.GuestAccessSpec) (GuestAccess, bool, error) {
	if spec == nil {
		return GuestAccess{}, false, nil
	}
	access := GuestAccess{
		Protocol:     strings.ToLower(spec.Protocol),
		Host:         spec.Host,
		HostPort:     spec.HostPort,
		User:         spec.User,
		SSHKeyPath:   spec.SSHKeyPath,
		PasswordPath: spec.PasswordPath,
		Timeout:      defaultGuestTimeout,
	}
	if access.Host == "" {
		access.Host = defaultGuestHost
	}
	if spec.Timeout != nil {
		access.Timeout = spec.Timeout.Duration
	}
	if access.Timeout <= 0 {
		return GuestAccess{}, false, fmt.Errorf("guest access timeout must be greater than zero")
	}
	if access.HostPort <= 0 || access.HostPort > 65535 {
		return GuestAccess{}, false, fmt.Errorf("guest access hostPort must be between 1 and 65535")
	}
	if access.Protocol != guestProtocolSSH && access.Protocol != guestProtocolWinRM {
		return GuestAccess{}, false, fmt.Errorf("guest access protocol must be ssh or winrm")
	}

	switch access.Protocol {
	case guestProtocolSSH:
		access.GuestPort = defaultSSHGuestPort
	case guestProtocolWinRM:
		access.WinRMHTTPS = true
		if spec.WinRM != nil {
			if spec.WinRM.HTTPS != nil {
				access.WinRMHTTPS = *spec.WinRM.HTTPS
			}
			access.InsecureSkipVerify = spec.WinRM.InsecureSkipVerify
		}
		if access.WinRMHTTPS {
			access.GuestPort = defaultWinRMPortTLS
		} else {
			access.GuestPort = defaultWinRMPortHTTP
		}
	}
	if spec.GuestPort != 0 {
		if spec.GuestPort < 0 || spec.GuestPort > 65535 {
			return GuestAccess{}, false, fmt.Errorf("guest access guestPort must be between 1 and 65535")
		}
		access.GuestPort = spec.GuestPort
	}
	return access, true, nil
}

func (p NetworkGuestReadinessProbe) Wait(ctx context.Context, access GuestAccess) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, access.Timeout)
	defer cancel()

	interval := p.PollInterval
	if interval == 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error
	for {
		if err := p.probe(timeoutCtx, access); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("wait for %s readiness at %s:%d: %w: last error: %v",
				access.Protocol, access.Host, access.HostPort, timeoutCtx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func (p NetworkGuestReadinessProbe) probe(ctx context.Context, access GuestAccess) error {
	switch access.Protocol {
	case guestProtocolSSH:
		return p.probeSSH(ctx, access)
	case guestProtocolWinRM:
		return p.probeWinRM(ctx, access)
	default:
		return fmt.Errorf("unsupported guest access protocol %q", access.Protocol)
	}
}

func (p NetworkGuestReadinessProbe) probeSSH(ctx context.Context, access GuestAccess) error {
	dialContext := p.DialContext
	if dialContext == nil {
		dialer := p.Dialer
		if dialer == nil {
			dialer = &net.Dialer{Timeout: 5 * time.Second}
		}
		dialContext = dialer.DialContext
	}
	conn, err := dialContext(ctx, "tcp", net.JoinHostPort(access.Host, fmt.Sprintf("%d", access.HostPort)))
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	if strings.HasPrefix(line, "SSH-2.0-") || strings.HasPrefix(line, "SSH-1.99-") {
		return nil
	}
	return fmt.Errorf("unexpected ssh banner %q", strings.TrimSpace(line))
}

func (p NetworkGuestReadinessProbe) probeWinRM(ctx context.Context, access GuestAccess) error {
	client := p.HTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if access.WinRMHTTPS {
			transport.TLSClientConfig = &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: access.InsecureSkipVerify, //nolint:gosec // Explicit escape hatch for ephemeral self-signed WinRM.
			}
		}
		client = &http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
		}
	}
	scheme := "http"
	if access.WinRMHTTPS {
		scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s://%s/wsman", scheme, net.JoinHostPort(access.Host, fmt.Sprintf("%d", access.HostPort))), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusMethodNotAllowed ||
		(resp.StatusCode >= 200 && resp.StatusCode < 500) {
		return nil
	}
	return fmt.Errorf("unexpected winrm status %d", resp.StatusCode)
}
