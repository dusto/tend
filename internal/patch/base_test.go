package patch

import (
	"errors"
	"testing"

	"github.com/dusto/tend/api"
)

func TestContentHashDeterministicAndDistinct(t *testing.T) {
	a1, a2 := ContentHash([]byte("abc")), ContentHash(append([]byte("ab"), 'c'))
	if a1 != a2 {
		t.Error("hash should be deterministic across equal content")
	}
	if a1 == ContentHash([]byte("abd")) {
		t.Error("different content should hash differently")
	}
}

func TestVerifyContentHash(t *testing.T) {
	b := []byte("package main\n")
	h := ContentHash(b)
	if err := VerifyContentHash(b, h); err != nil {
		t.Errorf("matching hash: %v", err)
	}
	if err := VerifyContentHash([]byte("changed"), h); !errors.Is(err, ErrConflict) {
		t.Errorf("stale hash err = %v, want ErrConflict", err)
	}
}

func TestVerifyDiskBase(t *testing.T) {
	b := []byte("x")
	if err := VerifyDiskBase(b, api.FileBase{ContentHash: ContentHash(b)}); err != nil {
		t.Errorf("matching disk base: %v", err)
	}
	if err := VerifyDiskBase([]byte("y"), api.FileBase{ContentHash: ContentHash(b)}); !errors.Is(err, ErrConflict) {
		t.Errorf("stale disk base err = %v, want ErrConflict", err)
	}
	// A changedtick base cannot be verified against disk.
	tick := int64(3)
	if err := VerifyDiskBase(b, api.FileBase{ChangedTick: &tick}); err == nil || errors.Is(err, ErrConflict) {
		t.Errorf("changedtick disk base err = %v, want a non-conflict error", err)
	}
}
