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

// Normalizer is the inbound handler installed on an ACP process: it converts the
// agent's session/update notifications into TEND events on the session's stream,
// and preserves anything it does not recognize as a provider_notification so no
// agent output is silently dropped. It implements rpc.Handler.
type Normalizer struct {
	pub   Publisher
	parse MetadataParser // optional
}

// NewNormalizer returns a Normalizer that publishes through pub. parse may be nil.
func NewNormalizer(pub Publisher, parse MetadataParser) *Normalizer {
	return &Normalizer{pub: pub, parse: parse}
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
	}
	return api.Event{}, false
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
