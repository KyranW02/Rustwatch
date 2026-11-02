package rustplus

import "testing"

func TestDecodeRaysLiterals(t *testing.T) {
	w, h := 8, 4
	order := SampleOrder(w, h)
	var in [][3]int
	for k := 0; k < w*h; k++ {
		in = append(in, [3]int{k * 30 % 1024, k % 64, k % 8})
	}
	pix := make([]Sample, w*h)
	DecodeRays(&CameraRays{SampleOffset: 0, RayData: EncodeRays(in)}, order, w, h, pix)
	for k, s := range in {
		x, y := int(order[2*k]), int(order[2*k+1])
		got := pix[x+y*w]
		if !got.Set || int(got.Dist) != s[0] || int(got.Align) != s[1] || int(got.Mat) != s[2] {
			t.Fatalf("sample %d at %d,%d: got %+v want %v", k, x, y, got, s)
		}
	}
}

func TestDecodeRaysOpcodes(t *testing.T) {
	w, h := 4, 2
	order := SampleOrder(w, h)
	// literal (t=500, r=20, i=3) then: reuse it (0x00), small delta (0x40), big delta (0x80),
	// short literal (0xC0, material 5)
	t0, r0, i0 := 500, 20, 3
	slot := lookbackIndex(t0, r0, i0)
	data := []byte{255, byte(t0 >> 2), byte((t0&3)<<6 | r0), byte(i0)}
	data = append(data, byte(slot))                                     // same again
	data = append(data, byte(64|slot), byte(17<<3|5))                   // t+2, r+2
	data = append(data, byte(128|slot), 127+40)                         // t+40
	data = append(data, byte(192|5), byte(800>>2), byte((800&3)<<6|10)) // t=800 r=10 i=5
	data = append(data, 0)                                              // padding byte (the loop stops one short of the end)
	pix := make([]Sample, w*h)
	DecodeRays(&CameraRays{SampleOffset: 3, RayData: data}, order, w, h, pix)
	want := [][3]int{{500, 20, 3}, {500, 20, 3}, {502, 22, 3}, {540, 20, 3}, {800, 10, 5}}
	for k, s := range want {
		i := (3 + k) % (w * h)
		x, y := int(order[2*i]), int(order[2*i+1])
		got := pix[x+y*w]
		if int(got.Dist) != s[0] || int(got.Align) != s[1] || int(got.Mat) != s[2] {
			t.Errorf("sample %d: got %+v want %v", k, got, s)
		}
	}
}

func TestRenderFrameSky(t *testing.T) {
	pix := []Sample{{Dist: 1023, Set: true}, {Dist: 100, Align: 63, Mat: 2, Set: true}}
	img := RenderFrame(pix, 2, 1)
	if c := img.RGBAAt(0, 0); c.R != 208 || c.B != 252 {
		t.Errorf("sky %v", c)
	}
	if c := img.RGBAAt(1, 0); c.B < 200 || c.R > 100 {
		t.Errorf("water-ish %v", c)
	}
}
