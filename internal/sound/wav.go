package sound

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// EncodeWAV writes mono audio at rate as 16-bit PCM WAV.
func EncodeWAV(x []float32, rate int) []byte {
	var b bytes.Buffer
	n := len(x) * 2
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+n))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(n))
	for _, v := range x {
		f := math.Max(-1, math.Min(1, float64(v)))
		binary.Write(&b, binary.LittleEndian, int16(f*32767))
	}
	return b.Bytes()
}

// DecodeWAV reads a WAV file (8/16/24/32-bit PCM or 32/64-bit float, any channel count and
// rate) and returns mono audio at Rate.
func DecodeWAV(data []byte) ([]float32, error) {
	r := bytes.NewReader(data)
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("not a WAV file — convert it to .wav first (e.g. Audacity → Export → WAV)")
	}
	var ch, bits, tag uint16
	var rate uint32
	for {
		var id [4]byte
		var size uint32
		if _, err := io.ReadFull(r, id[:]); err != nil {
			return nil, errors.New("WAV has no audio data")
		}
		if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
			return nil, err
		}
		if int64(size) > int64(r.Len()) {
			size = uint32(r.Len()) // some tools write a bad size for the last chunk
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return nil, err
		}
		if size%2 == 1 {
			r.ReadByte()
		}
		switch string(id[:]) {
		case "fmt ":
			if len(chunk) < 16 {
				return nil, errors.New("bad WAV header")
			}
			tag = binary.LittleEndian.Uint16(chunk[0:])
			ch = binary.LittleEndian.Uint16(chunk[2:])
			rate = binary.LittleEndian.Uint32(chunk[4:])
			bits = binary.LittleEndian.Uint16(chunk[14:])
			if tag == 0xFFFE && len(chunk) >= 26 { // extensible: the real format is in the sub-format GUID
				tag = binary.LittleEndian.Uint16(chunk[24:])
			}
			if tag != 1 && tag != 3 {
				return nil, errors.New("this WAV is compressed — re-export it as plain PCM WAV")
			}
		case "data":
			if ch == 0 || rate == 0 {
				return nil, errors.New("bad WAV header")
			}
			in, err := samples(chunk, tag, bits)
			if err != nil {
				return nil, err
			}
			if int(rate) == Rate && ch == 1 {
				return in, nil
			}
			rs := NewResampler(int(rate), int(ch))
			out := rs.Write(in)
			out = append(out, rs.Write(make([]float32, int(ch)*rs.half*4))...) // flush the filter tail
			want := int(float64(len(in)/int(ch)) / rs.step)
			if len(out) > want {
				out = out[:want]
			}
			return out, nil
		}
	}
}

func samples(b []byte, tag, bits uint16) ([]float32, error) {
	var out []float32
	switch {
	case tag == 1 && bits == 8:
		for _, v := range b {
			out = append(out, (float32(v)-128)/128)
		}
	case tag == 1 && bits == 16:
		for i := 0; i+1 < len(b); i += 2 {
			out = append(out, float32(int16(binary.LittleEndian.Uint16(b[i:])))/32768)
		}
	case tag == 1 && bits == 24:
		for i := 0; i+2 < len(b); i += 3 {
			v := int32(b[i])<<8 | int32(b[i+1])<<16 | int32(b[i+2])<<24
			out = append(out, float32(v)/2147483648)
		}
	case tag == 1 && bits == 32:
		for i := 0; i+3 < len(b); i += 4 {
			out = append(out, float32(int32(binary.LittleEndian.Uint32(b[i:])))/2147483648)
		}
	case tag == 3 && bits == 32:
		for i := 0; i+3 < len(b); i += 4 {
			out = append(out, math.Float32frombits(binary.LittleEndian.Uint32(b[i:])))
		}
	case tag == 3 && bits == 64:
		for i := 0; i+7 < len(b); i += 8 {
			out = append(out, float32(math.Float64frombits(binary.LittleEndian.Uint64(b[i:]))))
		}
	default:
		return nil, errors.New("unsupported WAV sample format")
	}
	return out, nil
}
