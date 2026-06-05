package patch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/dusto/tend/api"
)

// ErrConflict reports that a base is no longer current: the content a patch was
// computed against has changed, so applying it would overwrite work done since.
var ErrConflict = errors.New("patch: base no longer current")

// ContentHash returns the canonical content-base hash for b (SHA-256, hex). It
// is the single source for both the base a read reports and the base an apply
// re-verifies, so the two can never diverge.
func ContentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// VerifyContentHash returns ErrConflict if b's content hash does not match want.
// want must be a content hash; the changedtick form of a base is verified
// against the editor at apply time, not here.
func VerifyContentHash(b []byte, want string) error {
	if ContentHash(b) != want {
		return ErrConflict
	}
	return nil
}

// VerifyDiskBase checks a disk file's current bytes against a content-hash base.
// It returns ErrConflict on mismatch, and an error if base is not a content-hash
// base (an open-buffer changedtick base cannot be verified against disk).
func VerifyDiskBase(content []byte, base api.FileBase) error {
	if base.ContentHash == "" {
		return errors.New("patch: disk base check requires a content hash")
	}
	return VerifyContentHash(content, base.ContentHash)
}
