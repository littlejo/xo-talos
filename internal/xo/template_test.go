package xo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"
)

// memDisk is an in-memory raw disk.
type memDisk []byte

func (d memDisk) VirtualSize() int64         { return int64(len(d)) }
func (d memDisk) Format() payloads.VDIFormat { return payloads.VDIFormatRaw }
func (d memDisk) Open() (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(d)), int64(len(d)), nil
}

func newTestBuilder(f *fakeAPI) *Builder {
	b := NewBuilder(f, func(string, ...any) {})
	b.poll = time.Millisecond
	b.dialTalos = func(context.Context, string) error { return nil }
	return b
}

func testSpec(f *fakeAPI) Spec {
	return Spec{
		Name:         "talos-test",
		Tags:         []string{"talos"},
		PoolID:       f.pool,
		SRID:         uuid.Must(uuid.NewV4()),
		BaseTemplate: "Other install media",
		Firmware:     "bios",
		CPUs:         2,
		MemoryBytes:  2 << 30,
		Disk:         memDisk("talos disk"),
	}
}

// seedUnrelated adds a VM with a disk and a lone VDI that the builder must
// never touch.
func seedUnrelated(f *fakeAPI) (vm, vdi uuid.UUID) {
	vm, disk, vdi := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	f.vms[vm] = &fakeVM{vm: payloads.VM{ID: vm, NameLabel: "someone-else", PowerState: payloads.PowerStateRunning}, disks: []uuid.UUID{disk}}
	f.vdis[disk] = []byte("theirs")
	f.vdis[vdi] = []byte("theirs too")
	return vm, vdi
}

func TestBuild(t *testing.T) {
	f := newFakeAPI()
	id, err := newTestBuilder(f).Build(context.Background(), testSpec(f))
	if err != nil {
		t.Fatal(err)
	}
	v := f.vms[id]
	if v == nil || !v.template {
		t.Fatalf("VM %s is not a template", id)
	}
	if len(v.disks) != 1 || string(f.vdis[v.disks[0]]) != "talos disk" {
		t.Fatalf("template disk not uploaded: %v", v.disks)
	}
	if v.vm.PowerState != payloads.PowerStateHalted {
		t.Fatal("the template VM was started")
	}
	p := f.patches[id]
	if p.Viridian == nil || *p.Viridian || p.HVMBootFirmware != "bios" || p.CPUs != 2 {
		t.Fatalf("unexpected patch %+v", p)
	}
	if f.bootOrder[id] != "c" {
		t.Fatalf("boot order = %q, want c", f.bootOrder[id])
	}
	if slices.Contains(f.calls, "DeleteVM") || slices.Contains(f.calls, "DeleteVDI") {
		t.Fatalf("a successful build deleted something: %v", f.calls)
	}
}

// TestBuildCleanup makes each step after the VDI creation fail and checks that
// exactly what the build created is deleted.
func TestBuildCleanup(t *testing.T) {
	for _, step := range []string{"ImportVDI", "CreateVM", "CreateVBD", "PatchVM", "SetBootOrder", "ConvertToTemplate"} {
		t.Run(step, func(t *testing.T) {
			f := newFakeAPI()
			theirVM, theirVDI := seedUnrelated(f)
			f.failOn[step] = errors.New("boom")

			_, err := newTestBuilder(f).Build(context.Background(), testSpec(f))
			if err == nil || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("Build error = %v, want boom", err)
			}
			if len(f.vms) != 1 || f.vms[theirVM] == nil {
				t.Fatalf("VMs left: %v", f.vms)
			}
			if len(f.vdis) != 2 || f.vdis[theirVDI] == nil {
				t.Fatalf("VDIs left: %v", f.vdis)
			}
		})
	}
}

func TestBuildWaitsForTemplate(t *testing.T) {
	f := newFakeAPI()
	f.templateDelay = 3
	b := newTestBuilder(f)
	id, err := b.Build(context.Background(), testSpec(f))
	if err != nil {
		t.Fatal(err)
	}
	if tpl, _ := b.FindTemplate(context.Background(), f.pool, "talos-test"); tpl == nil || tpl.UUID != id {
		t.Fatal("Build returned before the template was visible")
	}
}

func TestBuildTemplateNeverVisible(t *testing.T) {
	f := newFakeAPI()
	theirVM, _ := seedUnrelated(f)
	f.templateDelay = 1 << 30
	b := newTestBuilder(f)
	b.poll = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := b.Build(ctx, testSpec(f)); err == nil {
		t.Fatal("Build succeeded although the template never showed up")
	}
	if len(f.vms) != 1 || f.vms[theirVM] == nil {
		t.Fatalf("converted template not deleted: %v", f.vms)
	}
}

func TestBuildCreateVDIFails(t *testing.T) {
	f := newFakeAPI()
	f.failOn["CreateVDI"] = errors.New("boom")
	if _, err := newTestBuilder(f).Build(context.Background(), testSpec(f)); err == nil {
		t.Fatal("Build succeeded")
	}
	if slices.Contains(f.calls, "DeleteVDI") || slices.Contains(f.calls, "DeleteVM") {
		t.Fatalf("deleted something it did not create: %v", f.calls)
	}
}

// TestBuildCanceled checks that Ctrl+C during the upload still cleans up.
func TestBuildCanceled(t *testing.T) {
	f := newFakeAPI()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := newTestBuilder(f)
	spec := testSpec(f)

	// The fake only honors the context in ImportVDI, like a canceled upload.
	if _, err := b.Build(ctx, spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("Build error = %v, want context.Canceled", err)
	}
	if len(f.vdis) != 0 || len(f.vms) != 0 {
		t.Fatalf("left behind: VMs %v, VDIs %v", f.vms, f.vdis)
	}
}

func TestBuildRejectsBaseTemplateWithDisks(t *testing.T) {
	f := newFakeAPI()
	for _, tpl := range f.templates {
		tpl.VBDs = []uuid.UUID{uuid.Must(uuid.NewV4())}
	}
	if _, err := newTestBuilder(f).Build(context.Background(), testSpec(f)); err == nil {
		t.Fatal("Build accepted a base template with disks")
	}
	if slices.Contains(f.calls, "CreateVDI") {
		t.Fatal("created a VDI before checking the base template")
	}
}

func buildTemplate(t *testing.T, f *fakeAPI) uuid.UUID {
	t.Helper()
	id, err := newTestBuilder(f).Build(context.Background(), testSpec(f))
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	return id
}

func TestVerify(t *testing.T) {
	f := newFakeAPI()
	tpl := buildTemplate(t, f)
	f.ipAfter = 3
	b := newTestBuilder(f)

	err := b.Verify(context.Background(), VerifySpec{TemplateID: tpl, PoolID: f.pool, NetworkID: uuid.Must(uuid.NewV4()), Name: "verify", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.vms) != 1 || f.vms[tpl] == nil {
		t.Fatalf("test VM not deleted: %v", f.vms)
	}
	if len(f.vdis) != 1 {
		t.Fatalf("test VM disk not deleted: %v", f.vdis)
	}
	if !slices.Contains(f.calls, "HardShutdownVM") {
		t.Fatal("running test VM was not stopped before deletion")
	}
}

func TestVerifyTalosAPIUnreachable(t *testing.T) {
	f := newFakeAPI()
	tpl := buildTemplate(t, f)
	b := newTestBuilder(f)
	b.dialTalos = func(context.Context, string) error { return errors.New("connection refused") }

	err := b.Verify(context.Background(), VerifySpec{TemplateID: tpl, PoolID: f.pool, Name: "verify", Timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Verify error = %v, want connection refused", err)
	}
	if len(f.vms) != 1 || len(f.vdis) != 1 {
		t.Fatalf("test VM left behind: VMs %v, VDIs %v", f.vms, f.vdis)
	}
}

func TestVerifyNoIP(t *testing.T) {
	f := newFakeAPI()
	tpl := buildTemplate(t, f)
	f.ipAfter = 1 << 30

	err := newTestBuilder(f).Verify(context.Background(), VerifySpec{TemplateID: tpl, PoolID: f.pool, Name: "verify", Timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "xen-guest-agent") {
		t.Fatalf("Verify error = %v, want a guest agent error", err)
	}
	if len(f.vms) != 1 {
		t.Fatalf("test VM left behind: %v", f.vms)
	}
}
