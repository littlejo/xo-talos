package xo

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gofrs/uuid"
	v1 "github.com/vatesfr/xenorchestra-go-sdk/client"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/services/library"
	"github.com/vatesfr/xenorchestra-go-sdk/v2/client"
)

// API is the set of Xen Orchestra operations the Builder uses. NewSDKAPI
// implements it with the SDK; tests use an in-memory fake.
type API interface {
	SRPool(ctx context.Context, srID uuid.UUID) (uuid.UUID, error)
	NetworkPool(ctx context.Context, networkID uuid.UUID) (uuid.UUID, error)
	Templates(ctx context.Context, filter string) ([]*payloads.Template, error)
	DeleteTemplate(ctx context.Context, id uuid.UUID) error

	CreateVDI(ctx context.Context, params payloads.VDICreateParams) (uuid.UUID, error)
	ImportVDI(ctx context.Context, id uuid.UUID, format payloads.VDIFormat, content io.Reader, size int64) error
	VDIExists(ctx context.Context, id uuid.UUID) (bool, error)
	DeleteVDI(ctx context.Context, id uuid.UUID) error

	CreateVM(ctx context.Context, poolID uuid.UUID, params *payloads.CreateVMParams) (uuid.UUID, error)
	GetVM(ctx context.Context, id uuid.UUID) (*payloads.VM, error)
	HardShutdownVM(ctx context.Context, id uuid.UUID) error
	DeleteVM(ctx context.Context, id uuid.UUID) error
	PatchVM(ctx context.Context, id uuid.UUID, patch VMPatch) error
	SetBootOrder(ctx context.Context, id uuid.UUID, order string) error
	ConvertToTemplate(ctx context.Context, id uuid.UUID) error

	CreateVBD(ctx context.Context, params *payloads.CreateVBDParams) error
}

// VMPatch is the body of PATCH /vms/<id>.
type VMPatch struct {
	Viridian        *bool    `json:"viridian,omitempty"`
	HVMBootFirmware string   `json:"hvmBootFirmware,omitempty"`
	CPUs            int      `json:"cpus,omitempty"`
	Tags            []string `json:"tags,omitempty"`
}

// sdkAPI implements API with the SDK's typed services (lib) and, for calls
// they do not wrap, the SDK's REST client.
type sdkAPI struct {
	lib  library.Library
	rest *client.Client
}

// NewSDKAPI returns an API backed by the Xen Orchestra Go SDK.
func NewSDKAPI(lib library.Library, rest *client.Client) API {
	return &sdkAPI{lib: lib, rest: rest}
}

func (a *sdkAPI) SRPool(ctx context.Context, srID uuid.UUID) (uuid.UUID, error) {
	sr, err := a.lib.SR().Get(ctx, srID)
	if err != nil {
		return uuid.Nil, err
	}
	return sr.Pool, nil
}

func (a *sdkAPI) NetworkPool(ctx context.Context, networkID uuid.UUID) (uuid.UUID, error) {
	n, err := a.lib.Network().Get(ctx, networkID)
	if err != nil {
		return uuid.Nil, err
	}
	return n.Pool, nil
}

func (a *sdkAPI) Templates(ctx context.Context, filter string) ([]*payloads.Template, error) {
	return a.lib.Template().GetAll(ctx, 0, filter)
}

func (a *sdkAPI) DeleteTemplate(ctx context.Context, id uuid.UUID) error {
	var out any
	return client.TypedDelete(ctx, a.rest, "vm-templates/"+id.String(), struct{}{}, &out)
}

func (a *sdkAPI) CreateVDI(ctx context.Context, params payloads.VDICreateParams) (uuid.UUID, error) {
	return a.lib.VDI().Create(ctx, params)
}

func (a *sdkAPI) ImportVDI(ctx context.Context, id uuid.UUID, format payloads.VDIFormat, content io.Reader, size int64) error {
	return a.lib.VDI().Import(ctx, id, format, content, size)
}

func (a *sdkAPI) VDIExists(ctx context.Context, id uuid.UUID) (bool, error) {
	// The SDK does not expose the status code; a failed lookup after a
	// deletion is treated as "gone".
	if _, err := a.lib.VDI().Get(ctx, id); err != nil {
		return false, nil
	}
	return true, nil
}

func (a *sdkAPI) DeleteVDI(ctx context.Context, id uuid.UUID) error {
	return a.lib.VDI().Delete(ctx, id)
}

func (a *sdkAPI) CreateVM(ctx context.Context, poolID uuid.UUID, params *payloads.CreateVMParams) (uuid.UUID, error) {
	return a.lib.Pool().CreateVM(ctx, poolID, *params)
}

func (a *sdkAPI) GetVM(ctx context.Context, id uuid.UUID) (*payloads.VM, error) {
	return a.lib.VM().GetByID(ctx, id)
}

func (a *sdkAPI) HardShutdownVM(ctx context.Context, id uuid.UUID) error {
	_, err := a.lib.VM().HardShutdown(ctx, id)
	return err
}

func (a *sdkAPI) DeleteVM(ctx context.Context, id uuid.UUID) error {
	return a.lib.VM().Delete(ctx, id)
}

func (a *sdkAPI) PatchVM(ctx context.Context, id uuid.UUID, patch VMPatch) error {
	var out any
	return client.TypedPatch(ctx, a.rest, "vms/"+id.String(), patch, &out)
}

func (a *sdkAPI) CreateVBD(ctx context.Context, params *payloads.CreateVBDParams) error {
	_, err := a.lib.VBD().Create(ctx, params)
	return err
}

// The REST API (0.40) can neither set the boot order nor turn a VM into a
// template, so these two go through JSON-RPC.

func (a *sdkAPI) SetBootOrder(_ context.Context, id uuid.UUID, order string) error {
	return a.call("vm.setBootOrder", map[string]any{"vm": id.String(), "order": order})
}

func (a *sdkAPI) ConvertToTemplate(_ context.Context, id uuid.UUID) error {
	return a.call("vm.convertToTemplate", map[string]any{"id": id.String()})
}

func (a *sdkAPI) call(method string, params map[string]any) error {
	rpc, ok := a.lib.V1Client().(*v1.Client)
	if !ok || rpc == nil {
		return errors.New("cannot open the JSON-RPC connection to Xen Orchestra")
	}
	var result any
	if err := rpc.Call(method, params, &result); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	return nil
}
