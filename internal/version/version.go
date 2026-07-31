// Package version exposes the build version shared by the tendd and tend
// binaries. The default is "dev"; a release build overrides it at link time via
// -ldflags "-X github.com/dusto/tend/internal/version.Version=<tag>" (set by
// GoReleaser). Keeping it in one package gives both binaries the same value from
// a single ldflags target.
package version

// Version is the build version: a release tag (e.g. "v0.1.0") in a released
// binary, or "dev" in a local build.
var Version = "dev"
