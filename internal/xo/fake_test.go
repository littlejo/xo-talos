package xo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/gofrs/uuid"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"
)

// fakeAPI is an in-memory Xen Orchestra. Like XO, deleting a VM or a
// template also deletes its disks.
type fakeAPI struct {
	mu        sync.Mutex
	pool      uuid.UUID
	templates map[uuid.UUID]*payloads.Template
	vms       map[uuid.UUID]*fakeVM
	vdis      map[uuid.UUID][]byte

	failOn map[string]error // method name -> error to return
	// templateDelay hides a new template from this many Templates calls,
	// like XO's asynchronous object cache.
	templateDelay int
	calls         []string
	ipAfter       int // GetVM reports an IP after this many calls on a running VM
	bootOrder     map[uuid.UUID]string
	patches       map[uuid.UUID]VMPatch
}

type fakeVM struct {
	vm        payloads.VM
	disks     []uuid.UUID
	template  bool
	getCalls  int
	hiddenFor int // Templates calls during which the template stays hidden
}

func newFakeAPI() *fakeAPI {
	f := &fakeAPI{
		pool:      uuid.Must(uuid.NewV4()),
		templates: map[uuid.UUID]*payloads.Template{},
		vms:       map[uuid.UUID]*fakeVM{},
		vdis:      map[uuid.UUID][]byte{},
		failOn:    map[string]error{},
		bootOrder: map[uuid.UUID]string{},
		patches:   map[uuid.UUID]VMPatch{},
	}
	base := uuid.Must(uuid.NewV4())
	f.templates[base] = &payloads.Template{
		ID: f.pool.String() + "-" + base.String(), UUID: base, NameLabel: "Other install media", Pool: f.pool,
	}
	return f
}

func (f *fakeAPI) enter(method string) error {
	f.calls = append(f.calls, method)
	return f.failOn[method]
}

func (f *fakeAPI) SRPool(context.Context, uuid.UUID) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pool, f.enter("SRPool")
}

func (f *fakeAPI) NetworkPool(context.Context, uuid.UUID) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pool, f.enter("NetworkPool")
}

func (f *fakeAPI) Templates(context.Context, string) ([]*payloads.Template, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("Templates"); err != nil {
		return nil, err
	}
	var out []*payloads.Template
	for _, t := range f.templates {
		out = append(out, t)
	}
	for id, v := range f.vms {
		if v.template && v.hiddenFor > 0 {
			v.hiddenFor--
			continue
		}
		if v.template {
			out = append(out, &payloads.Template{ID: id.String(), UUID: id, NameLabel: v.vm.NameLabel, Pool: f.pool, Tags: v.vm.Tags, VBDs: v.disks})
		}
	}
	return out, nil
}

func (f *fakeAPI) DeleteTemplate(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteTemplate"); err != nil {
		return err
	}
	v, ok := f.vms[id]
	if !ok || !v.template {
		return fmt.Errorf("template %s not found", id)
	}
	f.deleteVMLocked(id)
	return nil
}

func (f *fakeAPI) CreateVDI(_ context.Context, p payloads.VDICreateParams) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateVDI"); err != nil {
		return uuid.Nil, err
	}
	id := uuid.Must(uuid.NewV4())
	f.vdis[id] = nil
	return id, nil
}

func (f *fakeAPI) ImportVDI(ctx context.Context, id uuid.UUID, _ payloads.VDIFormat, r io.Reader, size int64) error {
	f.mu.Lock()
	err := f.enter("ImportVDI")
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("content length %d, read %d", size, len(data))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vdis[id] = data
	return nil
}

func (f *fakeAPI) VDIExists(_ context.Context, id uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.vdis[id]
	return ok, f.enter("VDIExists")
}

func (f *fakeAPI) DeleteVDI(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteVDI"); err != nil {
		return err
	}
	delete(f.vdis, id)
	return nil
}

func (f *fakeAPI) CreateVM(_ context.Context, pool uuid.UUID, p *payloads.CreateVMParams) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateVM"); err != nil {
		return uuid.Nil, err
	}
	id := uuid.Must(uuid.NewV4())
	v := &fakeVM{vm: payloads.VM{ID: id, NameLabel: p.NameLabel, PowerState: payloads.PowerStateHalted}}
	if src, ok := f.vms[p.Template]; ok && p.Clone != nil && *p.Clone {
		for range src.disks { // clone the template's disks
			d := uuid.Must(uuid.NewV4())
			f.vdis[d] = nil
			v.disks = append(v.disks, d)
		}
	}
	if p.Boot != nil && *p.Boot {
		v.vm.PowerState = payloads.PowerStateRunning
	}
	f.vms[id] = v
	return id, nil
}

func (f *fakeAPI) GetVM(_ context.Context, id uuid.UUID) (*payloads.VM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("GetVM"); err != nil {
		return nil, err
	}
	v, ok := f.vms[id]
	if !ok {
		return nil, errors.New("not found")
	}
	if v.vm.PowerState == payloads.PowerStateRunning {
		v.getCalls++
		if v.getCalls > f.ipAfter {
			v.vm.MainIpAddress = "192.0.2.10"
		}
	}
	vm := v.vm
	return &vm, nil
}

func (f *fakeAPI) HardShutdownVM(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("HardShutdownVM"); err != nil {
		return err
	}
	f.vms[id].vm.PowerState = payloads.PowerStateHalted
	return nil
}

func (f *fakeAPI) DeleteVM(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteVM"); err != nil {
		return err
	}
	v, ok := f.vms[id]
	if !ok {
		return errors.New("not found")
	}
	if v.vm.PowerState != payloads.PowerStateHalted {
		return errors.New("VM is running")
	}
	f.deleteVMLocked(id)
	return nil
}

func (f *fakeAPI) deleteVMLocked(id uuid.UUID) {
	for _, d := range f.vms[id].disks {
		delete(f.vdis, d)
	}
	delete(f.vms, id)
}

func (f *fakeAPI) PatchVM(_ context.Context, id uuid.UUID, p VMPatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("PatchVM"); err != nil {
		return err
	}
	f.patches[id] = p
	f.vms[id].vm.Tags = p.Tags
	return nil
}

func (f *fakeAPI) SetBootOrder(_ context.Context, id uuid.UUID, order string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("SetBootOrder"); err != nil {
		return err
	}
	f.bootOrder[id] = order
	return nil
}

func (f *fakeAPI) ConvertToTemplate(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ConvertToTemplate"); err != nil {
		return err
	}
	f.vms[id].template = true
	f.vms[id].hiddenFor = f.templateDelay
	return nil
}

func (f *fakeAPI) CreateVBD(_ context.Context, p *payloads.CreateVBDParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateVBD"); err != nil {
		return err
	}
	f.vms[p.VM].disks = append(f.vms[p.VM].disks, p.VDI)
	return nil
}
