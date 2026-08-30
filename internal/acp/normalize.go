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

// CommandSink receives the agent's advertised slash commands (ACP
// available_commands_update). The implementation stores them per session and
// emits the merged slash_commands_updated event; the normalizer only adapts the
// ACP wire format to it. *slash.Service satisfies this. Optional: nil leaves
// available_commands_update preserved as a provider_notification.
type CommandSink interface {
	SetSessionCommands(sessionID api.SessionID, commands []api.SlashCommand)
}

// ConfigSink records an agent-driven config-option change (ACP
// config_option_update) on the authoritative session state, per selector axis.
// Like a mode change, the streamed event alone is not enough: session.list reads
// the registry, so a later list or reconnect would report a stale current value.
// *session.Registry satisfies this. Optional: nil means config-option changes are
// published but not recorded.
type ConfigSink interface {
	SetSessionMode(sessionID api.SessionID, modeID string)
	SetSessionModel(sessionID api.SessionID, modelID string)
	SetSessionThoughtLevel(sessionID api.SessionID, thoughtLevelID string)
}

// UsageSink records the agent's latest reported context-window fullness (ACP
// usage_update) on the authoritative session state, so the compaction trigger
// can read it after a turn. The streamed agent_context_usage event alone is not
// enough: the trigger inspects the session, not the event stream.
// *session.Registry satisfies this. Optional: nil means usage events are
// published but not recorded on the session.
type UsageSink interface {
	SetSessionContextUsage(sessionID api.SessionID, used, window int)
}

// Normalizer is the inbound handler installed on an ACP process: it converts the
// agent's session/update notifications into TEND events on the session's stream,
// and preserves anything it does not recognize as a provider_notification so no
// agent output is silently dropped. It implements rpc.Handler.
type Normalizer struct {
	pub        Publisher
	parse      MetadataParser // optional
	modeSink   ModeSink       // optional
	cmdSink    CommandSink    // optional
	configSink ConfigSink     // optional
	usageSink  UsageSink      // optional
	steps      []updateStep
}

// NewNormalizer returns a Normalizer that publishes through pub. parse may be nil.
func NewNormalizer(pub Publisher, parse MetadataParser) *Normalizer {
	return &Normalizer{pub: pub, parse: parse, steps: defaultUpdateSteps()}
}

// SetModeSink wires the authoritative session state that agent-driven mode
// changes are written back to. Set once at daemon wiring, before any ACP process
// is handling notifications.
func (n *Normalizer) SetModeSink(sink ModeSink) {
	n.modeSink = sink
}

// SetCommandSink wires the slash-command aggregator the agent's advertised
// commands are written to. Set once at daemon wiring, before any ACP process is
// handling notifications.
func (n *Normalizer) SetCommandSink(sink CommandSink) {
	n.cmdSink = sink
}

// SetConfigSink wires the authoritative session state that agent-driven
// config-option changes are written back to. Set once at daemon wiring, before
// any ACP process is handling notifications.
func (n *Normalizer) SetConfigSink(sink ConfigSink) {
	n.configSink = sink
}

// SetUsageSink wires the authoritative session state that agent-reported context
// usage is written back to. Set once at daemon wiring, before any ACP process is
// handling notifications.
func (n *Normalizer) SetUsageSink(sink UsageSink) {
	n.usageSink = sink
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

// PublishPromptUsage emits agent_prompt_usage for a session: the size of the
// prompt input the daemon composed for the turn. The caller running the turn
// invokes it once per turn, as the turn starts (before the provider send).
func (n *Normalizer) PublishPromptUsage(usage api.AgentPromptUsage) {
	n.publish(sessionEvent(string(usage.SessionID), "agent_prompt_usage", usage))
}

// PublishUserPrompt emits user_prompt for a session: the user's prompt content
// for the turn, so a replay/resume sees the human side of the conversation. The
// caller running the turn invokes it once per turn, as the turn starts (before
// the provider send), so the human turn is recorded ahead of the agent's output.
func (n *Normalizer) PublishUserPrompt(prompt api.UserPrompt) {
	n.publish(sessionEvent(string(prompt.SessionID), "user_prompt", prompt))
}

func (n *Normalizer) handleUpdate(params json.RawMessage) {
	update, ok := parseUpdate(params)
	if !ok {
		return
	}
	flow := &updateFlow{}
	for _, step := range n.steps {
		if step.match != nil && !step.match(update) {
			continue
		}
		if step.run(n, flow, update) == updateStop {
			return
		}
	}
}

type sessionUpdate struct {
	SessionID string
	Kind      string
	Raw       json.RawMessage
}

type updateFlow struct {
	handled bool
}

type updateStepResult int

const (
	updateContinue updateStepResult = iota
	updateStop
)

type updateStep struct {
	name  string
	match func(sessionUpdate) bool
	run   func(*Normalizer, *updateFlow, sessionUpdate) updateStepResult
}

func parseUpdate(params json.RawMessage) (sessionUpdate, bool) {
	var env struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &env); err != nil || env.SessionID == "" {
		return sessionUpdate{}, false
	}
	var kind struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(env.Update, &kind)
	return sessionUpdate{SessionID: env.SessionID, Kind: kind.SessionUpdate, Raw: env.Update}, true
}

// handleConfigOptionUpdate processes an ACP config_option_update: for each
// recognized select-category option it records the new current value on the
// registry (via the config sink) and emits the matching axis update event.
// Boolean toggles and unknown categories carry no daemon selector and are
// skipped, so no agent output is lost — those still flow as the raw update is
// otherwise dropped here (a config_option_update is fully consumed).
func (n *Normalizer) handleConfigOptionUpdate(sessionID string, update json.RawMessage) {
	var u struct {
		ConfigOptions []configOptionUpdate `json:"configOptions"`
	}
	if err := json.Unmarshal(update, &u); err != nil {
		return
	}
	sid := api.SessionID(sessionID)
	for _, opt := range u.ConfigOptions {
		n.handleConfigAxisUpdate(sid, opt)
	}
}

type configOptionUpdate struct {
	Category     string          `json:"category"`
	CurrentValue json.RawMessage `json:"currentValue"`
}

func (n *Normalizer) handleConfigAxisUpdate(sessionID api.SessionID, opt configOptionUpdate) {
	axis, ok := configAxisForCategory(opt.Category)
	if !ok {
		return
	}
	// Only single-value selectors map to a daemon axis; a boolean toggle's
	// currentValue is not a string, so it is skipped.
	var value string
	if json.Unmarshal(opt.CurrentValue, &value) != nil {
		return
	}
	if n.configSink != nil {
		axis.write(n.configSink, sessionID, value)
	}
	n.publish(axis.event(sessionID, value))
}

// parseAvailableCommands converts an ACP available_commands_update into the
// daemon's provider-origin slash commands, taking the optional input hint as the
// argument hint.
func parseAvailableCommands(update json.RawMessage) []api.SlashCommand {
	var u struct {
		AvailableCommands []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Input       *struct {
				Hint string `json:"hint"`
			} `json:"input"`
		} `json:"availableCommands"`
	}
	_ = json.Unmarshal(update, &u)
	out := make([]api.SlashCommand, len(u.AvailableCommands))
	for i, c := range u.AvailableCommands {
		hint := ""
		if c.Input != nil {
			hint = c.Input.Hint
		}
		out[i] = api.SlashCommand{
			Name:        c.Name,
			Description: c.Description,
			Origin:      api.SlashOriginProvider,
			ArgHint:     hint,
		}
	}
	return out
}

func defaultUpdateSteps() []updateStep {
	return []updateStep{
		availableCommandsStep(),
		configOptionStep(),
		currentModeSinkStep(),
		agentMessageChunkStep(),
		agentThoughtChunkStep(),
		toolCallStep(),
		toolCallUpdateStep(),
		planStep(),
		currentModeEventStep(),
		contextUsageSinkStep(),
		contextUsageStep(),
		metadataParserStep(),
		preserveUnhandledStep(),
	}
}

func kindIs(kind string) func(sessionUpdate) bool {
	return func(update sessionUpdate) bool {
		return update.Kind == kind
	}
}

func availableCommandsStep() updateStep {
	return updateStep{
		name:  "available_commands",
		match: kindIs("available_commands_update"),
		run: func(n *Normalizer, flow *updateFlow, update sessionUpdate) updateStepResult {
			// The agent's advertised commands are handed to the slash aggregator, which
			// owns storing them and emitting the merged slash_commands_updated event (the
			// daemon commands are not visible here). Without a sink wired, fall through so
			// the update is still preserved as a provider_notification.
			if n.cmdSink == nil {
				return updateContinue
			}
			n.cmdSink.SetSessionCommands(api.SessionID(update.SessionID), parseAvailableCommands(update.Raw))
			flow.handled = true
			return updateStop
		},
	}
}

func configOptionStep() updateStep {
	return updateStep{
		name:  "config_option",
		match: kindIs("config_option_update"),
		run: func(n *Normalizer, flow *updateFlow, update sessionUpdate) updateStepResult {
			// A config_option_update carries the provider's full configOptions set with
			// their current values. It can move several axes at once, so it owns its
			// multi-event emission instead of going through a single mapped event.
			n.handleConfigOptionUpdate(update.SessionID, update.Raw)
			flow.handled = true
			return updateStop
		},
	}
}

func currentModeSinkStep() updateStep {
	return updateStep{
		name:  "current_mode_sink",
		match: kindIs("current_mode_update"),
		run: func(n *Normalizer, _ *updateFlow, update sessionUpdate) updateStepResult {
			// An agent-driven mode change must also update the authoritative session
			// state, not just stream the event — session.list reads the registry, so
			// without this a later list or reconnect reports a stale current mode.
			if n.modeSink == nil {
				return updateContinue
			}
			var u struct {
				CurrentModeID string `json:"currentModeId"`
			}
			_ = json.Unmarshal(update.Raw, &u)
			n.modeSink.SetSessionMode(api.SessionID(update.SessionID), u.CurrentModeID)
			return updateContinue
		},
	}
}

func agentMessageChunkStep() updateStep {
	return publishStep("agent_message_chunk", func(update sessionUpdate) api.Event {
		var u struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(update.Raw, &u)
		return sessionEvent(update.SessionID, "agent_message_chunk", api.AgentMessageChunk{
			SessionID: api.SessionID(update.SessionID),
			Text:      u.Content.Text,
		})
	})
}

func agentThoughtChunkStep() updateStep {
	return publishStep("agent_thought_chunk", func(update sessionUpdate) api.Event {
		var u struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(update.Raw, &u)
		return sessionEvent(update.SessionID, "agent_thought_chunk", api.AgentThoughtChunk{
			SessionID: api.SessionID(update.SessionID),
			Text:      u.Content.Text,
		})
	})
}

func toolCallStep() updateStep {
	return publishStep("tool_call", func(update sessionUpdate) api.Event {
		var u struct {
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			RawInput   json.RawMessage `json:"rawInput"`
		}
		_ = json.Unmarshal(update.Raw, &u)
		return sessionEvent(update.SessionID, "tool_call", api.ToolCall{
			SessionID:  api.SessionID(update.SessionID),
			ToolCallID: u.ToolCallID,
			Name:       u.Title,
			RawInput:   u.RawInput,
		})
	})
}

func toolCallUpdateStep() updateStep {
	return updateStep{
		name: "tool_call_update",
		match: func(update sessionUpdate) bool {
			return update.Kind == "tool_call_update" || update.Kind == "tool_call_complete"
		},
		run: func(n *Normalizer, flow *updateFlow, update sessionUpdate) updateStepResult {
			n.publish(mapToolCallUpdate(update))
			flow.handled = true
			return updateContinue
		},
	}
}

func mapToolCallUpdate(update sessionUpdate) api.Event {
	var u struct {
		ToolCallID string          `json:"toolCallId"`
		Status     string          `json:"status"`
		Title      string          `json:"title"`
		RawInput   json.RawMessage `json:"rawInput"`
	}
	_ = json.Unmarshal(update.Raw, &u)
	if update.Kind == "tool_call_complete" && u.Status == "" {
		u.Status = "completed"
	}
	// A provider opens a tool_call with an empty input and refines it in a later
	// update (often with no status change) once the arguments finish streaming.
	// Carry the refined title and input through so a consumer can show what the
	// tool is actually doing rather than the initial empty input.
	return sessionEvent(update.SessionID, "tool_call_update", api.ToolCallUpdate{
		SessionID:  api.SessionID(update.SessionID),
		ToolCallID: u.ToolCallID,
		Status:     u.Status,
		Name:       u.Title,
		RawInput:   nonEmptyRaw(u.RawInput),
	})
}

// nonEmptyRaw returns raw unless it is absent or a JSON null/empty object, in
// which case it returns nil so the omitempty field is dropped — an update that
// does not refine the input should not overwrite a prior populated input.
func nonEmptyRaw(raw json.RawMessage) json.RawMessage {
	switch string(raw) {
	case "", "null", "{}":
		return nil
	default:
		return raw
	}
}

func planStep() updateStep {
	return publishStep("plan", func(update sessionUpdate) api.Event {
		var u struct {
			Entries []struct {
				Content  string `json:"content"`
				Priority string `json:"priority"`
				Status   string `json:"status"`
			} `json:"entries"`
		}
		_ = json.Unmarshal(update.Raw, &u)
		entries := make([]api.PlanEntry, len(u.Entries))
		for i, e := range u.Entries {
			entries[i] = api.PlanEntry{Content: e.Content, Priority: e.Priority, Status: e.Status}
		}
		return sessionEvent(update.SessionID, "agent_plan", api.AgentPlan{
			SessionID: api.SessionID(update.SessionID),
			Entries:   entries,
		})
	})
}

func currentModeEventStep() updateStep {
	return publishStep("current_mode_update", func(update sessionUpdate) api.Event {
		var u struct {
			CurrentModeID string `json:"currentModeId"`
		}
		_ = json.Unmarshal(update.Raw, &u)
		return sessionEvent(update.SessionID, "agent_mode_updated", api.AgentModeUpdated{
			SessionID:     api.SessionID(update.SessionID),
			CurrentModeID: u.CurrentModeID,
		})
	})
}

// contextUsageSinkStep records the agent's reported context-window fullness on
// the authoritative session state, mirroring currentModeSinkStep: it runs before
// contextUsageStep (which publishes the event) and continues, so a usage_update
// both updates the session and streams its event. The trigger inspects the
// session, not the stream, so without this write-back it could not act.
func contextUsageSinkStep() updateStep {
	return updateStep{
		name:  "usage_sink",
		match: kindIs("usage_update"),
		run: func(n *Normalizer, _ *updateFlow, update sessionUpdate) updateStepResult {
			if n.usageSink == nil {
				return updateContinue
			}
			var u struct {
				Used int `json:"used"`
				Size int `json:"size"`
			}
			_ = json.Unmarshal(update.Raw, &u)
			n.usageSink.SetSessionContextUsage(api.SessionID(update.SessionID), u.Used, u.Size)
			return updateContinue
		},
	}
}

func contextUsageStep() updateStep {
	return publishStep("usage_update", func(update sessionUpdate) api.Event {
		// Context-window fullness. Codex and Claude emit the same {used, size} shape;
		// Claude also attaches a cumulative cost.
		var u struct {
			Used int `json:"used"`
			Size int `json:"size"`
			Cost *struct {
				Amount   float64 `json:"amount"`
				Currency string  `json:"currency"`
			} `json:"cost"`
		}
		_ = json.Unmarshal(update.Raw, &u)
		ctxUsage := api.AgentContextUsage{
			SessionID:    api.SessionID(update.SessionID),
			UsedTokens:   u.Used,
			WindowTokens: u.Size,
		}
		if u.Cost != nil {
			ctxUsage.Cost = &api.UsageCost{Amount: u.Cost.Amount, Currency: u.Cost.Currency}
		}
		return sessionEvent(update.SessionID, "agent_context_usage", ctxUsage)
	})
}

func publishStep(kind string, mapEvent func(sessionUpdate) api.Event) updateStep {
	return updateStep{
		name:  kind,
		match: kindIs(kind),
		run: func(n *Normalizer, flow *updateFlow, update sessionUpdate) updateStepResult {
			n.publish(mapEvent(update))
			flow.handled = true
			return updateContinue
		},
	}
}

func metadataParserStep() updateStep {
	return updateStep{
		name: "metadata_parser",
		run: func(n *Normalizer, flow *updateFlow, update sessionUpdate) updateStepResult {
			if flow.handled || n.parse == nil {
				return updateContinue
			}
			method := SessionUpdateMethod + ":" + update.Kind
			if ev, ok := n.parse(update.SessionID, method, update.Raw); ok {
				n.publish(ev)
				flow.handled = true
				return updateStop
			}
			return updateContinue
		},
	}
}

func preserveUnhandledStep() updateStep {
	return updateStep{
		name: "preserve_unhandled",
		run: func(n *Normalizer, flow *updateFlow, update sessionUpdate) updateStepResult {
			if !flow.handled {
				n.preserve(update.SessionID, SessionUpdateMethod+":"+update.Kind, update.Raw)
			}
			return updateStop
		},
	}
}

// PublishTokenUsage parses the provider's authoritative token usage from a
// session/prompt result — the usage object, plus _meta for a per-model breakdown
// — and emits agent_token_usage. It is a no-op when the provider reported none,
// so a provider without usage accounting simply produces no event. The caller
// running the turn invokes it once, as the turn completes.
func (n *Normalizer) PublishTokenUsage(sessionID string, usage, meta json.RawMessage) {
	ev, ok := parseTokenUsage(sessionID, usage, meta)
	if !ok {
		return
	}
	n.publish(sessionEvent(sessionID, "agent_token_usage", ev))
}

// tokenCounts reads the token fields a provider reports, spanning the top-level
// result usage (inputTokens/outputTokens/cachedReadTokens/cachedWriteTokens/
// thoughtTokens/totalTokens) and the codex _meta.quota per-model token_count,
// which names two of them differently (cachedInputTokens, reasoningOutputTokens).
type tokenCounts struct {
	InputTokens       int `json:"inputTokens"`
	OutputTokens      int `json:"outputTokens"`
	CachedReadTokens  int `json:"cachedReadTokens"`
	CachedWriteTokens int `json:"cachedWriteTokens"`
	ThoughtTokens     int `json:"thoughtTokens"`
	TotalTokens       int `json:"totalTokens"`
	// _meta.quota model_usage aliases.
	CachedInputTokens     int `json:"cachedInputTokens"`
	ReasoningOutputTokens int `json:"reasoningOutputTokens"`
}

func (t tokenCounts) cachedRead() int {
	if t.CachedReadTokens != 0 {
		return t.CachedReadTokens
	}
	return t.CachedInputTokens
}

func (t tokenCounts) reasoning() int {
	if t.ThoughtTokens != 0 {
		return t.ThoughtTokens
	}
	return t.ReasoningOutputTokens
}

// isZero reports whether no token field carries a count. A provider that reports
// no usage — JSON null, {}, or all-null fields (e.g. Kiro through 2.4.1, which
// surfaces context percent + credits but null token counts) — decodes to a zero
// tokenCounts, which must not be surfaced as a real (all-zeros) accounting.
func (t tokenCounts) isZero() bool {
	return t == tokenCounts{}
}

// parseTokenUsage maps a session/prompt result's usage object (and optional _meta
// per-model breakdown) into an AgentTokenUsage. ok is false when the provider
// reported no usage — an empty, null, or all-zero usage object with no per-model
// breakdown — so no all-zeros event is emitted.
func parseTokenUsage(sessionID string, usage, meta json.RawMessage) (api.AgentTokenUsage, bool) {
	if len(usage) == 0 {
		return api.AgentTokenUsage{}, false
	}
	var u tokenCounts
	if json.Unmarshal(usage, &u) != nil {
		return api.AgentTokenUsage{}, false
	}
	// usage:null, usage:{}, or all-null token fields decode to a zero tokenCounts.
	// With no per-model breakdown either, the provider reported nothing to surface.
	models := parseModelUsage(meta)
	if u.isZero() && len(models) == 0 {
		return api.AgentTokenUsage{}, false
	}
	return api.AgentTokenUsage{
		SessionID:         api.SessionID(sessionID),
		InputTokens:       u.InputTokens,
		OutputTokens:      u.OutputTokens,
		CachedReadTokens:  u.cachedRead(),
		CachedWriteTokens: u.CachedWriteTokens,
		ReasoningTokens:   u.reasoning(),
		TotalTokens:       u.TotalTokens,
		ModelUsage:        models,
	}, true
}

// parseModelUsage extracts a per-model token breakdown from a result's _meta,
// following the codex shape _meta.quota.model_usage[].{model, token_count}. It
// returns nil when absent.
func parseModelUsage(meta json.RawMessage) []api.ModelTokenUsage {
	if len(meta) == 0 {
		return nil
	}
	var m struct {
		Quota struct {
			ModelUsage []struct {
				Model      string      `json:"model"`
				TokenCount tokenCounts `json:"token_count"`
			} `json:"model_usage"`
		} `json:"quota"`
	}
	if json.Unmarshal(meta, &m) != nil || len(m.Quota.ModelUsage) == 0 {
		return nil
	}
	out := make([]api.ModelTokenUsage, 0, len(m.Quota.ModelUsage))
	for _, mu := range m.Quota.ModelUsage {
		out = append(out, api.ModelTokenUsage{
			Model:            mu.Model,
			InputTokens:      mu.TokenCount.InputTokens,
			OutputTokens:     mu.TokenCount.OutputTokens,
			CachedReadTokens: mu.TokenCount.cachedRead(),
			ReasoningTokens:  mu.TokenCount.reasoning(),
			TotalTokens:      mu.TokenCount.TotalTokens,
		})
	}
	return out
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

// PublishThoughtLevelUpdated emits agent_thought_level_updated for a session
// after a session.set_thought_level change.
func (n *Normalizer) PublishThoughtLevelUpdated(sessionID, thoughtLevelID string) {
	n.publish(sessionEvent(sessionID, "agent_thought_level_updated", api.AgentThoughtLevelUpdated{
		SessionID:             api.SessionID(sessionID),
		CurrentThoughtLevelID: thoughtLevelID,
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
