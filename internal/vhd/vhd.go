// Package vhd streams a raw disk image as a dynamic VHD (Microsoft Virtual
// Hard Disk Image Format Specification, version 1.0) that only carries the
// blocks holding data.
//
// Encoding takes two passes over the raw image: Scan finds the blocks that
// hold data, which gives the exact length of the VHD up front (Xen Orchestra
// needs a Content-Length), then Write streams the VHD.
package vhd

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// BlockSize is the size of a VHD data block, the default of the format.
	BlockSize = 2 << 20

	sectorSize = 512
	footerSize = 512
	headerSize = 1024
	// bitmapSize is the sector bitmap that precedes each block: one bit per
	// 512-byte sector, padded to a sector.
	bitmapSize = BlockSize / sectorSize / 8

	unusedEntry = 0xFFFFFFFF
)

// Layout records which blocks of a raw disk hold data.
type Layout struct {
	Size int64  // raw disk size in bytes, a multiple of 512
	Used []bool // one entry per BlockSize block
}

// Scan reads a raw disk image to its end and records which blocks hold data.
func Scan(raw io.Reader) (Layout, error) {
	var l Layout
	buf := make([]byte, BlockSize)
	for {
		n, err := io.ReadFull(raw, buf)
		if n > 0 {
			l.Used = append(l.Used, !isZero(buf[:n]))
			l.Size += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return Layout{}, err
		}
	}
	if l.Size == 0 {
		return Layout{}, errors.New("vhd: empty disk image")
	}
	if l.Size%sectorSize != 0 {
		return Layout{}, fmt.Errorf("vhd: disk size %d is not a multiple of %d", l.Size, sectorSize)
	}
	return l, nil
}

// UsedBlocks returns how many blocks hold data.
func (l Layout) UsedBlocks() int {
	n := 0
	for _, u := range l.Used {
		if u {
			n++
		}
	}
	return n
}

func (l Layout) batSize() int64 {
	return roundUp(int64(len(l.Used))*4, sectorSize)
}

func (l Layout) dataOffset() int64 {
	return footerSize + headerSize + l.batSize()
}

// Length returns the exact length of the VHD stream.
func (l Layout) Length() int64 {
	return l.dataOffset() + int64(l.UsedBlocks())*(bitmapSize+BlockSize) + footerSize
}

// Write streams the VHD of the raw image to w. raw must have the content that
// Scan read: a block that was empty and now holds data is an error.
func (l Layout) Write(w io.Writer, raw io.Reader) error {
	footer, err := l.footer(time.Now())
	if err != nil {
		return err
	}
	if _, err := w.Write(footer); err != nil {
		return err
	}
	if _, err := w.Write(l.header()); err != nil {
		return err
	}
	if _, err := w.Write(l.bat()); err != nil {
		return err
	}

	bitmap := bytes.Repeat([]byte{0xFF}, bitmapSize)
	buf := make([]byte, BlockSize)
	for i, used := range l.Used {
		n, err := io.ReadFull(raw, buf)
		if err != nil && (err != io.ErrUnexpectedEOF || i != len(l.Used)-1) {
			return fmt.Errorf("vhd: read block %d: %w", i, err)
		}
		clear(buf[n:]) // the last block is padded with zeros
		if !used {
			if !isZero(buf) {
				return fmt.Errorf("vhd: block %d holds data but was empty when scanned", i)
			}
			continue
		}
		if _, err := w.Write(bitmap); err != nil {
			return err
		}
		if _, err := w.Write(buf); err != nil {
			return err
		}
	}
	if n, _ := raw.Read(buf[:1]); n > 0 {
		return errors.New("vhd: disk image is longer than when scanned")
	}
	_, err = w.Write(footer)
	return err
}

// Reader returns the VHD of raw as a stream, encoded in a goroutine.
func (l Layout) Reader(raw io.Reader) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		_ = pw.CloseWithError(l.Write(pw, raw))
	}()
	return pr
}

func (l Layout) footer(now time.Time) ([]byte, error) {
	b := make([]byte, footerSize)
	copy(b[0:], "conectix")
	binary.BigEndian.PutUint32(b[8:], 2)        // features: reserved bit, always set
	binary.BigEndian.PutUint32(b[12:], 0x10000) // file format version 1.0
	binary.BigEndian.PutUint64(b[16:], footerSize)
	epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	binary.BigEndian.PutUint32(b[24:], uint32(now.Sub(epoch)/time.Second))
	copy(b[28:], "txot")                        // creator application
	binary.BigEndian.PutUint32(b[32:], 0x10000) // creator version
	copy(b[36:], "Wi2k")                        // creator host OS
	binary.BigEndian.PutUint64(b[40:], uint64(l.Size))
	binary.BigEndian.PutUint64(b[48:], uint64(l.Size))
	binary.BigEndian.PutUint32(b[56:], geometry(l.Size))
	binary.BigEndian.PutUint32(b[60:], 3) // disk type: dynamic
	if _, err := rand.Read(b[68:84]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(b[64:], checksum(b))
	return b, nil
}

func (l Layout) header() []byte {
	b := make([]byte, headerSize)
	copy(b[0:], "cxsparse")
	binary.BigEndian.PutUint64(b[8:], 0xFFFFFFFFFFFFFFFF)     // data offset: unused
	binary.BigEndian.PutUint64(b[16:], footerSize+headerSize) // BAT offset
	binary.BigEndian.PutUint32(b[24:], 0x10000)               // header version 1.0
	binary.BigEndian.PutUint32(b[28:], uint32(len(l.Used)))   // max table entries
	binary.BigEndian.PutUint32(b[32:], BlockSize)
	binary.BigEndian.PutUint32(b[36:], checksum(b))
	return b
}

func (l Layout) bat() []byte {
	b := bytes.Repeat([]byte{0xFF}, int(l.batSize()))
	next := uint32(l.dataOffset() / sectorSize)
	for i, used := range l.Used {
		entry := uint32(unusedEntry)
		if used {
			entry = next
			next += (bitmapSize + BlockSize) / sectorSize
		}
		binary.BigEndian.PutUint32(b[i*4:], entry)
	}
	return b
}

// geometry returns the CHS geometry field, computed as in appendix A of the
// specification.
func geometry(size int64) uint32 {
	total := size / sectorSize
	if total > 65535*16*255 {
		total = 65535 * 16 * 255
	}
	var spt, heads, cth int64
	if total >= 65535*16*63 {
		spt, heads = 255, 16
		cth = total / spt
	} else {
		spt = 17
		cth = total / spt
		heads = (cth + 1023) / 1024
		if heads < 4 {
			heads = 4
		}
		if cth >= heads*1024 || heads > 16 {
			spt, heads = 31, 16
			cth = total / spt
		}
		if cth >= heads*1024 {
			spt, heads = 63, 16
			cth = total / spt
		}
	}
	cylinders := cth / heads
	return uint32(cylinders)<<16 | uint32(heads)<<8 | uint32(spt)
}

// checksum is the one's complement of the sum of all bytes; the checksum
// field itself must be zero when it is computed.
func checksum(b []byte) uint32 {
	var sum uint32
	for _, c := range b {
		sum += uint32(c)
	}
	return ^sum
}

func isZero(b []byte) bool {
	for len(b) >= 8 {
		if binary.LittleEndian.Uint64(b) != 0 {
			return false
		}
		b = b[8:]
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func roundUp(n, to int64) int64 {
	return (n + to - 1) / to * to
}
