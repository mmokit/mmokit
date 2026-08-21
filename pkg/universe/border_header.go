package universe

import (
	"encoding/binary"
	"math"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/quantize"
)

// The border frame's fixed header, which carries an entity's motion state to
// neighbouring cells so their replicas track the authority between transfers.
//
// The layout is DIMENSION-SELECTED, not shared. A 2D cluster keeps the exact
// 26-byte header it has always had — a 2D game pays no wire byte for a 3D
// feature, on the mesh as much as on the client wire — while a 3D cluster
// carries a third coordinate, a third velocity axis, and full orientation.
//
// Two decode paths at the authority seam would normally be the expensive kind
// of mistake. It is safe here because a cluster cannot be mixed: the dimension
// is part of the schema fingerprint and mesh admission refuses a peer whose
// fingerprint disagrees, so a 2D and a 3D border decoder can never meet.
//
//	2D, 26 bytes:  worldX f32 | worldY f32 | radius f32 |
//	               qvx i16 | qvy i16 | qangle u16 | producedAtMs u64
//
//	3D, 37 bytes:  worldX f32 | worldY f32 | worldZ f32 | radius f32 |
//	               qvx i16 | qvy i16 | qvz i16 | qquat 7B | producedAtMs u64
const (
	borderHeader2D = 26
	borderHeader3D = 37

	// borderMinTail is the smallest component tail: the two-byte
	// "unchanged since last frame" sentinel.
	borderMinTail = 2
)

// borderHeaderSize returns the fixed header width for a dimension.
func borderHeaderSize(d Dimension) int {
	if d == Dimension3D {
		return borderHeader3D
	}
	return borderHeader2D
}

// appendBorderHeader writes the fixed header for d.
//
// vz and rot are ignored in a 2D profile, and pos.Z with them: passing the
// full state and letting the encoder drop what its profile does not carry
// keeps the call site free of dimension branches.
func appendBorderHeader(
	dst []byte, d Dimension,
	worldX, worldY, worldZ, radius, vx, vy, vz float32,
	rot component.Rotation,
	producedAtMs uint64,
) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(worldX))
	dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(worldY))
	if d == Dimension3D {
		dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(worldZ))
	}
	dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(radius))
	dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVelI16(vx, 2000)))
	dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVelI16(vy, 2000)))
	if d == Dimension3D {
		dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVelI16(vz, 2000)))
		dst = appendQuat56(dst, quantize.Quat(rot.X, rot.Y, rot.Z, rot.W))
	} else {
		// A 2D profile flattens orientation to yaw, which is all it has.
		dst = binary.LittleEndian.AppendUint16(dst, quantize.Angle(rot.Yaw()))
	}
	return binary.LittleEndian.AppendUint64(dst, producedAtMs)
}

// borderHeaderFields is a decoded fixed header.
type borderHeaderFields struct {
	WorldX, WorldY, WorldZ float32
	Radius                 float32
	VX, VY, VZ             float32
	Rot                    component.Rotation
	ProducedAtMs           uint64
}

// parseBorderHeader decodes the fixed header for d. ok is false when buf is
// too short to hold a header plus the minimum tail.
func parseBorderHeader(buf []byte, d Dimension) (f borderHeaderFields, tail []byte, ok bool) {
	size := borderHeaderSize(d)
	if len(buf) < size+borderMinTail {
		return f, nil, false
	}
	off := 0
	readF32 := func() float32 {
		v := math.Float32frombits(binary.LittleEndian.Uint32(buf[off : off+4]))
		off += 4
		return v
	}
	readI16 := func() int16 {
		v := int16(binary.LittleEndian.Uint16(buf[off : off+2]))
		off += 2
		return v
	}

	f.WorldX = readF32()
	f.WorldY = readF32()
	if d == Dimension3D {
		f.WorldZ = readF32()
	}
	f.Radius = readF32()
	f.VX = dequantizeVelI16(readI16(), 2000)
	f.VY = dequantizeVelI16(readI16(), 2000)
	if d == Dimension3D {
		f.VZ = dequantizeVelI16(readI16(), 2000)
		x, y, z, w := quantize.UnQuat(readQuat56(buf[off : off+quantize.QuatWireSize]))
		off += quantize.QuatWireSize
		f.Rot = component.Rotation{X: x, Y: y, Z: z, W: w}
	} else {
		f.Rot = component.RotationFromYaw(quantize.UnAngle(binary.LittleEndian.Uint16(buf[off : off+2])))
		off += 2
	}
	f.ProducedAtMs = binary.LittleEndian.Uint64(buf[off : off+8])
	off += 8
	return f, buf[off:], true
}

// appendQuat56 writes the low 56 bits of v, little-endian, as 7 bytes.
func appendQuat56(dst []byte, v uint64) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	return append(dst, buf[:quantize.QuatWireSize]...)
}

// readQuat56 reads 7 little-endian bytes back into a 56-bit value.
func readQuat56(b []byte) uint64 {
	var buf [8]byte
	copy(buf[:], b[:quantize.QuatWireSize])
	return binary.LittleEndian.Uint64(buf[:])
}
