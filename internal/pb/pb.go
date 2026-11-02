// Package pb is a tiny protobuf (proto2 wire format) encoder/decoder.
// It exists so the app can be built with the Go standard library only.
package pb

import (
	"encoding/binary"
	"errors"
	"math"
)

const (
	WireVarint  = 0
	WireFixed64 = 1
	WireBytes   = 2
	WireFixed32 = 5
)

// Writer builds a protobuf message.
type Writer struct{ buf []byte }

func (w *Writer) Bytes() []byte { return w.buf }

func (w *Writer) varint(v uint64) {
	for v >= 0x80 {
		w.buf = append(w.buf, byte(v)|0x80)
		v >>= 7
	}
	w.buf = append(w.buf, byte(v))
}

func (w *Writer) tag(field, wire int) { w.varint(uint64(field)<<3 | uint64(wire)) }

// Uint writes an unsigned varint field (uint32/uint64/enum).
func (w *Writer) Uint(field int, v uint64) { w.tag(field, WireVarint); w.varint(v) }

// Int writes a signed int32/int64 field (two's complement, as protobuf does).
func (w *Writer) Int(field int, v int64) { w.tag(field, WireVarint); w.varint(uint64(v)) }

func (w *Writer) Bool(field int, v bool) {
	w.tag(field, WireVarint)
	if v {
		w.varint(1)
	} else {
		w.varint(0)
	}
}

func (w *Writer) String(field int, s string) { w.BytesField(field, []byte(s)) }

func (w *Writer) BytesField(field int, b []byte) {
	w.tag(field, WireBytes)
	w.varint(uint64(len(b)))
	w.buf = append(w.buf, b...)
}

// Message writes an embedded message (possibly empty).
func (w *Writer) Message(field int, m *Writer) {
	if m == nil {
		w.BytesField(field, nil)
		return
	}
	w.BytesField(field, m.buf)
}

func (w *Writer) Fixed64(field int, v uint64) {
	w.tag(field, WireFixed64)
	w.buf = binary.LittleEndian.AppendUint64(w.buf, v)
}

func (w *Writer) Float(field int, f float32) {
	w.tag(field, WireFixed32)
	w.buf = binary.LittleEndian.AppendUint32(w.buf, math.Float32bits(f))
}

// AppendVarint is exported for framing protocols (MCS) that use raw varints.
func AppendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// Field is one decoded field.
type Field struct {
	Num  int
	Wire int
	V    uint64 // varint / fixed value
	B    []byte // length-delimited payload
}

func (f Field) Uint() uint64    { return f.V }
func (f Field) Int32() int32    { return int32(f.V) }
func (f Field) Int64() int64    { return int64(f.V) }
func (f Field) Bool() bool      { return f.V != 0 }
func (f Field) Str() string     { return string(f.B) }
func (f Field) Float() float32  { return math.Float32frombits(uint32(f.V)) }
func (f Field) Sint32() int32   { return int32((uint32(f.V) >> 1) ^ -(uint32(f.V) & 1)) }
func (f Field) Fixed64() uint64 { return f.V }

var ErrTruncated = errors.New("pb: truncated message")

// ReadVarint decodes a varint and returns value and bytes consumed (0 if incomplete).
func ReadVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

// Parse decodes all fields of a message (no schema needed).
func Parse(b []byte) ([]Field, error) {
	var out []Field
	for len(b) > 0 {
		key, n := ReadVarint(b)
		if n == 0 {
			return out, ErrTruncated
		}
		b = b[n:]
		f := Field{Num: int(key >> 3), Wire: int(key & 7)}
		switch f.Wire {
		case WireVarint:
			v, n := ReadVarint(b)
			if n == 0 {
				return out, ErrTruncated
			}
			f.V, b = v, b[n:]
		case WireFixed64:
			if len(b) < 8 {
				return out, ErrTruncated
			}
			f.V, b = binary.LittleEndian.Uint64(b), b[8:]
		case WireFixed32:
			if len(b) < 4 {
				return out, ErrTruncated
			}
			f.V, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		case WireBytes:
			l, n := ReadVarint(b)
			if n == 0 || uint64(len(b)-n) < l {
				return out, ErrTruncated
			}
			f.B, b = b[n:n+int(l)], b[n+int(l):]
		default:
			return out, errors.New("pb: unsupported wire type")
		}
		out = append(out, f)
	}
	return out, nil
}

// Each calls fn for every field; parse errors are returned.
func Each(b []byte, fn func(f Field)) error {
	fields, err := Parse(b)
	for _, f := range fields {
		fn(f)
	}
	return err
}
