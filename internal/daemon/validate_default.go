//go:build !dev

package daemon

import "github.com/dusto/tend/internal/dispatch"

// newValidator returns no validator in release builds; inbound params are not
// schema-validated.
func newValidator() *dispatch.Validator { return nil }
