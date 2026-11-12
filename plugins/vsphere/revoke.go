package vsphere

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vmware/govmomi/guest"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// errBuildCredentialNotRevoked marks a build whose temporary guest login still
// works after the provider locked it. The build fails closed (ADR-009).
var errBuildCredentialNotRevoked = errors.New("BuildCredentialNotRevoked: the temporary build login still works after it was locked")

const (
	revokeDelay          = 30 * time.Second
	revokeSettleMargin   = 20 * time.Second
	revokeCheckName      = "vsphere-build-credential-revoked"
	guestAuthFaultMarker = "Failed to authenticate with the guest operating system"
)

var safeUserName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// guestCredentialClient is the part of Guest Operations the revocation needs.
// Probe returns nil when the build login works, an auth error when it is
// rejected, and any other error when the outcome is unknown.
type guestCredentialClient interface {
	Probe(ctx context.Context) error
	Schedule(ctx context.Context) error
}

func isGuestAuthError(err error) bool {
	if err == nil {
		return false
	}
	if soap.IsVimFault(err) {
		if _, ok := soap.ToVimFault(err).(*types.InvalidGuestLogin); ok {
			return true
		}
	}
	return strings.Contains(err.Error(), guestAuthFaultMarker)
}

// revokeBuildCredential locks the temporary build login and proves that the
// lock took effect. It returns true when the login is verifiably revoked. It
// is safe to call again after an interrupted attempt: a login that is already
// rejected counts as revoked without scheduling anything.
func revokeBuildCredential(ctx context.Context, g guestCredentialClient, wait func(context.Context, time.Duration) error, delay time.Duration) (bool, error) {
	err := g.Probe(ctx)
	switch {
	case isGuestAuthError(err):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("revoke build credential: inconclusive login probe: %w", err)
	}
	if err := g.Schedule(ctx); err != nil {
		return false, fmt.Errorf("revoke build credential: schedule the lock: %w", err)
	}
	if err := wait(ctx, delay+revokeSettleMargin); err != nil {
		return false, err
	}
	err = g.Probe(ctx)
	switch {
	case isGuestAuthError(err):
		return true, nil
	case err == nil:
		return false, errBuildCredentialNotRevoked
	default:
		return false, fmt.Errorf("revoke build credential: inconclusive login probe after the lock: %w", err)
	}
}

// revokeScript schedules a delayed `passwd -l` so that the lock takes effect
// after the process that schedules it has ended.
func revokeScript(user string, delay time.Duration) (string, error) {
	if !safeUserName.MatchString(user) {
		return "", fmt.Errorf("unsafe guest user name %q", user)
	}
	return fmt.Sprintf("systemd-run --unit=imagebuilder-revoke-login --collect --on-active=%ds /usr/bin/passwd -l %s",
		int(delay.Seconds()), user), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// vsphereGuestCredential implements guestCredentialClient over Guest Operations.
type vsphereGuestCredential struct {
	pm   *guest.ProcessManager
	auth *types.NamePasswordAuthentication
}

func (g *vsphereGuestCredential) Probe(ctx context.Context) error {
	_, err := g.pm.StartProgram(ctx, g.auth, &types.GuestProgramSpec{ProgramPath: "/bin/true"})
	return err
}

func (g *vsphereGuestCredential) Schedule(ctx context.Context) error {
	script, err := revokeScript(g.auth.Username, revokeDelay)
	if err != nil {
		return err
	}
	pid, err := g.pm.StartProgram(ctx, g.auth, &types.GuestProgramSpec{ProgramPath: "/bin/sh", Arguments: "-c " + shellQuote(script)})
	if err != nil {
		return err
	}
	return waitGuestProcess(ctx, g.pm, g.auth, pid, -1)
}
