package acp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// capture is a Publisher that records the events it receives.
type capture struct {
	mu sync.Mutex
	ev []api.Event
}

func (c *capture) Publish(ev api.Event) (api.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ev = append(c.ev, ev)
	return ev, nil
}

func (c *capture) events() []api.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]api.Event(nil), c.ev...)
}

func (c *capture) last(t *testing.T) api.Event {
	t.Helper()
	evs := c.events()
	if len(evs) == 0 {
		t.Fatal("no events published")
	}
	return evs[len(evs)-1]
}

// notify delivers a notification to the normalizer as if from the agent.
func notify(n *Normalizer, method string, params any) {
	raw, _ := json.Marshal(params)
	_, _ = n.Handle(context.Background(), &rpc.Request{Method: method, Params: raw, Notification: true})
}

func update(sessionID string, body map[string]any) map[string]any {
	return map[string]any{"sessionId": sessionID, "update": body}
}

func TestNormalizeAgentMessageChunk(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "hello"},
	}))

	ev := c.last(t)
	if ev.Type != "agent_message_chunk" || ev.StreamID != "session:s1" || ev.Scope != api.ScopeSession {
		t.Fatalf("event = %+v", ev)
	}
	var p api.AgentMessageChunk
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "s1" || p.Text != "hello" {
		t.Errorf("payload = %+v", p)
	}
}

func TestNormalizeAgentThoughtChunk(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"content":       map[string]any{"type": "text", "text": "let me think"},
	}))

	ev := c.last(t)
	if ev.Type != "agent_thought_chunk" || ev.StreamID != "session:s1" || ev.Scope != api.ScopeSession {
		t.Fatalf("event = %+v", ev)
	}
	var p api.AgentThoughtChunk
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "s1" || p.Text != "let me think" {
		t.Errorf("payload = %+v", p)
	}
}

func TestNormalizeToolCalls(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)

	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "read_file",
		"rawInput": map[string]any{"path": "x.go"},
	}))
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "in_progress",
	}))
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "tool_call_complete", "toolCallId": "t1",
	}))

	evs := c.events()
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3", len(evs))
	}
	if evs[0].Type != "tool_call" {
		t.Errorf("ev0 type = %q", evs[0].Type)
	}
	var tc api.ToolCall
	_ = json.Unmarshal(evs[0].Payload, &tc)
	if tc.ToolCallID != "t1" || tc.Name != "read_file" {
		t.Errorf("tool_call payload = %+v", tc)
	}
	// tool_call_complete maps to tool_call_update with a completed status.
	if evs[2].Type != "tool_call_update" {
		t.Errorf("ev2 type = %q, want tool_call_update", evs[2].Type)
	}
	var done api.ToolCallUpdate
	_ = json.Unmarshal(evs[2].Payload, &done)
	if done.Status != "completed" {
		t.Errorf("complete status = %q, want completed", done.Status)
	}
}

func TestUnknownUpdatePreservedAsMetadata(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "available_commands_update",
		"availableCommands": []map[string]any{
			{"name": "review"},
		},
	}))

	ev := c.last(t)
	if ev.Type != "provider_notification" || ev.StreamID != "session:s1" {
		t.Fatalf("event = %+v", ev)
	}
	var p api.ProviderNotification
	_ = json.Unmarshal(ev.Payload, &p)
	if p.Method != "session/update:available_commands_update" {
		t.Errorf("method = %q", p.Method)
	}
}

func TestProviderPrivateNotificationPreserved(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, "codex/usage", map[string]any{"sessionId": "s1", "tokens": 42})

	ev := c.last(t)
	if ev.Type != "provider_notification" || ev.StreamID != "session:s1" {
		t.Fatalf("event = %+v", ev)
	}
	var p api.ProviderNotification
	_ = json.Unmarshal(ev.Payload, &p)
	if p.Method != "codex/usage" {
		t.Errorf("method = %q", p.Method)
	}
}

func TestMetadataParserHook(t *testing.T) {
	c := &capture{}
	parse := func(sessionID, method string, _ json.RawMessage) (api.Event, bool) {
		if method == "session/update:usage_update" {
			return sessionEvent(sessionID, "agent_message_chunk", api.AgentMessageChunk{
				SessionID: api.SessionID(sessionID), Text: "parsed",
			}), true
		}
		return api.Event{}, false
	}
	n := NewNormalizer(c, parse)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "usage_update",
		"usage":         map[string]any{"used": 42},
	}))

	ev := c.last(t)
	if ev.Type != "agent_message_chunk" {
		t.Fatalf("parser hook not used: %+v", ev)
	}
}

func TestPublishTurnEnd(t *testing.T) {
	c := &capture{}
	NewNormalizer(c, nil).PublishTurnEnd("s1")
	ev := c.last(t)
	if ev.Type != "turn_end" || ev.StreamID != "session:s1" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestNormalizerIgnoresRequests(t *testing.T) {
	n := NewNormalizer(&capture{}, nil)
	_, err := n.Handle(context.Background(), &rpc.Request{Method: "fs/read_text_file", Notification: false})
	var rerr *rpc.Error
	if !errors.As(err, &rerr) || rerr.Code != rpc.CodeMethodNotFound {
		t.Fatalf("err = %v, want method-not-found", err)
	}
}

// TestNormalizeOverACPStream drives the normalizer through a real ACP transport:
// a fake agent sends session/update notifications to a Client whose inbound
// handler is the normalizer.
func TestNormalizeOverACPStream(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)
	a, b := net.Pipe()
	agent := rpc.NewConn(a, nil)
	client := newClient(b, n)
	t.Cleanup(func() { _ = agent.Close(); _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := agent.Notify(ctx, SessionUpdateMethod, update("s9", map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"text": "streamed"},
	})); err != nil {
		t.Fatalf("agent Notify: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if evs := c.events(); len(evs) == 1 && evs[0].StreamID == "session:s9" {
			var p api.AgentMessageChunk
			_ = json.Unmarshal(evs[0].Payload, &p)
			if p.Text == "streamed" {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("normalized event did not arrive over the ACP stream")
}

func TestNormalizeCurrentModeUpdate(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "current_mode_update",
		"currentModeId": "think",
	}))

	ev := c.last(t)
	if ev.Type != "agent_mode_updated" || ev.StreamID != "session:s1" || ev.Scope != api.ScopeSession {
		t.Fatalf("event = %+v", ev)
	}
	var p api.AgentModeUpdated
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "s1" || p.CurrentModeID != "think" {
		t.Errorf("payload = %+v", p)
	}
}

func TestPublishModeAndModelUpdated(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)

	n.PublishModeUpdated("s1", "think")
	n.PublishModelUpdated("s1", "opus")

	evs := c.events()
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2", len(evs))
	}
	if evs[0].Type != "agent_mode_updated" {
		t.Errorf("event 0 type = %q", evs[0].Type)
	}
	var mode api.AgentModeUpdated
	_ = json.Unmarshal(evs[0].Payload, &mode)
	if mode.SessionID != "s1" || mode.CurrentModeID != "think" {
		t.Errorf("mode payload = %+v", mode)
	}
	if evs[1].Type != "agent_model_updated" {
		t.Errorf("event 1 type = %q", evs[1].Type)
	}
	var model api.AgentModelUpdated
	_ = json.Unmarshal(evs[1].Payload, &model)
	if model.SessionID != "s1" || model.CurrentModelID != "opus" {
		t.Errorf("model payload = %+v", model)
	}
}

// fakeModeSink records SetSessionMode calls.
type fakeModeSink struct {
	sessionID api.SessionID
	modeID    string
	calls     int
}

func (f *fakeModeSink) SetSessionMode(id api.SessionID, modeID string) {
	f.sessionID = id
	f.modeID = modeID
	f.calls++
}

func TestCurrentModeUpdateWritesBackToSink(t *testing.T) {
	c := &capture{}
	sink := &fakeModeSink{}
	n := NewNormalizer(c, nil)
	n.SetModeSink(sink)

	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "current_mode_update",
		"currentModeId": "think",
	}))

	// The authoritative session state is updated, not just the streamed event,
	// so a later session.list reports the new mode.
	if sink.calls != 1 || sink.sessionID != "s1" || sink.modeID != "think" {
		t.Errorf("sink = %+v, want one call for s1/think", sink)
	}
	// The event is still published for live subscribers.
	if ev := c.last(t); ev.Type != "agent_mode_updated" {
		t.Errorf("event type = %q, want agent_mode_updated", ev.Type)
	}
}

func TestCurrentModeUpdateWithoutSink(t *testing.T) {
	// No sink wired: the event still publishes and nothing panics.
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "current_mode_update",
		"currentModeId": "think",
	}))
	if ev := c.last(t); ev.Type != "agent_mode_updated" {
		t.Errorf("event type = %q, want agent_mode_updated", ev.Type)
	}
}
