package rustmap

import "errors"

var errLZ4 = errors.New("corrupt LZ4 block")

// lz4Block decompresses one raw LZ4 block into dst (which must be exactly the original size).
func lz4Block(src, dst []byte) error {
	si, di := 0, 0
	for si < len(src) {
		tok := int(src[si])
		si++
		// literals
		n := tok >> 4
		if n == 15 {
			for {
				if si >= len(src) {
					return errLZ4
				}
				b := int(src[si])
				si++
				n += b
				if b != 255 {
					break
				}
			}
		}
		if si+n > len(src) || di+n > len(dst) {
			return errLZ4
		}
		copy(dst[di:], src[si:si+n])
		si += n
		di += n
		if si >= len(src) {
			break // the last sequence has literals only
		}
		// match
		if si+2 > len(src) {
			return errLZ4
		}
		off := int(src[si]) | int(src[si+1])<<8
		si += 2
		if off == 0 || off > di {
			return errLZ4
		}
		m := tok & 15
		if m == 15 {
			for {
				if si >= len(src) {
					return errLZ4
				}
				b := int(src[si])
				si++
				m += b
				if b != 255 {
					break
				}
			}
		}
		m += 4
		if di+m > len(dst) {
			return errLZ4
		}
		// byte by byte: matches may overlap their own output
		for k := 0; k < m; k++ {
			dst[di+k] = dst[di-off+k]
		}
		di += m
	}
	if di != len(dst) {
		return errLZ4
	}
	return nil
}
