package pty

// ring is a byte buffer that retains only the most recent max bytes, dropping
// the oldest output once a pane produces more than the cap. It is not safe for
// concurrent use; the Pane guards it with its mutex.
type ring struct {
	max  int
	data []byte
}

func newRing(max int) *ring {
	if max < 1 {
		max = 1
	}
	return &ring{max: max}
}

// write appends b, trimming from the front to stay within the cap.
func (r *ring) write(b []byte) {
	r.data = append(r.data, b...)
	if len(r.data) > r.max {
		// Keep the last max bytes; copy into a fresh slice so the large backing
		// array from append can be released.
		trimmed := make([]byte, r.max)
		copy(trimmed, r.data[len(r.data)-r.max:])
		r.data = trimmed
	}
}

// bytes returns a copy of the retained output.
func (r *ring) bytes() []byte {
	return append([]byte(nil), r.data...)
}
