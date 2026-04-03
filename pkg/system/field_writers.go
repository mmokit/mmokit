package system

import (
	"fmt"
	"strconv"

	"github.com/zenion/mmoserver/pkg/quantize"
)

// hashWriterFunc writes a Go value into a Hasher for change detection.
type hashWriterFunc func(val any, h *Hasher)

// snapshotWriterFunc writes a Go value into a SnapshotWriter for serialization.
type snapshotWriterFunc func(val any, w *quantize.SnapshotWriter)

// initialWriterFunc writes a Go value into a byte slice for InitialData.
type initialWriterFunc func(val any, buf []byte) []byte

// hashWriterFor returns a hash writer closure for the given field metadata.
func hashWriterFor(fm fieldMeta) (hashWriterFunc, error) {
	switch fm.encoding {
	case "f32", "pos":
		return func(val any, h *Hasher) { h.Float32(val.(float32)) }, nil
	case "qvel", "qsize", "qangle", "u16":
		return func(val any, h *Hasher) { h.Float32(toFloat32(val)) }, nil
	case "qnorm", "u8":
		return func(val any, h *Hasher) { h.Uint8(toUint8(val)) }, nil
	case "u32":
		return func(val any, h *Hasher) { h.Uint32(toUint32(val)) }, nil
	case "i16":
		return func(val any, h *Hasher) { h.Int32(int32(toInt16(val))) }, nil
	case "bool":
		return func(val any, h *Hasher) { h.Bool(val.(bool)) }, nil
	case "string":
		return func(val any, h *Hasher) {
			s := val.(string)
			h.Uint32(uint32(len(s)))
			for i := 0; i < len(s); i++ {
				h.Uint8(s[i])
			}
		}, nil
	}
	return nil, fmt.Errorf("no hash writer for encoding %q", fm.encoding)
}

// snapshotWriterFor returns a snapshot writer closure for the given field metadata.
func snapshotWriterFor(fm fieldMeta) (snapshotWriterFunc, error) {
	switch fm.encoding {
	case "f32":
		return func(val any, w *quantize.SnapshotWriter) { w.Float32(toFloat32(val)) }, nil
	case "pos":
		return func(val any, w *quantize.SnapshotWriter) { w.Float32(toFloat32(val)) }, nil
	case "qvel":
		scale := float32(1000)
		if s, ok := fm.options["scale"]; ok {
			if v, err := strconv.ParseFloat(s, 32); err == nil {
				scale = float32(v)
			}
		}
		return func(val any, w *quantize.SnapshotWriter) { w.QVel(toFloat32(val), scale) }, nil
	case "qangle":
		return func(val any, w *quantize.SnapshotWriter) { w.QAngle(toFloat32(val)) }, nil
	case "qnorm":
		return func(val any, w *quantize.SnapshotWriter) { w.QNorm(toFloat32(val)) }, nil
	case "qsize":
		scale := float32(500)
		if s, ok := fm.options["scale"]; ok {
			if v, err := strconv.ParseFloat(s, 32); err == nil {
				scale = float32(v)
			}
		}
		return func(val any, w *quantize.SnapshotWriter) { w.QVel(toFloat32(val), scale) }, nil
	case "u8":
		return func(val any, w *quantize.SnapshotWriter) { w.Uint8(toUint8(val)) }, nil
	case "u16":
		return func(val any, w *quantize.SnapshotWriter) { w.Uint16(toUint16(val)) }, nil
	case "u32":
		return func(val any, w *quantize.SnapshotWriter) { w.Uint32(toUint32(val)) }, nil
	case "i16":
		return func(val any, w *quantize.SnapshotWriter) { w.Int16(toInt16(val)) }, nil
	case "bool":
		return func(val any, w *quantize.SnapshotWriter) { w.Bool(val.(bool)) }, nil
	}
	return nil, fmt.Errorf("no snapshot writer for encoding %q", fm.encoding)
}

// initialWriterFor returns a writer that appends InitialData bytes.
func initialWriterFor(fm fieldMeta) (initialWriterFunc, error) {
	switch fm.encoding {
	case "string":
		return func(val any, buf []byte) []byte {
			s := val.(string)
			b := []byte(s)
			if len(b) > 255 {
				b = b[:255]
			}
			buf = append(buf, uint8(len(b)))
			buf = append(buf, b...)
			return buf
		}, nil
	case "u8":
		return func(val any, buf []byte) []byte {
			return append(buf, toUint8(val))
		}, nil
	case "u16":
		return func(val any, buf []byte) []byte {
			v := toUint16(val)
			return append(buf, byte(v>>8), byte(v))
		}, nil
	case "u32":
		return func(val any, buf []byte) []byte {
			v := toUint32(val)
			return append(buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
		}, nil
	case "bool":
		return func(val any, buf []byte) []byte {
			if val.(bool) {
				return append(buf, 1)
			}
			return append(buf, 0)
		}, nil
	}
	return nil, fmt.Errorf("no initial writer for encoding %q", fm.encoding)
}

func toFloat32(v any) float32 {
	switch x := v.(type) {
	case float32:
		return x
	case float64:
		return float32(x)
	case int:
		return float32(x)
	case uint16:
		return float32(x)
	case int16:
		return float32(x)
	default:
		return 0
	}
}

func toUint8(v any) uint8 {
	switch x := v.(type) {
	case uint8:
		return x
	case int:
		return uint8(x)
	case uint16:
		return uint8(x)
	case uint32:
		return uint8(x)
	case float32:
		return uint8(x)
	default:
		return 0
	}
}

func toUint16(v any) uint16 {
	switch x := v.(type) {
	case uint16:
		return x
	case int:
		return uint16(x)
	case uint32:
		return uint16(x)
	case float32:
		return uint16(x)
	default:
		return 0
	}
}

func toUint32(v any) uint32 {
	switch x := v.(type) {
	case uint32:
		return x
	case int:
		return uint32(x)
	case uint16:
		return uint32(x)
	case float32:
		return uint32(x)
	default:
		return 0
	}
}

func toInt16(v any) int16 {
	switch x := v.(type) {
	case int16:
		return x
	case int:
		return int16(x)
	case float32:
		return int16(x)
	default:
		return 0
	}
}
