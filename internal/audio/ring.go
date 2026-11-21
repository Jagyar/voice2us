package audio

import "sync"

// ring is a lock-protected s16 ring buffer with monotonic read/write
// cursors. Overflow drops the oldest samples (live audio prefers fresh).
type ring struct {
	mu  sync.Mutex
	buf []int16
	r   uint64
	w   uint64
}

func newRing(capacity int) *ring {
	return &ring{buf: make([]int16, capacity)}
}

func (r *ring) write(s []int16) {
	if len(s) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	avail := len(r.buf) - int(r.w-r.r)
	if avail < len(s) {
		r.r += uint64(len(s) - avail) // выбрасываем самое старое
	}
	pos := int(r.w % uint64(len(r.buf)))
	n1 := copy(r.buf[pos:], s)
	if n2 := len(s) - n1; n2 > 0 {
		copy(r.buf, s[n1:])
	}
	r.w += uint64(len(s))
}

func (r *ring) read(out []int16) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	avail := int(r.w - r.r)
	n := avail
	if n > len(out) {
		n = len(out)
	}
	if n == 0 {
		return 0
	}
	pos := int(r.r % uint64(len(r.buf)))
	n1 := copy(out[:n], r.buf[pos:])
	if n1 < n {
		copy(out[n1:n], r.buf)
	}
	r.r += uint64(n)
	return n
}

func (r *ring) available() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int(r.w - r.r)
}
