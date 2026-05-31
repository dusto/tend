//go:build dev

package daemon

import (
	"log/slog"

	"github.com/dusto/tend/internal/dispatch"
)

// newValidator returns a params validator that checks inbound params against the
// embedded JSON Schemas. A compile failure is logged and validation is skipped.
func newValidator() *dispatch.Validator {
	v, err := dispatch.NewValidator()
	if err != nil {
		slog.Warn("tendd: schema validator unavailable; skipping param validation", "err", err)
		return nil
	}
	return v
}
