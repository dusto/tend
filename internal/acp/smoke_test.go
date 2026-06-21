package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/events"
)

func TestDefaultProvidersHasCodex(t *testing.T) {
	cfg := DefaultConfig()
	codex, ok := cfg.Provider("codex")
	if !ok {
		t.Fatal("codex provider not present in defaults")
	}
	// The codex ACP adapter is the standalone codex-acp binary (the codex CLI
	// has no acp subcommand).
	if codex.Command != "codex-acp" || len(codex.Args) != 0 {
		t.Errorf("codex def = %+v, want command codex-acp with no args", codex)
	}
	if !codex.Enabled || codex.CwdMode != CwdWorkspace {
		t.Errorf("codex enabled/cwd = %v/%q", codex.Enabled, codex.CwdMode)
	}
	// Selectable: enabled providers include codex.
	found := false
	for _, p := range cfg.EnabledProviders() {
		if p.ID == "codex" {
			found = true
		}
	}
	if !found {
		t.Error("codex not selectable via EnabledProviders")
	}
}

// TestCodexSmoke drives the whole ACP stack against a fake ACP server running as
// a real child process: spawn + initialize (pool), session/new + prompt
// (manager), the agent's streamed session/update notifications normalized onto
// the session event stream, and turn_end — proving the units compose.
func TestCodexSmoke(t *testing.T) {
	log, err := events.OpenLog(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	store := events.NewStore(log)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// Spawn wiring as the daemon would assemble it: SpawnAndInitialize with a
	// Normalizer installed as the process's inbound handler. (The command points
	// at the fake codex; the real Codex def lives in DefaultProviders.)
	var norm *Normalizer
	spawn := func(ctx context.Context, _ Key) (Process, error) {
		norm = NewNormalizer(store, nil)
		cmd := Command{Path: exe, Env: append(os.Environ(), "TEND_FAKE_ACP=codex")}
		cl, _, err := SpawnAndInitialize(ctx, cmd, InitializeParams{ProtocolVersion: ProtocolVersion}, norm)
		return cl, err
	}
	pool := NewPool(spawn, store, Options{Max: 1})
	t.Cleanup(func() { _ = pool.Close() })
	mgr := NewManager(pool)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s, err := mgr.Open(ctx, Key{Workspace: "ws", Provider: "codex"}, NewSessionParams{Cwd: "/repo"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := mgr.Prompt(ctx, s.ID, PromptParams{Prompt: []json.RawMessage{json.RawMessage(`{"type":"text","text":"hi"}`)}})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", res.StopReason)
	}
	// The turn ends with the prompt response; the turn-runner emits turn_end.
	norm.PublishTurnEnd(string(s.ID))

	// All streamed updates were delivered and normalized before Prompt returned
	// (notifications precede the response on the wire), so the session stream now
	// carries the turn in order.
	recs, _, err := store.Read(api.StreamID("session:"+s.ID), 0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var types []string
	for _, r := range recs {
		types = append(types, r.Type)
	}
	want := []string{"agent_message_chunk", "agent_message_chunk", "tool_call", "tool_call_update", "turn_end"}
	if len(types) != len(want) {
		t.Fatalf("event types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event types = %v, want %v", types, want)
		}
	}
}
