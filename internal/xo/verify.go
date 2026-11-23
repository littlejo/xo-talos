package xo

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/gofrs/uuid"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"
)

// TalosAPIPort is the port of the Talos API (apid), served in maintenance
// mode as soon as Talos boots without a machine configuration.
const TalosAPIPort = 50000

// VerifySpec describes a boot test of a template.
type VerifySpec struct {
	TemplateID uuid.UUID
	PoolID     uuid.UUID
	NetworkID  uuid.UUID // network of the test VM's VIF
	Name       string    // name of the test VM
	Timeout    time.Duration
}

// Verify clones the template into a test VM with one VIF, boots it, waits for
// xen-guest-agent to report an IP address and for the Talos API to accept a
// TLS connection, then deletes the test VM. The template is never started.
func (b *Builder) Verify(ctx context.Context, spec VerifySpec) error {
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	b.logf("verify: booting test VM %q from template %s", spec.Name, spec.TemplateID)
	vmID, err := b.api.CreateVM(ctx, spec.PoolID, &payloads.CreateVMParams{
		NameLabel:       spec.Name,
		NameDescription: "Boot test of a Talos template by xo-talos; deleted after the test",
		Template:        spec.TemplateID,
		Clone:           ptr(true),
		Boot:            ptr(true),
		AutoPoweron:     ptr(false),
		VIFs:            []payloads.VIFParams{{Network: &spec.NetworkID, Device: ptr(payloads.StringifiedInt(0))}},
	})
	if err != nil {
		return fmt.Errorf("verify: create test VM: %w", err)
	}
	defer b.destroyVM(vmID)

	ip, err := b.waitForIP(ctx, vmID)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	b.logf("verify: xen-guest-agent reports %s", ip)

	ip, err = b.waitForTalosAPI(ctx, vmID, ip)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	b.logf("verify: Talos API answers on %s", net.JoinHostPort(ip, strconv.Itoa(TalosAPIPort)))
	return nil
}

// waitForIP waits for xen-guest-agent to report an address the tool can reach.
// The agent often reports the IPv6 link-local address first, before DHCP gives
// the VM its address: a link-local address is only reachable from the same
// link, with a zone, so it is skipped.
func (b *Builder) waitForIP(ctx context.Context, vmID uuid.UUID) (string, error) {
	skipped := ""
	for {
		vm, err := b.api.GetVM(ctx, vmID)
		if err == nil && vm.MainIpAddress != "" {
			if reachable(vm.MainIpAddress) {
				return vm.MainIpAddress, nil
			}
			if vm.MainIpAddress != skipped {
				skipped = vm.MainIpAddress
				b.logf("verify: xen-guest-agent reports %s, not reachable from here: waiting for another address", skipped)
			}
		}
		if err := b.sleep(ctx); err != nil {
			if skipped != "" {
				return "", fmt.Errorf("no reachable IP address reported by xen-guest-agent for VM %s (only %s): %w", vmID, skipped, err)
			}
			return "", fmt.Errorf("no IP address reported by xen-guest-agent for VM %s: %w", vmID, err)
		}
	}
}

// reachable reports whether ip can be dialed from another network: not
// link-local, loopback or unspecified.
func reachable(ip string) bool {
	addr := net.ParseIP(ip)
	return addr != nil && !addr.IsLinkLocalUnicast() && !addr.IsLoopback() && !addr.IsUnspecified()
}

// waitForTalosAPI dials the Talos API until it answers. Between attempts it
// reads the address again: the agent can report a new one (another interface,
// a DHCP renewal), which is then dialed instead.
func (b *Builder) waitForTalosAPI(ctx context.Context, vmID uuid.UUID, ip string) (string, error) {
	for {
		err := b.dialTalos(ctx, ip)
		if err == nil {
			return ip, nil
		}
		if ctxErr := b.sleep(ctx); ctxErr != nil {
			return "", fmt.Errorf("the Talos API on %s is not reachable: %w", ip, err)
		}
		if vm, getErr := b.api.GetVM(ctx, vmID); getErr == nil && vm.MainIpAddress != ip && reachable(vm.MainIpAddress) {
			ip = vm.MainIpAddress
			b.logf("verify: xen-guest-agent now reports %s", ip)
		}
	}
}

// dialTalosAPI completes a TLS handshake with the Talos API. In maintenance
// mode apid serves a self-signed certificate, so it is not verified: this
// only proves Talos booted.
func dialTalosAPI(ctx context.Context, ip string) error {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config:    &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // reachability check only
	}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(TalosAPIPort)))
	if err != nil {
		return err
	}
	return conn.Close()
}

// destroyVM stops and deletes a VM this run created, with its disks. It uses
// a fresh context so it still runs after Ctrl+C or a timeout.
func (b *Builder) destroyVM(vmID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	vm, err := b.api.GetVM(ctx, vmID)
	if err == nil && vm.PowerState != payloads.PowerStateHalted {
		if err := b.api.HardShutdownVM(ctx, vmID); err != nil {
			b.logf("cleanup: stop VM %s: %v", vmID, err)
		}
		for err == nil && vm.PowerState != payloads.PowerStateHalted {
			if sleepErr := b.sleep(ctx); sleepErr != nil {
				b.logf("cleanup: VM %s did not stop: %v", vmID, sleepErr)
				return
			}
			vm, err = b.api.GetVM(ctx, vmID)
		}
	}
	b.logf("cleanup: deleting VM %s", vmID)
	if err := b.api.DeleteVM(ctx, vmID); err != nil {
		b.logf("cleanup: delete VM %s: %v", vmID, err)
	}
}

func (b *Builder) sleep(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.poll):
		return nil
	}
}
