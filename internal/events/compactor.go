package events

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/summarize"
)

// Compactor turns a range of raw session-turn events into a summary record: it
// reads the range, renders it to a transcript, condenses it with the summarizer,
// and writes a kind=summary record (via Store.Compact) that replaces the range
// on replay. Deciding WHEN and WHICH range to compact is the caller's — the
// trigger lives with the context-window policy (see tend-w1h.11); Compactor is
// the mechanism. Store.CompactableRange bounds the safe range.
type Compactor struct {
	store *Store
	sum   summarize.Summarizer
}

// NewCompactor returns a Compactor that summarizes ranges of store with sum.
func NewCompactor(store *Store, sum summarize.Summarizer) *Compactor {
	return &Compactor{store: store, sum: sum}
}

// Compact summarizes [from, to] of a session stream and records the compaction.
// It reads the raw events in the range, renders a transcript, asks the
// summarizer to condense it (transcript purpose, budget characters — 0 uses the
// summarizer default), and writes the summary via the store, which enforces the
// retention window and rejects overlap. ErrWithinRetention and ErrSummaryOverlap
// pass through unchanged so a caller can treat "nothing to do" distinctly.
func (c *Compactor) Compact(ctx context.Context, streamID api.StreamID, sessionID api.SessionID, from, to uint64, budget int) error {
	recs, _, err := c.store.Read(streamID, from-1, int(to-from)+1)
	if err != nil {
		return err
	}
	res, err := c.sum.Summarize(ctx, summarize.Request{
		Purpose:     summarize.PurposeTranscript,
		Text:        renderTranscript(recs, from, to),
		TargetChars: budget,
	})
	if err != nil {
		return err
	}
	payload, err := json.Marshal(api.ContextSummary{SessionID: sessionID, Text: res.Text})
	if err != nil {
		return err
	}
	return c.store.Compact(streamID, api.ScopeSession, from, to, payload)
}

// RenderTranscript renders a full session read — summary records and raw
// transcript events, in stream order — into plain text for a resume seed. A
// summary record (kind=summary) contributes its already-condensed text, standing
// in for the turns it subsumed; a raw event renders as in a transcript
// (message/thought inline, tool calls and turn boundaries marked). This is the
// whole-history counterpart to the range renderer the Compactor uses: pass it a
// Store.Read result (which already serves summaries in place of compacted
// ranges), and it yields the prior-context text to condense into a seed.
func RenderTranscript(recs []api.Event) string {
	var b strings.Builder
	for _, e := range recs {
		switch e.Kind {
		case api.KindSummary:
			var p api.ContextSummary
			if json.Unmarshal(e.Payload, &p) == nil && p.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
		case api.KindEvent:
			writeTranscriptEvent(&b, e)
		}
	}
	return strings.TrimSpace(b.String())
}

// renderTranscript renders the raw events whose seq is in [from, to] into a plain
// transcript for summarization: agent message and thought text flows inline;
// tool calls and turn boundaries are marked on their own lines. Non-transcript
// events (and any summary record) are skipped.
func renderTranscript(recs []api.Event, from, to uint64) string {
	var b strings.Builder
	for _, e := range recs {
		if e.Kind != api.KindEvent || e.Seq < from || e.Seq > to {
			continue
		}
		writeTranscriptEvent(&b, e)
	}
	return strings.TrimSpace(b.String())
}

// writeTranscriptEvent appends one event's transcript form to b.
func writeTranscriptEvent(b *strings.Builder, e api.Event) {
	switch e.Type {
	case "agent_message_chunk":
		var p api.AgentMessageChunk
		if json.Unmarshal(e.Payload, &p) == nil {
			b.WriteString(p.Text) // streamed fragment: keep inline, no separator
		}
	case "agent_thought_chunk":
		var p api.AgentThoughtChunk
		if json.Unmarshal(e.Payload, &p) == nil {
			b.WriteString(p.Text)
		}
	case "tool_call":
		var p api.ToolCall
		if json.Unmarshal(e.Payload, &p) == nil {
			b.WriteString("\n[tool: " + p.Name + "]\n")
		}
	case "turn_end":
		b.WriteString("\n--- end of turn ---\n")
	}
}
