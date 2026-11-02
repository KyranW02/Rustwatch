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

// DecodeWAV reads 16-bit PCM WAV (any channel count/rate) and returns mono audio at Rate.
func DecodeWAV(data []byte) ([]float32, error) {
	r := bytes.NewReader(data)
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("not a WAV file")
	}
	var ch, bits uint16
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
		chunk := make([]byte, size)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return nil, err
		}
		if size%2 == 1 {
			r.ReadByte()
		}
		switch string(id[:]) {
		case "fmt ":
			if len(chunk) < 16 || binary.LittleEndian.Uint16(chunk[0:]) != 1 {
				return nil, errors.New("only 16-bit PCM WAV files are supported")
			}
			ch = binary.LittleEndian.Uint16(chunk[2:])
			rate = binary.LittleEndian.Uint32(chunk[4:])
			bits = binary.LittleEndian.Uint16(chunk[14:])
		case "data":
			if ch == 0 || bits != 16 {
				return nil, errors.New("only 16-bit PCM WAV files are supported")
			}
			in := make([]float32, len(chunk)/2)
			for i := range in {
				in[i] = float32(int16(binary.LittleEndian.Uint16(chunk[i*2:]))) / 32768
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
