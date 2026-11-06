package image

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/vatesfr/xenorchestra-go-sdk/pkg/payloads"

	"github.com/littlejo/xo-talos/internal/vhd"
)

func writeZstd(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "disk.raw.zst")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func readAll(t *testing.T, d *Disk) []byte {
	t.Helper()
	r, length, err := d.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(b)) != length {
		t.Fatalf("stream is %d bytes, Open announced %d", len(b), length)
	}
	return b
}

func TestFromCompressedFile(t *testing.T) {
	raw := make([]byte, 3*vhd.BlockSize)
	copy(raw[vhd.BlockSize:], "talos")
	path := writeZstd(t, raw)

	d, err := FromCompressedFile(path, payloads.VDIFormatRaw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.VirtualSize() != int64(len(raw)) {
		t.Fatalf("VirtualSize = %d", d.VirtualSize())
	}
	if !bytes.Equal(readAll(t, d), raw) {
		t.Fatal("raw stream differs")
	}

	d, err = FromCompressedFile(path, payloads.VDIFormatVHD, nil)
	if err != nil {
		t.Fatal(err)
	}
	if used, total := d.UsedBlocks(); used != 1 || total != 3 {
		t.Fatalf("UsedBlocks = %d/%d, want 1/3", used, total)
	}
	v := readAll(t, d)
	if string(v[:8]) != "conectix" || len(v) >= len(raw) {
		t.Fatalf("not a sparse VHD: %d bytes starting with %q", len(v), v[:8])
	}
	// Each upload opens a fresh stream. Only the footers differ (unique ID
	// and timestamp).
	if v2 := readAll(t, d); !bytes.Equal(v2[512:len(v2)-512], v[512:len(v)-512]) {
		t.Fatal("second upload stream differs")
	}
}

func TestUnsupportedFormat(t *testing.T) {
	if _, err := FromCompressedFile(writeZstd(t, make([]byte, 512)), "qcow2", nil); err == nil {
		t.Fatal("accepted an unsupported format")
	}
}
