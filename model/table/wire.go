package table

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
)

// Bounded little-endian readers and writers for the package's records. Every
// length is checked against a limit before anything is allocated, so a
// forged record costs a bounded read and an ErrCorrupt.

var errShort = errors.New("record ends early")

type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: %s", chunk.ErrCorrupt, fmt.Sprintf(format, args...))
	}
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || len(r.b)-r.off < n {
		r.fail("%v: want %d bytes at %d of %d", errShort, n, r.off, len(r.b))
		return nil
	}
	out := r.b[r.off : r.off+n]
	r.off += n
	return out
}

func (r *reader) u8() uint8 {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *reader) u16() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (r *reader) u32() uint32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *reader) u64() uint64 {
	b := r.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

// uvarint reads a minimal varint no larger than limit.
func (r *reader) uvarint(limit uint64) uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b[r.off:])
	if n <= 0 {
		r.fail("varint at %d does not decode", r.off)
		return 0
	}
	if n > 1 && r.b[r.off+n-1] == 0 {
		r.fail("varint at %d is not minimal", r.off)
		return 0
	}
	if v > limit {
		r.fail("length %d at %d over the limit %d", v, r.off, limit)
		return 0
	}
	r.off += n
	return v
}

// bytes reads a length-prefixed byte string of at most limit bytes.
func (r *reader) bytes(limit uint64) []byte {
	n := r.uvarint(limit)
	return r.take(int(n))
}

// done fails on trailing bytes.
func (r *reader) done() error {
	if r.err == nil && r.off != len(r.b) {
		r.fail("%d trailing bytes", len(r.b)-r.off)
	}
	return r.err
}

type writer struct{ b []byte }

func (w *writer) raw(b []byte)     { w.b = append(w.b, b...) }
func (w *writer) u8(v uint8)       { w.b = append(w.b, v) }
func (w *writer) u16(v uint16)     { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *writer) u32(v uint32)     { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *writer) u64(v uint64)     { w.b = binary.LittleEndian.AppendUint64(w.b, v) }
func (w *writer) uvarint(v uint64) { w.b = binary.AppendUvarint(w.b, v) }
func (w *writer) bytes(b []byte) {
	w.uvarint(uint64(len(b)))
	w.b = append(w.b, b...)
}
