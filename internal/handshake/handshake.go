// Package handshake implements the connect handshake: the daemon reports its
// contract versions and process epoch, and a client checks the daemon's
// versions against its own minimum before proceeding.
package handshake

import (
	"context"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/rpc"
)

// Method is the connect-handshake method name.
const Method = "daemon.hello"

// Register installs the daemon-side handshake handler on m, reporting the
// current contract versions and the given daemon epoch.
func Register(m *dispatch.Mux, epoch string) error {
	return dispatch.Handle(m, Method, func(_ context.Context, _ api.HelloParams) (api.HelloResult, error) {
		return api.HelloResult{
			Versions:    api.CurrentVersions(),
			DaemonEpoch: api.DaemonEpoch(epoch),
		}, nil
	})
}

// Do runs the client side of the handshake over c: it sends required (the
// client's minimum acceptable versions) and returns the daemon's reply. It
// returns an error if the call fails or the daemon's versions do not satisfy
// required.
func Do(ctx context.Context, c *rpc.Conn, required api.Versions) (api.HelloResult, error) {
	var res api.HelloResult
	if err := c.Call(ctx, Method, api.HelloParams{Required: required}, &res); err != nil {
		return res, err
	}
	return res, res.Versions.Satisfies(required)
}
