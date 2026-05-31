package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

func newValidatedMux(t *testing.T) *Mux {
	t.Helper()
	v, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	m := NewMux(api.PluginToDaemon)
	m.UseValidator(v)
	if err := Handle(m, "workspace.open", func(_ context.Context, p api.WorkspaceOpenParams) (api.WorkspaceInfo, error) {
		return api.WorkspaceInfo{WorkspaceID: api.WorkspaceID(p.Dir)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

func wantInvalidParams(t *testing.T, err error) {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeInvalidParams {
		t.Fatalf("want invalid-params, got %v", err)
	}
}

func TestValidatorAcceptsValidParams(t *testing.T) {
	m := newValidatedMux(t)
	raw, _ := json.Marshal(api.WorkspaceOpenParams{Dir: "/repo"})
	if _, err := m.Handle(context.Background(), &rpc.Request{Method: "workspace.open", Params: raw}); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
}

func TestValidatorRejectsMissingRequired(t *testing.T) {
	m := newValidatedMux(t)
	_, err := m.Handle(context.Background(), &rpc.Request{Method: "workspace.open", Params: json.RawMessage(`{}`)})
	wantInvalidParams(t, err) // "dir" is required
}

func TestValidatorRejectsUnknownField(t *testing.T) {
	m := newValidatedMux(t)
	_, err := m.Handle(context.Background(), &rpc.Request{Method: "workspace.open", Params: json.RawMessage(`{"dir":"/x","bogus":true}`)})
	wantInvalidParams(t, err) // additionalProperties: false
}

func TestNoValidatorSkipsValidation(t *testing.T) {
	m := NewMux(api.PluginToDaemon)
	if err := Handle(m, "workspace.open", func(_ context.Context, _ api.WorkspaceOpenParams) (api.WorkspaceInfo, error) {
		return api.WorkspaceInfo{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	// Without a validator, an unknown field is ignored (Go unmarshal drops it).
	if _, err := m.Handle(context.Background(), &rpc.Request{Method: "workspace.open", Params: json.RawMessage(`{"dir":"/x","bogus":true}`)}); err != nil {
		t.Fatalf("no-validator path should not reject: %v", err)
	}
}
