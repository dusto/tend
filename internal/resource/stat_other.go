//go:build !linux

package resource

import "errors"

// clockTicksPerSec is unused off Linux but defined so the shared code compiles.
const clockTicksPerSec = 100

// errUnsupported reports that process sampling is not implemented on this
// platform; the Sampler turns it into an absent sample.
var errUnsupported = errors.New("resource: process sampling unsupported on this platform")

// readProc is a no-op off Linux: without a /proc equivalent wired up, sampling
// degrades to unavailable rather than guessing.
func readProc(int) (uint64, int64, error) { return 0, 0, errUnsupported }
