// Package xo builds a Talos VM template in Xen Orchestra from a disk image.
package xo

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gofrs/uuid"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"

	"github.com/littlejo/xo-talos/internal/progress"
)

// Builder creates, verifies and deletes templates.
type Builder struct {
	api  API
	logf func(format string, args ...any)

	// Progress reports the upload progress; nil reports nothing.
	Progress *progress.Reporter

	// poll is the interval between status checks; dialTalos checks the Talos
	// API. Tests replace both.
	poll      time.Duration
	dialTalos func(ctx context.Context, ip string) error
}

// NewBuilder returns a Builder. logf receives progress messages.
func NewBuilder(api API, logf func(string, ...any)) *Builder {
	return &Builder{api: api, logf: logf, poll: 5 * time.Second, dialTalos: dialTalosAPI}
}

// Disk is a disk image to upload.
type Disk interface {
	// VirtualSize is the size of the disk as the VM sees it.
	VirtualSize() int64
	// Format is the upload format.
	Format() payloads.VDIFormat
	// Open returns the upload stream and its exact length. It is called once
	// per upload.
	Open() (io.ReadCloser, int64, error)
}

// Spec describes the template to build.
type Spec struct {
	Name        string
	Description string
	Tags        []string
	PoolID      uuid.UUID
	SRID        uuid.UUID
	// NetworkID adds a VIF on this network when set. Leave it unset for CAPV,
	// which adds its own VIFs starting at device 0.
	NetworkID    uuid.UUID
	BaseTemplate string // name of a diskless template in the pool
	Firmware     string // "bios" or "uefi"
	CPUs         int
	MemoryBytes  int
	Disk         Disk
}

// PoolOfSR returns the pool an SR belongs to.
func (b *Builder) PoolOfSR(ctx context.Context, srID uuid.UUID) (uuid.UUID, error) {
	pool, err := b.api.SRPool(ctx, srID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("get SR %s: %w", srID, err)
	}
	return pool, nil
}

// PoolOfNetwork returns the pool a network belongs to.
func (b *Builder) PoolOfNetwork(ctx context.Context, networkID uuid.UUID) (uuid.UUID, error) {
	pool, err := b.api.NetworkPool(ctx, networkID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("get network %s: %w", networkID, err)
	}
	return pool, nil
}

// FindTemplate returns the template named name in the pool, or nil.
func (b *Builder) FindTemplate(ctx context.Context, poolID uuid.UUID, name string) (*payloads.Template, error) {
	templates, err := b.api.Templates(ctx, fmt.Sprintf("name_label:%q $pool:%s", name, poolID))
	if err != nil {
		return nil, fmt.Errorf("look up template %q: %w", name, err)
	}
	for _, t := range templates {
		if t.NameLabel == name && t.Pool == poolID {
			return t, nil
		}
	}
	return nil, nil
}

// DeleteTemplate deletes a template and its disks.
func (b *Builder) DeleteTemplate(ctx context.Context, id uuid.UUID) error {
	if err := b.api.DeleteTemplate(ctx, id); err != nil {
		return fmt.Errorf("delete template %s: %w", id, err)
	}
	return nil
}

// Build creates the template and returns its UUID. Everything it created is
// deleted again if a step fails.
func (b *Builder) Build(ctx context.Context, spec Spec) (id uuid.UUID, err error) {
	base, err := b.FindTemplate(ctx, spec.PoolID, spec.BaseTemplate)
	if err != nil {
		return uuid.Nil, err
	}
	if base == nil {
		return uuid.Nil, fmt.Errorf("base template %q not found in pool %s", spec.BaseTemplate, spec.PoolID)
	}
	if len(base.VBDs) > 0 {
		return uuid.Nil, fmt.Errorf("base template %q has disks; it must be diskless", spec.BaseTemplate)
	}

	var vdiID, vmID uuid.UUID
	converted := false
	defer func() {
		if err == nil {
			return
		}
		if converted {
			b.logf("cleanup: deleting template %s", vmID)
			if delErr := b.api.DeleteTemplate(context.Background(), vmID); delErr != nil {
				b.logf("cleanup: delete template %s: %v", vmID, delErr)
			}
			return
		}
		b.cleanup(vmID, vdiID)
	}()

	size := spec.Disk.VirtualSize()
	b.logf("creating VDI %q (%s) on SR %s", spec.Name, progress.Bytes(size), spec.SRID)
	vdiID, err = b.api.CreateVDI(ctx, payloads.VDICreateParams{
		SRId:            spec.SRID,
		VirtualSize:     size,
		NameLabel:       spec.Name,
		NameDescription: spec.Description,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create VDI: %w", err)
	}

	if err = b.upload(ctx, vdiID, spec.Disk); err != nil {
		return uuid.Nil, err
	}

	b.logf("creating VM %q from %q", spec.Name, spec.BaseTemplate)
	params := payloads.CreateVMParams{
		NameLabel:       spec.Name,
		NameDescription: spec.Description,
		Template:        base.UUID,
		Boot:            ptr(false),
		AutoPoweron:     ptr(false),
		Memory:          &spec.MemoryBytes,
	}
	if spec.NetworkID != uuid.Nil {
		params.VIFs = []payloads.VIFParams{{Network: &spec.NetworkID, Device: ptr(payloads.StringifiedInt(0))}}
	}
	if vmID, err = b.api.CreateVM(ctx, spec.PoolID, &params); err != nil {
		return uuid.Nil, fmt.Errorf("create VM: %w", err)
	}

	b.logf("attaching VDI %s to VM %s", vdiID, vmID)
	if err = b.api.CreateVBD(ctx, &payloads.CreateVBDParams{
		VM:         vmID,
		VDI:        vdiID,
		Type:       payloads.VBDTypeDisk,
		Mode:       payloads.VBDModeRW,
		Bootable:   ptr(true),
		Userdevice: ptr(payloads.StringifiedInt(0)),
	}); err != nil {
		return uuid.Nil, fmt.Errorf("attach VDI: %w", err)
	}

	// viridian must be false: with it, XO prepends an MBR to the cloud-init
	// config drive and Talos cannot mount it.
	b.logf("setting viridian=false, firmware=%s, cpus=%d, tags on VM %s", spec.Firmware, spec.CPUs, vmID)
	patch := VMPatch{Viridian: ptr(false), HVMBootFirmware: spec.Firmware, CPUs: spec.CPUs, Tags: spec.Tags}
	if err = b.api.PatchVM(ctx, vmID, patch); err != nil {
		return uuid.Nil, fmt.Errorf("update VM %s: %w", vmID, err)
	}

	// The base template boots from CD first ("cd"); a Talos template has no
	// CD drive and boots from its disk only, like CAPV expects.
	if err = b.api.SetBootOrder(ctx, vmID, "c"); err != nil {
		return uuid.Nil, fmt.Errorf("set boot order of VM %s: %w", vmID, err)
	}

	b.logf("converting VM %s to a template", vmID)
	if err = b.api.ConvertToTemplate(ctx, vmID); err != nil {
		return uuid.Nil, fmt.Errorf("convert VM %s to a template: %w", vmID, err)
	}
	converted = true

	// XO updates its object cache asynchronously: right after the conversion
	// the template is not visible yet, and cloning or deleting it fails with
	// "no such VM-template".
	if err = b.waitForTemplate(ctx, spec.PoolID, spec.Name, vmID); err != nil {
		return uuid.Nil, err
	}
	return vmID, nil
}

func (b *Builder) waitForTemplate(ctx context.Context, poolID uuid.UUID, name string, id uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		t, err := b.FindTemplate(ctx, poolID, name)
		if err == nil && t != nil && t.UUID == id {
			return nil
		}
		if sleepErr := b.sleep(ctx); sleepErr != nil {
			return fmt.Errorf("template %s did not show up in Xen Orchestra: %w", id, sleepErr)
		}
	}
}

func (b *Builder) upload(ctx context.Context, vdiID uuid.UUID, disk Disk) error {
	r, length, err := disk.Open()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	b.logf("uploading %s image (%s) into VDI %s", disk.Format(), progress.Bytes(length), vdiID)
	pr := b.Progress.Wrap(r, length, "upload")
	if err := b.api.ImportVDI(ctx, vdiID, disk.Format(), pr, length); err != nil {
		return fmt.Errorf("upload disk into VDI %s: %w", vdiID, err)
	}
	pr.Finish()
	return nil
}

// cleanup deletes the VM and VDI this run created. It uses a fresh context so
// it still runs after Ctrl+C.
func (b *Builder) cleanup(vmID, vdiID uuid.UUID) {
	if vmID != uuid.Nil {
		b.destroyVM(vmID) // deleting the VM also deletes its attached VDI
	}
	if vdiID == uuid.Nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if exists, err := b.api.VDIExists(ctx, vdiID); err != nil || !exists {
		return
	}
	b.logf("cleanup: deleting VDI %s", vdiID)
	if err := b.api.DeleteVDI(ctx, vdiID); err != nil {
		b.logf("cleanup: delete VDI %s: %v", vdiID, err)
	}
}

func ptr[T any](v T) *T { return &v }
