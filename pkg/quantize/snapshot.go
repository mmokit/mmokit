package quantize

import (
	"encoding/binary"
	"math"
)

// SnapshotWriter builds fixed-layout binary buffers in big-endian byte order.
type SnapshotWriter struct {
	buf []byte
	pos int
}

// NewSnapshotWriter returns a writer backed by the given buffer.
func NewSnapshotWriter(buf []byte) *SnapshotWriter {
	return &SnapshotWriter{buf: buf}
}

// Uint8 writes a single byte.
func (w *SnapshotWriter) Uint8(v uint8) {
	w.buf[w.pos] = v
	w.pos++
}

// Uint16 writes a big-endian uint16.
func (w *SnapshotWriter) Uint16(v uint16) {
	binary.BigEndian.PutUint16(w.buf[w.pos:], v)
	w.pos += 2
}

// Uint32 writes a big-endian uint32.
func (w *SnapshotWriter) Uint32(v uint32) {
	binary.BigEndian.PutUint32(w.buf[w.pos:], v)
	w.pos += 4
}

// Int16 writes a big-endian int16.
func (w *SnapshotWriter) Int16(v int16) {
	binary.BigEndian.PutUint16(w.buf[w.pos:], uint16(v))
	w.pos += 2
}

// Float32 writes an IEEE 754 big-endian float32.
func (w *SnapshotWriter) Float32(v float32) {
	binary.BigEndian.PutUint32(w.buf[w.pos:], math.Float32bits(v))
	w.pos += 4
}

// Float32Pair writes two float32 values (8 bytes total). Preferred for positions
// in cell-meshed games where quantization causes boundary shift artifacts.
func (w *SnapshotWriter) Float32Pair(x, y float32) {
	w.Float32(x)
	w.Float32(y)
}

// QPos quantizes a cell-local position and writes it as uint16.
func (w *SnapshotWriter) QPos(pos, cellSize float32) {
	w.Uint16(Pos(pos, cellSize))
}

// QRel quantizes a viewer-relative offset and writes it as int16.
func (w *SnapshotWriter) QRel(offset, halfRange float32) {
	w.Int16(Rel(offset, halfRange))
}

// QAngle quantizes an angle in radians and writes it as uint16.
func (w *SnapshotWriter) QAngle(radians float32) {
	w.Uint16(Angle(radians))
}

// QNorm quantizes a [0,1] value and writes it as uint8.
func (w *SnapshotWriter) QNorm(v float32) {
	w.Uint8(Norm(v))
}

// QVel quantizes a velocity component and writes it as int16.
func (w *SnapshotWriter) QVel(v, scale float32) {
	w.Int16(Vel(v, scale))
}

// Bool writes a boolean as uint8 (1 for true, 0 for false).
func (w *SnapshotWriter) Bool(v bool) {
	if v {
		w.Uint8(1)
	} else {
		w.Uint8(0)
	}
}

// Bytes returns the written portion of the buffer.
func (w *SnapshotWriter) Bytes() []byte {
	return w.buf[:w.pos]
}

// Len returns the number of bytes written.
func (w *SnapshotWriter) Len() int {
	return w.pos
}

// Reset resets the writer position to the beginning of the buffer.
func (w *SnapshotWriter) Reset() {
	w.pos = 0
}

// SnapshotReader decodes fixed-layout binary buffers in big-endian byte order.
type SnapshotReader struct {
	buf []byte
	pos int
}

// NewSnapshotReader returns a reader backed by the given buffer.
func NewSnapshotReader(buf []byte) *SnapshotReader {
	return &SnapshotReader{buf: buf}
}

// Uint8 reads a single byte.
func (r *SnapshotReader) Uint8() uint8 {
	v := r.buf[r.pos]
	r.pos++
	return v
}

// Uint16 reads a big-endian uint16.
func (r *SnapshotReader) Uint16() uint16 {
	v := binary.BigEndian.Uint16(r.buf[r.pos:])
	r.pos += 2
	return v
}

// Uint32 reads a big-endian uint32.
func (r *SnapshotReader) Uint32() uint32 {
	v := binary.BigEndian.Uint32(r.buf[r.pos:])
	r.pos += 4
	return v
}

// Int16 reads a big-endian int16.
func (r *SnapshotReader) Int16() int16 {
	v := int16(binary.BigEndian.Uint16(r.buf[r.pos:]))
	r.pos += 2
	return v
}

// Float32 reads an IEEE 754 big-endian float32.
func (r *SnapshotReader) Float32() float32 {
	v := math.Float32frombits(binary.BigEndian.Uint32(r.buf[r.pos:]))
	r.pos += 4
	return v
}

// Float32Pair reads two float32 values (8 bytes total).
func (r *SnapshotReader) Float32Pair() (float32, float32) {
	x := r.Float32()
	y := r.Float32()
	return x, y
}

// UnQPos reads a uint16 and dequantizes it to a cell-local position.
func (r *SnapshotReader) UnQPos(cellSize float32) float32 {
	return UnPos(r.Uint16(), cellSize)
}

// UnQRel reads an int16 and dequantizes it to a viewer-relative offset.
func (r *SnapshotReader) UnQRel(halfRange float32) float32 {
	return UnRel(r.Int16(), halfRange)
}

// UnQAngle reads a uint16 and dequantizes it to radians.
func (r *SnapshotReader) UnQAngle() float32 {
	return UnAngle(r.Uint16())
}

// UnQNorm reads a uint8 and dequantizes it to [0, 1].
func (r *SnapshotReader) UnQNorm() float32 {
	return UnNorm(r.Uint8())
}

// UnQVel reads an int16 and dequantizes it to a velocity component.
func (r *SnapshotReader) UnQVel(scale float32) float32 {
	return UnVel(r.Int16(), scale)
}

// Bool reads a uint8 and returns true if non-zero.
func (r *SnapshotReader) Bool() bool {
	return r.Uint8() != 0
}

// Remaining returns the number of unread bytes.
func (r *SnapshotReader) Remaining() int {
	return len(r.buf) - r.pos
}
