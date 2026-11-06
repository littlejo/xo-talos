package vhd

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"testing"
)

// decode is an independent dynamic-VHD reader used to check the encoder. It
// returns the raw disk.
func decode(t *testing.T, v []byte) []byte {
	t.Helper()
	if len(v) < footerSize+headerSize+footerSize {
		t.Fatalf("VHD too short: %d bytes", len(v))
	}
	footer := v[:footerSize]
	if !bytes.Equal(footer, v[len(v)-footerSize:]) {
		t.Fatal("footer copy at the start differs from the footer at the end")
	}
	if string(footer[:8]) != "conectix" {
		t.Fatalf("footer cookie %q", footer[:8])
	}
	checkChecksum(t, "footer", footer, 64)
	if typ := binary.BigEndian.Uint32(footer[60:]); typ != 3 {
		t.Fatalf("disk type %d, want 3 (dynamic)", typ)
	}
	size := int64(binary.BigEndian.Uint64(footer[48:]))
	if orig := int64(binary.BigEndian.Uint64(footer[40:])); orig != size {
		t.Fatalf("original size %d != current size %d", orig, size)
	}

	hdrOff := binary.BigEndian.Uint64(footer[16:])
	header := v[hdrOff : hdrOff+headerSize]
	if string(header[:8]) != "cxsparse" {
		t.Fatalf("header cookie %q", header[:8])
	}
	checkChecksum(t, "header", header, 36)
	batOff := binary.BigEndian.Uint64(header[16:])
	entries := int(binary.BigEndian.Uint32(header[28:]))
	blockSize := int64(binary.BigEndian.Uint32(header[32:]))
	if want := int((size + blockSize - 1) / blockSize); entries != want {
		t.Fatalf("%d BAT entries, want %d", entries, want)
	}

	raw := make([]byte, int64(entries)*blockSize)
	for i := range entries {
		entry := binary.BigEndian.Uint32(v[batOff+uint64(i)*4:])
		if entry == unusedEntry {
			continue
		}
		off := int64(entry) * sectorSize
		if off%sectorSize != 0 || off+bitmapSize+blockSize > int64(len(v))-footerSize {
			t.Fatalf("block %d at bad offset %d", i, off)
		}
		if !bytes.Equal(v[off:off+bitmapSize], bytes.Repeat([]byte{0xFF}, bitmapSize)) {
			t.Fatalf("block %d: sector bitmap not all ones", i)
		}
		copy(raw[int64(i)*blockSize:], v[off+bitmapSize:off+bitmapSize+blockSize])
	}
	return raw[:size]
}

func checkChecksum(t *testing.T, what string, b []byte, at int) {
	t.Helper()
	c := make([]byte, len(b))
	copy(c, b)
	want := binary.BigEndian.Uint32(c[at:])
	binary.BigEndian.PutUint32(c[at:], 0)
	if got := checksum(c); got != want {
		t.Fatalf("%s checksum %#x, want %#x", what, want, got)
	}
}

func encode(t *testing.T, raw []byte) (Layout, []byte) {
	t.Helper()
	l, err := Scan(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := l.Write(&out, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if int64(out.Len()) != l.Length() {
		t.Fatalf("wrote %d bytes, Length() = %d", out.Len(), l.Length())
	}
	return l, out.Bytes()
}

func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	// 5.5 blocks: data in blocks 0, 2 and the partial last one; 1, 3 and 4
	// empty.
	raw := make([]byte, 5*BlockSize+BlockSize/2)
	for _, off := range []int{0, 2*BlockSize + 12345, 5*BlockSize + 512} {
		for i := range 4096 {
			raw[off+i] = byte(rng.Uint32())
		}
	}
	l, v := encode(t, raw)
	if want := []bool{true, false, true, false, false, true}; !equalBools(l.Used, want) {
		t.Fatalf("used blocks %v, want %v", l.Used, want)
	}
	if got := decode(t, v); !bytes.Equal(got, raw) {
		t.Fatal("decoded disk differs from the original")
	}
	if int64(len(v)) >= int64(len(raw)) {
		t.Fatalf("VHD (%d bytes) is not smaller than the sparse raw disk (%d bytes)", len(v), len(raw))
	}
}

func TestEmptyBlocksOnly(t *testing.T) {
	raw := make([]byte, 3*BlockSize)
	l, v := encode(t, raw)
	if l.UsedBlocks() != 0 {
		t.Fatalf("%d used blocks in an empty disk", l.UsedBlocks())
	}
	if got := decode(t, v); !bytes.Equal(got, raw) {
		t.Fatal("decoded disk differs")
	}
}

func TestReader(t *testing.T) {
	raw := bytes.Repeat([]byte{1, 2, 3, 4}, BlockSize/2) // 2 full blocks
	l, err := Scan(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r := l.Reader(bytes.NewReader(raw))
	defer func() { _ = r.Close() }()
	v, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decode(t, v), raw) {
		t.Fatal("decoded disk differs")
	}
}

func TestWriteDetectsChangedSource(t *testing.T) {
	raw := make([]byte, 2*BlockSize)
	l, err := Scan(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw[BlockSize+7] = 1
	if err := l.Write(io.Discard, bytes.NewReader(raw)); err == nil {
		t.Fatal("Write accepted data in a block scanned as empty")
	}
	if err := l.Write(io.Discard, bytes.NewReader(raw[:BlockSize])); err == nil {
		t.Fatal("Write accepted a shorter source")
	}
	if err := l.Write(io.Discard, bytes.NewReader(append(make([]byte, 2*BlockSize), 0))); err == nil {
		t.Fatal("Write accepted a longer source")
	}
}

func TestScanRejectsBadSizes(t *testing.T) {
	if _, err := Scan(bytes.NewReader(nil)); err == nil {
		t.Error("Scan accepted an empty disk")
	}
	if _, err := Scan(bytes.NewReader(make([]byte, 1000))); err == nil {
		t.Error("Scan accepted a size that is not a multiple of 512")
	}
}

func TestGeometry(t *testing.T) {
	// Values computed by hand with the algorithm of the specification.
	for size, want := range map[int64][3]uint32{
		4453302272:      {8628, 16, 63}, // Talos v1.14.5 nocloud image
		10 << 20:        {301, 4, 17},
		30 << 30:        {62415, 16, 63},
		40 << 30:        {20560, 16, 255},
		3 * 65535 << 30: {65535, 16, 255}, // capped
	} {
		g := geometry(size)
		got := [3]uint32{g >> 16, g >> 8 & 0xFF, g & 0xFF}
		if got != want {
			t.Errorf("geometry(%d) = %v, want %v", size, got, want)
		}
	}
}

func equalBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
