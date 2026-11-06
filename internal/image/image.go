// Package image turns a Talos disk image into the upload stream Xen Orchestra
// imports, as a raw disk or as a dynamic VHD that skips empty blocks.
package image

import (
	"fmt"
	"io"

	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"

	"github.com/littlejo/xo-talos/internal/factory"
	"github.com/littlejo/xo-talos/internal/progress"
	"github.com/littlejo/xo-talos/internal/vhd"
)

// Disk is an upload-ready disk image. It implements xo.Disk.
type Disk struct {
	format payloads.VDIFormat
	layout vhd.Layout
	// openRaw returns the raw disk content from its start.
	openRaw func() (io.ReadCloser, error)
}

// FromCompressedFile returns the disk image in a compressed file saved by
// factory.Download, uploaded in the given format. The raw disk is never
// written: it is decompressed on the fly, once to find the empty blocks and
// once per upload.
// rep reports the progress of the scan; it may be nil.
func FromCompressedFile(path string, format payloads.VDIFormat, rep *progress.Reporter) (*Disk, error) {
	return newDisk(func() (io.ReadCloser, error) { return factory.OpenRaw(path) }, format, rep)
}

func newDisk(openRaw func() (io.ReadCloser, error), format payloads.VDIFormat, rep *progress.Reporter) (*Disk, error) {
	if format != payloads.VDIFormatRaw && format != payloads.VDIFormatVHD {
		return nil, fmt.Errorf("unsupported disk format %q", format)
	}
	r, err := openRaw()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	pr := rep.Wrap(r, 0, "scan")
	layout, err := vhd.Scan(pr)
	if err != nil {
		return nil, err
	}
	pr.Finish()
	return &Disk{format: format, layout: layout, openRaw: openRaw}, nil
}

// VirtualSize is the size of the disk as the VM sees it.
func (d *Disk) VirtualSize() int64 { return d.layout.Size }

// Format is the upload format.
func (d *Disk) Format() payloads.VDIFormat { return d.format }

// UsedBlocks returns how many vhd.BlockSize blocks hold data, out of total.
func (d *Disk) UsedBlocks() (used, total int) {
	return d.layout.UsedBlocks(), len(d.layout.Used)
}

// Open returns the upload stream and its exact length.
func (d *Disk) Open() (io.ReadCloser, int64, error) {
	raw, err := d.openRaw()
	if err != nil {
		return nil, 0, err
	}
	if d.format == payloads.VDIFormatRaw {
		return raw, d.layout.Size, nil
	}
	return &closeBoth{ReadCloser: d.layout.Reader(raw), other: raw}, d.layout.Length(), nil
}

type closeBoth struct {
	io.ReadCloser
	other io.Closer
}

func (c *closeBoth) Close() error {
	err := c.ReadCloser.Close()
	if err2 := c.other.Close(); err == nil {
		err = err2
	}
	return err
}
