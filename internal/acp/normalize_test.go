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

func TestNormalizePlan(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "plan",
		"entries": []map[string]any{
			{"content": "read the spec", "priority": "high", "status": "completed"},
			{"content": "write the code", "priority": "medium", "status": "in_progress"},
			{"content": "add tests", "status": "pending"},
		},
	}))

	ev := c.last(t)
	if ev.Type != "agent_plan" || ev.StreamID != "session:s1" || ev.Scope != api.ScopeSession {
		t.Fatalf("event = %+v", ev)
	}
	var p api.AgentPlan
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "s1" || len(p.Entries) != 3 {
		t.Fatalf("payload = %+v", p)
	}
	if p.Entries[0] != (api.PlanEntry{Content: "read the spec", Priority: "high", Status: "completed"}) {
		t.Errorf("entry 0 = %+v", p.Entries[0])
	}
	// priority is optional in the ACP entry; absence passes through as empty.
	if p.Entries[2] != (api.PlanEntry{Content: "add tests", Status: "pending"}) {
		t.Errorf("entry 2 = %+v", p.Entries[2])
	}
}

func TestNormalizeEmptyPlan(t *testing.T) {
	// An empty plan (the agent cleared its todos) maps to a plan event with no
	// entries, not a provider_notification.
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{"sessionUpdate": "plan"}))

	ev := c.last(t)
	if ev.Type != "agent_plan" {
		t.Fatalf("event type = %q, want agent_plan", ev.Type)
	}
	var p api.AgentPlan
	_ = json.Unmarshal(ev.Payload, &p)
	if len(p.Entries) != 0 {
		t.Errorf("entries = %+v, want empty", p.Entries)
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

// fakeCommandSink records the commands handed to it.
type fakeCommandSink struct {
	sessionID api.SessionID
	commands  []api.SlashCommand
	calls     int
}

func (f *fakeCommandSink) SetSessionCommands(id api.SessionID, cmds []api.SlashCommand) {
	f.sessionID = id
	f.commands = cmds
	f.calls++
}

func TestAvailableCommandsRoutedToSink(t *testing.T) {
	c := &capture{}
	sink := &fakeCommandSink{}
	n := NewNormalizer(c, nil)
	n.SetCommandSink(sink)

	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "available_commands_update",
		"availableCommands": []map[string]any{
			{"name": "review", "description": "review the diff", "input": map[string]any{"hint": "<path>"}},
			{"name": "compact"},
		},
	}))

	// The commands go to the aggregator (which owns the merged event); the
	// normalizer does not also emit a provider_notification for them.
	if sink.calls != 1 || sink.sessionID != "s1" || len(sink.commands) != 2 {
		t.Fatalf("sink = %+v, want one call for s1 with 2 commands", sink)
	}
	got := sink.commands[0]
	if got.Name != "review" || got.Origin != api.SlashOriginProvider || got.ArgHint != "<path>" || got.Description != "review the diff" {
		t.Errorf("command 0 = %+v", got)
	}
	if sink.commands[1].Name != "compact" || sink.commands[1].ArgHint != "" {
		t.Errorf("command 1 = %+v, want compact with no hint", sink.commands[1])
	}
	if evs := c.events(); len(evs) != 0 {
		t.Errorf("normalizer emitted %v, want nothing (the sink owns the event)", evs)
	}
}

func TestAvailableCommandsWithoutSinkPreserved(t *testing.T) {
	// No command sink wired: the update is still preserved as a provider_notification
	// so nothing the agent advertised is silently dropped.
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate":     "available_commands_update",
		"availableCommands": []map[string]any{{"name": "review"}},
	}))

	ev := c.last(t)
	if ev.Type != "provider_notification" {
		t.Fatalf("event type = %q, want provider_notification", ev.Type)
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
