package audio

import "testing"

func TestRingWrapAndOverflow(t *testing.T) {
	r := newRing(8)
	r.write([]int16{1, 2, 3, 4, 5, 6})
	out := make([]int16, 4)
	if n := r.read(out); n != 4 || out[0] != 1 || out[3] != 4 {
		t.Fatalf("read: %d %v", n, out)
	}
	r.write([]int16{7, 8, 9, 10}) // оборачивается через границу
	out = make([]int16, 8)
	n := r.read(out)
	if n != 6 || out[0] != 5 || out[5] != 10 {
		t.Fatalf("wrap: %d %v", n, out[:n])
	}
	r.write([]int16{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) // больше ёмкости: остаются последние 8
	n = r.read(out)
	if n != 8 || out[0] != 3 || out[7] != 10 {
		t.Fatalf("overflow: %d %v", n, out[:n])
	}
	r.write([]int16{1})
	r.reset()
	if r.available() != 0 {
		t.Fatal("reset")
	}
}
