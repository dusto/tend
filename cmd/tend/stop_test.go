package main

import (
	"context"
	"strings"
	"testing"
)

// `tend stop` with no session id must fail on the argument guard, before any
// attempt to reach the daemon.
func TestStopRequiresSessionID(t *testing.T) {
	err := newApp().Run(context.Background(), []string{"tend", "stop"})
	if err == nil {
		t.Fatal("stop with no session id should error")
	}
	if !strings.Contains(err.Error(), "session id is required") {
		t.Errorf("err = %v, want the session-id-required guard", err)
	}
}
