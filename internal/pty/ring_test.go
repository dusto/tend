package pty

import "testing"

func TestRingRetainsRecentBytes(t *testing.T) {
	r := newRing(4)
	r.write([]byte("ab"))
	if string(r.bytes()) != "ab" {
		t.Fatalf("under cap = %q", r.bytes())
	}
	r.write([]byte("cdef")) // total "abcdef", cap 4 -> keep "cdef"
	if got := string(r.bytes()); got != "cdef" {
		t.Fatalf("over cap = %q, want cdef", got)
	}
	r.write([]byte("XYZ")) // "cdefXYZ" -> keep "fXYZ"
	if got := string(r.bytes()); got != "fXYZ" {
		t.Fatalf("after more = %q, want fXYZ", got)
	}
}

func TestRingBytesIsCopy(t *testing.T) {
	r := newRing(8)
	r.write([]byte("data"))
	b := r.bytes()
	b[0] = 'X'
	if string(r.bytes()) != "data" {
		t.Error("bytes() must return a copy")
	}
}
