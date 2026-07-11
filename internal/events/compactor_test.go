package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/summarize"
)

// pubEvent publishes one event with a typed payload on a session stream.
func pubEvent(t *testing.T, s *Store, stream api.StreamID, typ string, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := s.Publish(api.Event{StreamID: stream, Scope: api.ScopeSession, Type: typ, Payload: raw}); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func TestRenderTranscript(t *testing.T) {
	sid := api.SessionID("s")
	recs := []api.Event{
		{Kind: api.KindEvent, Seq: 1, Type: "user_prompt", Payload: mustJSON(t, api.UserPrompt{SessionID: sid, Text: "find the bug"})},
		{Kind: api.KindEvent, Seq: 2, Type: "agent_message_chunk", Payload: mustJSON(t, api.AgentMessageChunk{SessionID: sid, Text: "Hello "})},
		{Kind: api.KindEvent, Seq: 3, Type: "agent_message_chunk", Payload: mustJSON(t, api.AgentMessageChunk{SessionID: sid, Text: "world"})},
		{Kind: api.KindEvent, Seq: 4, Type: "tool_call", Payload: mustJSON(t, api.ToolCall{SessionID: sid, Name: "grep"})},
		{Kind: api.KindEvent, Seq: 5, Type: "turn_end", Payload: mustJSON(t, api.TurnEnd{SessionID: sid})},
		{Kind: api.KindEvent, Seq: 6, Type: "approval_requested", Payload: nil}, // non-transcript: skipped
	}
	got := renderTranscript(recs, 1, 6)
	if !strings.Contains(got, "[user] find the bug") {
		t.Errorf("user prompt should render as a human turn: %q", got)
	}
	if !strings.Contains(got, "Hello world") {
		t.Errorf("message chunks should flow inline: %q", got)
	}
	if !strings.Contains(got, "[tool: grep]") || !strings.Contains(got, "end of turn") {
		t.Errorf("missing tool/turn markers: %q", got)
	}
}

func TestRenderTranscriptRespectsRange(t *testing.T) {
	sid := api.SessionID("s")
	recs := []api.Event{
		{Kind: api.KindEvent, Seq: 1, Type: "agent_message_chunk", Payload: mustJSON(t, api.AgentMessageChunk{SessionID: sid, Text: "before"})},
		{Kind: api.KindEvent, Seq: 2, Type: "agent_message_chunk", Payload: mustJSON(t, api.AgentMessageChunk{SessionID: sid, Text: "inside"})},
		{Kind: api.KindEvent, Seq: 3, Type: "agent_message_chunk", Payload: mustJSON(t, api.AgentMessageChunk{SessionID: sid, Text: "after"})},
		{Kind: api.KindSummary, Seq: 2, Type: "summary"}, // a summary record is never transcript
	}
	got := renderTranscript(recs, 2, 2)
	if got != "inside" {
		t.Errorf("range [2,2] = %q, want just \"inside\"", got)
	}
}

func TestRenderTranscriptIncludesSummaries(t *testing.T) {
	sid := api.SessionID("s")
	recs := []api.Event{
		{Kind: api.KindSummary, Seq: 1, Type: "summary", Payload: mustJSON(t, api.ContextSummary{SessionID: sid, Text: "earlier: set up the scaffolding"})},
		{Kind: api.KindEvent, Seq: 4, Type: "agent_message_chunk", Payload: mustJSON(t, api.AgentMessageChunk{SessionID: sid, Text: "now editing "})},
		{Kind: api.KindEvent, Seq: 5, Type: "tool_call", Payload: mustJSON(t, api.ToolCall{SessionID: sid, Name: "edit"})},
	}
	got, condensed := RenderTranscript(recs)
	if !strings.Contains(got, "earlier: set up the scaffolding") {
		t.Errorf("summary text should appear in a full render: %q", got)
	}
	if !strings.Contains(got, "now editing") || !strings.Contains(got, "[tool: edit]") {
		t.Errorf("raw transcript should render alongside the summary: %q", got)
	}
	if !condensed {
		t.Error("a render that folded in a summary record should report condensed=true")
	}
}

func TestRenderTranscriptUserPromptAttachments(t *testing.T) {
	sid := api.SessionID("s")
	cases := []struct {
		name string
		up   api.UserPrompt
		want string
	}{
		{"attachment-only turn is not dropped", api.UserPrompt{SessionID: sid, Attachments: 2}, "[user] (2 attachments)"},
		{"single attachment is singular", api.UserPrompt{SessionID: sid, Attachments: 1}, "[user] (1 attachment)"},
		{"text plus attachments notes the count", api.UserPrompt{SessionID: sid, Text: "look here", Attachments: 1}, "[user] look here (1 attachment)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs := []api.Event{{Kind: api.KindEvent, Seq: 1, Type: "user_prompt", Payload: mustJSON(t, tc.up)}}
			if got := renderTranscript(recs, 1, 1); got != tc.want {
				t.Errorf("render = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderTranscriptRawOnlyNotCondensed(t *testing.T) {
	sid := api.SessionID("s")
	recs := []api.Event{
		{Kind: api.KindEvent, Seq: 1, Type: "agent_message_chunk", Payload: mustJSON(t, api.AgentMessageChunk{SessionID: sid, Text: "just raw turns"})},
	}
	if got, condensed := RenderTranscript(recs); condensed {
		t.Errorf("raw-only render should report condensed=false: %q", got)
	}
}

func TestCompactableRange(t *testing.T) {
	s := newStore(t)
	s.SetRetention(2)
	stream := api.SessionStream("sess")

	if _, _, ok := s.CompactableRange(stream); ok {
		t.Error("empty stream has nothing to compact")
	}
	for range 5 {
		pubEvent(t, s, stream, "turn_end", api.TurnEnd{})
	}
	// tail=5, retention=2 -> compactable [1,3].
	from, to, ok := s.CompactableRange(stream)
	if !ok || from != 1 || to != 3 {
		t.Fatalf("CompactableRange = %d,%d,%v, want 1,3,true", from, to, ok)
	}
}

func TestCompactableRangeAdvancesPastSummary(t *testing.T) {
	s := newStore(t)
	s.SetRetention(2)
	stream := api.SessionStream("sess")
	for range 5 {
		pubEvent(t, s, stream, "turn_end", api.TurnEnd{})
	}
	if err := s.Compact(stream, api.ScopeSession, 1, 3, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// The [1,3] prefix is compacted; the next compactable range starts at 4, but
	// tail-retention = 3 < 4, so nothing remains compactable.
	if _, _, ok := s.CompactableRange(stream); ok {
		t.Error("no compactable range should remain")
	}
	// Grow the stream so 4.. moves beyond retention again.
	for range 3 {
		pubEvent(t, s, stream, "turn_end", api.TurnEnd{})
	}
	from, to, ok := s.CompactableRange(stream) // tail=8, retention=2 -> [4,6]
	if !ok || from != 4 || to != 6 {
		t.Fatalf("CompactableRange = %d,%d,%v, want 4,6,true", from, to, ok)
	}
}

func TestCompactorWritesSummaryRecord(t *testing.T) {
	s := newStore(t)
	s.SetRetention(2)
	stream := api.SessionStream("sess")
	sid := api.SessionID("sess")

	pubEvent(t, s, stream, "agent_message_chunk", api.AgentMessageChunk{SessionID: sid, Text: "planning the change"})
	pubEvent(t, s, stream, "tool_call", api.ToolCall{SessionID: sid, Name: "edit"})
	pubEvent(t, s, stream, "turn_end", api.TurnEnd{SessionID: sid})
	pubEvent(t, s, stream, "agent_message_chunk", api.AgentMessageChunk{SessionID: sid, Text: "recent one"})
	pubEvent(t, s, stream, "agent_message_chunk", api.AgentMessageChunk{SessionID: sid, Text: "recent two"})

	from, to, ok := s.CompactableRange(stream) // tail=5, retention=2 -> [1,3]
	if !ok {
		t.Fatal("expected a compactable range")
	}
	c := NewCompactor(s, summarize.Fallback{TargetChars: 1000})
	if err := c.Compact(context.Background(), stream, sid, from, to, 0); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Replay from the start: the summary stands in for [1,3], then the two recent
	// raw records follow.
	recs, _, err := s.Read(stream, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("replay returned %d records, want 3 (summary + 2 recent)", len(recs))
	}
	sum := recs[0]
	if sum.Kind != api.KindSummary || sum.Type != "summary" {
		t.Fatalf("first record = %+v, want a summary", sum)
	}
	if sum.Summary == nil || sum.Summary.FromSeq != from || sum.Summary.ToSeq != to {
		t.Errorf("summary range = %+v, want [%d,%d]", sum.Summary, from, to)
	}
	var payload api.ContextSummary
	if err := json.Unmarshal(sum.Payload, &payload); err != nil {
		t.Fatalf("summary payload: %v", err)
	}
	if payload.SessionID != sid {
		t.Errorf("summary session = %q, want %q", payload.SessionID, sid)
	}
	if !strings.Contains(payload.Text, "planning the change") || !strings.Contains(payload.Text, "[tool: edit]") {
		t.Errorf("summary text missing the compacted turns: %q", payload.Text)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
