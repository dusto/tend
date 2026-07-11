// Package resume implements session.resume_seed: daemon-side context
// reconstruction. Rather than relying on a provider-side session load (opaque,
// unevenly supported, impossible to hand off across providers), the daemon
// rebuilds the context needed to continue a prior session from durable state it
// owns — the event log and workspace memory — and returns a seed to open a fresh
// session with. Because the seed is read from the durable log and composed
// daemon-side, resume works across providers and across a daemon restart.
//
// The seed combines (a) the prior session's rendered history (the w1h.9 summary
// records standing in for compacted turn ranges, plus the recent raw transcript,
// as Store.Read already serves them) and (b) the workspace's applicable memory
// (steering + optional query-matched notes), condensed to a character budget by
// the daemon's summarizer. It is a lossy handoff — a "pick up where I left off"
// briefing — not a verbatim token replay.
package resume

import (
	"context"
	"strings"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/dispatch"
	"github.com/dusto/tend/internal/events"
	"github.com/dusto/tend/internal/rpc"
	"github.com/dusto/tend/internal/summarize"
)

// MethodResumeSeed is the JSON-RPC method this service backs.
const MethodResumeSeed = "session.resume_seed"

// readBatch bounds one page of the prior session's event history when reading
// the whole stream; the reader is paged so a long session does not need one
// giant read.
const readBatch = 512

// Reader reads a stream's durable history honoring compaction (a summary record
// served in place of the raw range it subsumed). *events.Store satisfies it.
type Reader interface {
	Read(streamID api.StreamID, after uint64, limit int) ([]api.Event, uint64, error)
}

// MemoryContext assembles the budget-bounded memory context for a workspace.
// *memory.Service satisfies it (via its Context method, which also backs
// memory.context).
type MemoryContext interface {
	Context(ctx context.Context, p api.MemoryContextParams) (api.MemoryContextResult, error)
}

// Service backs session.resume_seed. It reads a prior session's history through
// reader, assembles memory context through mem, and condenses the seed with sum.
// It is safe for concurrent use (its collaborators are).
type Service struct {
	reader Reader
	mem    MemoryContext
	sum    summarize.Summarizer
}

// NewService returns a Service that reads history through reader, memory through
// mem, and condenses with sum. A nil sum uses the deterministic fallback, so a
// seed is always produced.
func NewService(reader Reader, mem MemoryContext, sum summarize.Summarizer) *Service {
	if sum == nil {
		sum = summarize.Fallback{}
	}
	return &Service{reader: reader, mem: mem, sum: sum}
}

// Register installs session.resume_seed on m, backed by s.
func Register(m *dispatch.Mux, s *Service) error {
	return dispatch.Handle(m, MethodResumeSeed, s.ResumeSeed)
}

// ResumeSeed reconstructs the resume seed for a prior session: it renders that
// session's durable history, assembles the workspace memory context, combines
// them, and condenses the result to the requested budget. The prior session is
// addressed only by id (its stream is read from the log), so it need not still
// be live — the cross-restart path.
func (s *Service) ResumeSeed(ctx context.Context, p api.SessionResumeSeedParams) (api.SessionResumeSeedResult, error) {
	if p.SessionID == "" {
		return api.SessionResumeSeedResult{}, invalidParams("session_id is required")
	}
	if p.WorkspaceID == "" {
		return api.SessionResumeSeedResult{}, invalidParams("workspace_id is required")
	}

	// (a) Prior session history: summary records in place of compacted ranges,
	// plus the recent raw transcript, rendered to plain text. condensed reports
	// whether a prior summary record already contributed lossy context (so the
	// seed is a digest even before the final pass below). Note this is the agent's
	// output transcript — the user's prompt text is not persisted on the stream
	// (see the contract note on SessionResumeSeedParams).
	recs, err := readAll(s.reader, api.SessionStream(p.SessionID))
	if err != nil {
		return api.SessionResumeSeedResult{}, internalErr(err)
	}
	transcript, condensed := events.RenderTranscript(recs)

	// (b) Workspace memory context. It is bounded to the summarizer's own default
	// (Budget 0) rather than the seed budget, so the memory portion cannot consume
	// the whole seed; the final pass below enforces the caller's total budget.
	memRes, err := s.mem.Context(ctx, api.MemoryContextParams{
		WorkspaceID: p.WorkspaceID,
		Path:        p.Path,
		Query:       p.Query,
	})
	if err != nil {
		return api.SessionResumeSeedResult{}, err // already an rpc.Error from memory
	}

	assembled := assembleSeed(memRes.Text, transcript)
	if assembled == "" {
		// Nothing to resume from: no readable history and no memory applies.
		return api.SessionResumeSeedResult{SourceSessionID: p.SessionID}, nil
	}

	res, err := s.sum.Summarize(ctx, summarize.Request{
		Purpose:     summarize.PurposeResumeSeed,
		Text:        assembled,
		TargetChars: p.Budget,
	})
	if err != nil {
		return api.SessionResumeSeedResult{}, internalErr(err)
	}
	return api.SessionResumeSeedResult{
		Text: res.Text,
		// The seed is a digest when any part of it is lossy: the final pass reduced
		// it, the memory portion was already condensed on the way in, or the rendered
		// history already folded in a prior transcript summary.
		Summarized:      res.Summarized || memRes.Summarized || condensed,
		SourceSessionID: p.SessionID,
	}, nil
}

// assembleSeed renders the memory context and prior-session transcript into one
// labeled seed. Memory (standing steering/rules) leads, then the prior session,
// mirroring the steering-first convention of memory.context; either part may be
// empty. The labels orient a fresh session on what is guidance versus history.
func assembleSeed(memory, transcript string) string {
	var parts []string
	if memory != "" {
		parts = append(parts, "## Relevant memory\n\n"+memory)
	}
	if transcript != "" {
		parts = append(parts, "## Prior session\n\n"+transcript)
	}
	return strings.Join(parts, "\n\n")
}

// readAll pages the whole durable history of a stream, advancing the cursor by
// each batch's last record. It stops at the tail (a short or empty batch) and
// guards against a cursor that fails to advance, so a malformed stream cannot
// loop forever.
func readAll(r Reader, stream api.StreamID) ([]api.Event, error) {
	var all []api.Event
	var after uint64
	for {
		recs, _, err := r.Read(stream, after, readBatch)
		if err != nil {
			return nil, err
		}
		if len(recs) == 0 {
			break
		}
		all = append(all, recs...)
		next := recs[len(recs)-1].CursorSeq
		if next <= after { // cursor did not advance: stop rather than spin
			break
		}
		after = next
		if len(recs) < readBatch {
			break
		}
	}
	return all, nil
}

func invalidParams(msg string) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "resume: " + msg}
}

func internalErr(err error) *rpc.Error {
	return &rpc.Error{Code: rpc.CodeInternalError, Message: err.Error()}
}
