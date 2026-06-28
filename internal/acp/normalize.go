package acp

import (
	"context"
	"encoding/json"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/rpc"
)

// SessionUpdateMethod is the ACP notification that streams a turn's progress.
const SessionUpdateMethod = "session/update"

// Publisher receives normalized TEND events. *events.Store satisfies it.
type Publisher interface {
	Publish(api.Event) (api.Event, error)
}

// MetadataParser is an optional per-provider hook. It is offered notifications
// the core mapping does not recognize and may return a TEND event to emit in
// their place (for example token usage or model metadata). Returning false
// falls back to a verbatim provider_notification.
type MetadataParser func(sessionID, method string, raw json.RawMessage) (api.Event, bool)

// ModeSink records an agent-driven mode change on the authoritative session
// state. The agent can switch modes itself (ACP current_mode_update); the event
// alone is not enough, because session.list reads the registry, so a later list
// or a reconnect would report a stale current mode. *session.Registry satisfies
// this. Optional: nil means mode-change events are published but not recorded.
type ModeSink interface {
	SetSessionMode(sessionID api.SessionID, modeID string)
}

// Normalizer is the inbound handler installed on an ACP process: it converts the
// agent's session/update notifications into TEND events on the session's stream,
// and preserves anything it does not recognize as a provider_notification so no
// agent output is silently dropped. It implements rpc.Handler.
type Normalizer struct {
	pub      Publisher
	parse    MetadataParser // optional
	modeSink ModeSink       // optional
}

// NewNormalizer returns a Normalizer that publishes through pub. parse may be nil.
func NewNormalizer(pub Publisher, parse MetadataParser) *Normalizer {
	return &Normalizer{pub: pub, parse: parse}
}

// SetModeSink wires the authoritative session state that agent-driven mode
// changes are written back to. Set once at daemon wiring, before any ACP process
// is handling notifications.
func (n *Normalizer) SetModeSink(sink ModeSink) {
	n.modeSink = sink
}

// Handle implements rpc.Handler. It processes inbound notifications; inbound
// requests from the agent are not handled here and return method-not-found.
func (n *Normalizer) Handle(_ context.Context, req *rpc.Request) (any, error) {
	if !req.Notification {
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "acp: unhandled agent request: " + req.Method}
	}
	if req.Method == SessionUpdateMethod {
		n.handleUpdate(req.Params)
		return nil, nil
	}
	// A provider-private notification: preserve it, attributed to its session.
	n.preserve(extractSessionID(req.Params), req.Method, req.Params)
	return nil, nil
}

// PublishTurnEnd emits the turn_end event for a session. A turn ends with the
// session/prompt response rather than a notification, so the caller running the
// turn invokes this when the prompt completes.
func (n *Normalizer) PublishTurnEnd(sessionID string) {
	n.publish(sessionEvent(sessionID, "turn_end", api.TurnEnd{SessionID: api.SessionID(sessionID)}))
}

func (n *Normalizer) handleUpdate(params json.RawMessage) {
	var env struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &env); err != nil || env.SessionID == "" {
		return
	}
	var kind struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(env.Update, &kind)

	if ev, ok := mapUpdate(env.SessionID, kind.SessionUpdate, env.Update); ok {
		// An agent-driven mode change must also update the authoritative session
		// state, not just stream the event — session.list reads the registry, so
		// without this a later list or reconnect reports a stale current mode.
		if kind.SessionUpdate == "current_mode_update" && n.modeSink != nil {
			var u struct {
				CurrentModeID string `json:"currentModeId"`
			}
			_ = json.Unmarshal(env.Update, &u)
			n.modeSink.SetSessionMode(api.SessionID(env.SessionID), u.CurrentModeID)
		}
		n.publish(ev)
		return
	}
	method := SessionUpdateMethod + ":" + kind.SessionUpdate
	if n.parse != nil {
		if ev, ok := n.parse(env.SessionID, method, env.Update); ok {
			n.publish(ev)
			return
		}
	}
	n.preserve(env.SessionID, method, env.Update)
}

// mapUpdate translates a recognized ACP session update into a TEND event.
func mapUpdate(sessionID, kind string, update json.RawMessage) (api.Event, bool) {
	switch kind {
	case "agent_message_chunk":
		var u struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(update, &u)
		return sessionEvent(sessionID, "agent_message_chunk", api.AgentMessageChunk{
			SessionID: api.SessionID(sessionID),
			Text:      u.Content.Text,
		}), true
	case "agent_thought_chunk":
		var u struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(update, &u)
		return sessionEvent(sessionID, "agent_thought_chunk", api.AgentThoughtChunk{
			SessionID: api.SessionID(sessionID),
			Text:      u.Content.Text,
		}), true
	case "tool_call":
		var u struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			RawInput   json.RawMessage `json:"rawInput"`
		}
		_ = json.Unmarshal(update, &u)
		return sessionEvent(sessionID, "tool_call", api.ToolCall{
			SessionID:  api.SessionID(sessionID),
			ToolCallID: u.ToolCallID,
			Name:       u.Title,
			RawInput:   u.RawInput,
		}), true
	case "tool_call_update", "tool_call_complete":
		var u struct {
			ToolCallID string `json:"toolCallId"`
			Status     string `json:"status"`
		}
		_ = json.Unmarshal(update, &u)
		if kind == "tool_call_complete" && u.Status == "" {
			u.Status = "completed"
		}
		return sessionEvent(sessionID, "tool_call_update", api.ToolCallUpdate{
			SessionID:  api.SessionID(sessionID),
			ToolCallID: u.ToolCallID,
			Status:     u.Status,
		}), true
	case "plan":
		var u struct {
			Entries []struct {
				Content  string `json:"content"`
				Priority string `json:"priority"`
				Status   string `json:"status"`
			} `json:"entries"`
		}
		_ = json.Unmarshal(update, &u)
		entries := make([]api.PlanEntry, len(u.Entries))
		for i, e := range u.Entries {
			entries[i] = api.PlanEntry{Content: e.Content, Priority: e.Priority, Status: e.Status}
		}
		return sessionEvent(sessionID, "agent_plan", api.AgentPlan{
			SessionID: api.SessionID(sessionID),
			Entries:   entries,
		}), true
	case "current_mode_update":
		var u struct {
			CurrentModeID string `json:"currentModeId"`
		}
		_ = json.Unmarshal(update, &u)
		return sessionEvent(sessionID, "agent_mode_updated", api.AgentModeUpdated{
			SessionID:     api.SessionID(sessionID),
			CurrentModeID: u.CurrentModeID,
		}), true
	}
	return api.Event{}, false
}

// PublishModeUpdated emits agent_mode_updated for a session. The agent's own
// mode changes arrive as current_mode_update notifications (handled in
// mapUpdate); this is for a daemon-driven change (session.set_mode) where the
// new mode is confirmed by the set call's success rather than a notification.
func (n *Normalizer) PublishModeUpdated(sessionID, modeID string) {
	n.publish(sessionEvent(sessionID, "agent_mode_updated", api.AgentModeUpdated{
		SessionID:     api.SessionID(sessionID),
		CurrentModeID: modeID,
	}))
}

// PublishModelUpdated emits agent_model_updated for a session after a
// session.set_model change.
func (n *Normalizer) PublishModelUpdated(sessionID, modelID string) {
	n.publish(sessionEvent(sessionID, "agent_model_updated", api.AgentModelUpdated{
		SessionID:      api.SessionID(sessionID),
		CurrentModelID: modelID,
	}))
}

func (n *Normalizer) preserve(sessionID, method string, raw json.RawMessage) {
	if sessionID == "" {
		return // cannot attribute to a session stream
	}
	n.publish(sessionEvent(sessionID, "provider_notification", api.ProviderNotification{
		SessionID: api.SessionID(sessionID),
		Method:    method,
		Raw:       raw,
	}))
}

func (n *Normalizer) publish(ev api.Event) {
	if n.pub != nil {
		_, _ = n.pub.Publish(ev)
	}
}

func sessionEvent(sessionID, typ string, payload any) api.Event {
	raw, _ := json.Marshal(payload)
	return api.Event{
		StreamID: api.SessionStream(api.SessionID(sessionID)),
		Scope:    api.ScopeSession,
		Type:     typ,
		Payload:  raw,
	}
}

func extractSessionID(params json.RawMessage) string {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.SessionID
}
