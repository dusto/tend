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
	// A provider-private update the core mapping does not recognize; the hook maps
	// it to an event of its choosing.
	parse := func(sessionID, method string, _ json.RawMessage) (api.Event, bool) {
		if method == "session/update:provider_metric" {
			return sessionEvent(sessionID, "agent_message_chunk", api.AgentMessageChunk{
				SessionID: api.SessionID(sessionID), Text: "parsed",
			}), true
		}
		return api.Event{}, false
	}
	n := NewNormalizer(c, parse)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "provider_metric",
		"value":         42,
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

func TestUpdateStepsCanShareAKind(t *testing.T) {
	c := &capture{}
	sink := &fakeModeSink{}
	n := NewNormalizer(c, nil)
	n.SetModeSink(sink)

	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "current_mode_update",
		"currentModeId": "think",
	}))

	if sink.calls != 1 || sink.modeID != "think" {
		t.Fatalf("sink = %+v, want one current_mode_update write", sink)
	}
	evs := c.events()
	if len(evs) != 1 {
		t.Fatalf("events = %+v, want only the mode event", evs)
	}
	if evs[0].Type != "agent_mode_updated" {
		t.Fatalf("event type = %q, want agent_mode_updated", evs[0].Type)
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

// fakeConfigSink records the per-axis config-option writes it receives.
type fakeConfigSink struct {
	model        string
	mode         string
	thoughtLevel string
	calls        int
}

func (f *fakeConfigSink) SetSessionMode(_ api.SessionID, id string)  { f.mode = id; f.calls++ }
func (f *fakeConfigSink) SetSessionModel(_ api.SessionID, id string) { f.model = id; f.calls++ }
func (f *fakeConfigSink) SetSessionThoughtLevel(_ api.SessionID, id string) {
	f.thoughtLevel = id
	f.calls++
}

func TestConfigOptionUpdateWritesBackAndEmits(t *testing.T) {
	c := &capture{}
	sink := &fakeConfigSink{}
	n := NewNormalizer(c, nil)
	n.SetConfigSink(sink)

	// A config_option_update carries the full selector set; several axes can move
	// at once. A boolean toggle carries no daemon axis and is ignored.
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "config_option_update",
		"configOptions": []map[string]any{
			{"id": "m", "category": "model", "currentValue": "opus"},
			// claude's alias category folds onto the thought-level axis.
			{"id": "effort", "category": "effort", "currentValue": "high"},
			{"id": "b", "category": "model_config", "currentValue": true},
		},
	}))

	// Each recognized select axis writes back to the registry.
	if sink.model != "opus" || sink.thoughtLevel != "high" || sink.mode != "" {
		t.Errorf("sink = %+v, want model=opus thought=high mode empty", sink)
	}
	if sink.calls != 2 {
		t.Errorf("sink.calls = %d, want 2 (boolean toggle ignored)", sink.calls)
	}
	// And each emits its matching axis event for live subscribers.
	types := map[string]bool{}
	for _, ev := range c.events() {
		types[ev.Type] = true
	}
	if !types["agent_model_updated"] || !types["agent_thought_level_updated"] {
		t.Errorf("emitted types = %v, want model + thought-level updates", types)
	}
	if len(c.events()) != 2 {
		t.Errorf("emitted %d events, want 2", len(c.events()))
	}
}

func TestConfigOptionUpdateWithoutSink(t *testing.T) {
	// No config sink wired: the axis event still publishes and nothing panics.
	c := &capture{}
	n := NewNormalizer(c, nil)
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "config_option_update",
		"configOptions": []map[string]any{
			{"id": "m", "category": "model", "currentValue": "sonnet"},
		},
	}))
	ev := c.last(t)
	if ev.Type != "agent_model_updated" {
		t.Errorf("event type = %q, want agent_model_updated", ev.Type)
	}
}

// TestNormalizeUsageUpdate covers the context-window signal both providers emit
// with the same shape; Claude also attaches a cumulative cost, Codex does not.
func TestNormalizeUsageUpdate(t *testing.T) {
	c := &capture{}
	n := NewNormalizer(c, nil)

	// Claude-shaped: used/size + cost.
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "usage_update", "used": 20348, "size": 200000,
		"cost": map[string]any{"amount": 0.0862785, "currency": "USD"},
	}))
	ev := c.last(t)
	if ev.Type != "agent_context_usage" || ev.StreamID != "session:s1" || ev.Scope != api.ScopeSession {
		t.Fatalf("event = %+v", ev)
	}
	var p api.AgentContextUsage
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "s1" || p.UsedTokens != 20348 || p.WindowTokens != 200000 {
		t.Errorf("payload = %+v", p)
	}
	if p.Cost == nil || p.Cost.Currency != "USD" || p.Cost.Amount == 0 {
		t.Errorf("cost = %+v, want a USD amount", p.Cost)
	}

	// Codex-shaped: bare used/size, no cost.
	notify(n, SessionUpdateMethod, update("s1", map[string]any{
		"sessionUpdate": "usage_update", "used": 22598, "size": 258400,
	}))
	var q api.AgentContextUsage
	if err := json.Unmarshal(c.last(t).Payload, &q); err != nil {
		t.Fatal(err)
	}
	if q.UsedTokens != 22598 || q.WindowTokens != 258400 || q.Cost != nil {
		t.Errorf("codex-shaped payload = %+v, want used/size only", q)
	}
}

// TestPublishTokenUsage covers the authoritative per-turn accounting parsed from
// the session/prompt result, across both providers' field vocabularies.
func TestPublishTokenUsage(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		c := &capture{}
		n := NewNormalizer(c, nil)
		usage := json.RawMessage(`{"inputTokens":11526,"outputTokens":61,"cachedReadTokens":6427,"cachedWriteTokens":2391,"totalTokens":20405}`)
		n.PublishTokenUsage("s1", usage, nil)

		ev := c.last(t)
		if ev.Type != "agent_token_usage" || ev.StreamID != "session:s1" {
			t.Fatalf("event = %+v", ev)
		}
		var p api.AgentTokenUsage
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.InputTokens != 11526 || p.OutputTokens != 61 || p.CachedReadTokens != 6427 || p.CachedWriteTokens != 2391 || p.TotalTokens != 20405 {
			t.Errorf("payload = %+v", p)
		}
		if len(p.ModelUsage) != 0 {
			t.Errorf("claude reports no per-model breakdown; got %+v", p.ModelUsage)
		}
	})

	t.Run("codex with _meta model breakdown", func(t *testing.T) {
		c := &capture{}
		n := NewNormalizer(c, nil)
		usage := json.RawMessage(`{"totalTokens":16000,"inputTokens":5869,"cachedReadTokens":10112,"outputTokens":19,"thoughtTokens":3}`)
		// _meta.quota.model_usage uses the aliased names cachedInputTokens/reasoningOutputTokens.
		meta := json.RawMessage(`{"quota":{"model_usage":[{"model":"gpt-5.5","token_count":{"totalTokens":16000,"inputTokens":5869,"cachedInputTokens":10112,"outputTokens":19,"reasoningOutputTokens":7}}]}}`)
		n.PublishTokenUsage("s1", usage, meta)

		var p api.AgentTokenUsage
		if err := json.Unmarshal(c.last(t).Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.InputTokens != 5869 || p.OutputTokens != 19 || p.CachedReadTokens != 10112 || p.TotalTokens != 16000 {
			t.Errorf("top-level payload = %+v", p)
		}
		if p.ReasoningTokens != 3 { // top-level thoughtTokens maps to reasoning
			t.Errorf("reasoning = %d, want 3 (from thoughtTokens)", p.ReasoningTokens)
		}
		if len(p.ModelUsage) != 1 {
			t.Fatalf("model usage = %+v, want one model", p.ModelUsage)
		}
		m := p.ModelUsage[0]
		if m.Model != "gpt-5.5" || m.CachedReadTokens != 10112 || m.ReasoningTokens != 7 || m.TotalTokens != 16000 {
			t.Errorf("model usage[0] = %+v (aliases cachedInputTokens/reasoningOutputTokens should map)", m)
		}
	})

	t.Run("no usage means no event", func(t *testing.T) {
		c := &capture{}
		n := NewNormalizer(c, nil)
		n.PublishTokenUsage("s1", nil, nil)
		if len(c.events()) != 0 {
			t.Errorf("empty usage should emit nothing; got %+v", c.events())
		}
	})

	// A provider that reports no token accounting (Kiro through 2.4.1 sends null
	// token counts) must not surface an all-zeros event.
	t.Run("null or empty usage means no event", func(t *testing.T) {
		for _, raw := range []string{"null", "{}", `{"inputTokens":0,"outputTokens":0,"totalTokens":0}`} {
			c := &capture{}
			n := NewNormalizer(c, nil)
			n.PublishTokenUsage("s1", json.RawMessage(raw), nil)
			if len(c.events()) != 0 {
				t.Errorf("usage %q should emit nothing; got %+v", raw, c.events())
			}
		}
	})

	// An all-zero top-level usage but a real per-model breakdown still emits (the
	// provider reported something, just not in the aggregate object).
	t.Run("model breakdown alone still emits", func(t *testing.T) {
		c := &capture{}
		n := NewNormalizer(c, nil)
		meta := json.RawMessage(`{"quota":{"model_usage":[{"model":"m1","token_count":{"totalTokens":50}}]}}`)
		n.PublishTokenUsage("s1", json.RawMessage("{}"), meta)
		if len(c.events()) != 1 {
			t.Fatalf("model-only usage should emit one event; got %+v", c.events())
		}
	})
}
