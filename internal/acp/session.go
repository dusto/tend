package acp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/dusto/tend/api"
)

// ACP session method names.
const (
	MethodNewSession      = "session/new"
	MethodPrompt          = "session/prompt"
	MethodCancel          = "session/cancel"
	MethodSetMode         = "session/set_mode"
	MethodSetModel        = "session/set_model"
	MethodSetConfigOption = "session/set_config_option"
)

// ErrNoSession is returned when a prompt names an unknown session.
var ErrNoSession = errors.New("acp: unknown session")

// NewSessionParams is the session/new request. mcpServers is a required field
// of the ACP contract (an empty list is valid, but the key must be present);
// strict agents such as claude-agent-acp reject the request when it is absent.
// So it is not omitempty, and a nil slice is normalized to [] before sending
// (see Manager.Open) rather than marshaling as null.
type NewSessionParams struct {
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

// NewSessionResult is the agent's session/new reply. modes/models are optional:
// a provider that offers no choice omits them. ACP names the model id "modelId"
// (not "id"), so the two are parsed with distinct shapes.
type NewSessionResult struct {
	SessionID string         `json:"sessionId"`
	Modes     *acpModeState  `json:"modes,omitempty"`
	Models    *acpModelState `json:"models,omitempty"`
	// ConfigOptions is the newer unified selector array. Providers advertise
	// model / mode / thought-level choices here (e.g. claude-agent-acp puts its
	// model in a "model" config option while still sending permission modes on
	// the legacy Modes field). A config-backed selector is changed with
	// session/set_config_option (by option id), not session/set_model.
	ConfigOptions []acpConfigOption `json:"configOptions,omitempty"`
}

// Config option categories the daemon maps to its selector axes.
const (
	configCategoryMode         = "mode"
	configCategoryModel        = "model"
	configCategoryThoughtLevel = "thought_level"
	// configCategoryEffort is claude-agent-acp's pre-spec category name for the
	// reasoning/thought-level selector: it advertised "effort" (PR #464) before
	// the ACP spec settled on "thought_level". Newer claude builds send
	// "thought_level", but the alias keeps older ones working. See
	// canonicalCategory.
	configCategoryEffort = "effort"
)

// canonicalCategory folds a provider's config-option category into the daemon's
// selector axis, mapping known vendor aliases (claude's "effort") onto the spec
// category so a single code path captures both. Unknown categories pass through
// unchanged (and are ignored by the axis switches).
func canonicalCategory(category string) string {
	if category == configCategoryEffort {
		return configCategoryThoughtLevel
	}
	return category
}

// acpConfigOption is one ACP SessionConfigOption from session/new: a selector
// with an id, a category, the current value, and the choices.
type acpConfigOption struct {
	ID           string                  `json:"id"`
	Category     string                  `json:"category"`
	CurrentValue string                  `json:"currentValue"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description"`
	Options      []acpConfigOptionChoice `json:"options"`
}

type acpConfigOptionChoice struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// acpModeState mirrors the ACP SessionModeState carried by session/new.
type acpModeState struct {
	CurrentModeID  string    `json:"currentModeId"`
	AvailableModes []acpMode `json:"availableModes"`
}

type acpMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// acpModelState mirrors the ACP SessionModelState carried by session/new.
type acpModelState struct {
	CurrentModelID  string     `json:"currentModelId"`
	AvailableModels []acpModel `json:"availableModels"`
}

type acpModel struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SetModeParams is the session/set_mode request.
type SetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// SetModelParams is the session/set_model request.
type SetModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

// SetConfigOptionParams is the session/set_config_option request: change the
// value of a config option (identified by its id) advertised in configOptions.
// The chosen option value is carried as "value" (the ACP wire field), not
// "configValue".
type SetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

// PromptParams is the session/prompt request. SessionID is set by the manager.
type PromptParams struct {
	SessionID string            `json:"sessionId"`
	Prompt    []json.RawMessage `json:"prompt"`
}

// PromptResult is the agent's session/prompt reply. Usage and Meta are kept raw:
// the provider reports authoritative token usage on the result (codex and claude
// both put a "usage" object here, codex adding a per-model breakdown under
// "_meta"), which the agent service parses into an agent_token_usage event. They
// are empty when the provider reports no usage.
type PromptResult struct {
	StopReason string          `json:"stopReason"`
	Usage      json.RawMessage `json:"usage,omitempty"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
}

// CancelParams is the session/cancel notification.
type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// Session is a task-scoped ACP session pinned to the process it was created on.
// The mode/model fields capture the provider's advertised choices from
// session/new (empty when it offers none), in TEND wire form for the caller to
// record on the session registry.
type Session struct {
	ID     api.SessionID
	key    Key
	client *Client

	CurrentModeID          string
	AvailableModes         []api.SessionMode
	CurrentModelID         string
	AvailableModels        []api.SessionModel
	CurrentThoughtLevelID  string
	AvailableThoughtLevels []api.SessionThoughtLevel

	// Config-option ids for selectors that came from ACP configOptions (empty for
	// a legacy modes/models selector). When set, a change routes to
	// session/set_config_option with this id rather than session/set_mode|model.
	modeConfigID    string
	modelConfigID   string
	thoughtConfigID string
}

// Manager creates task-scoped ACP sessions on pooled processes and routes
// prompts to the process each session lives on. A process may host many
// sessions; turns on a process are serialized through the pool, so only one is
// in flight at a time. A Manager is safe for concurrent use.
type Manager struct {
	pool *Pool

	mu       sync.Mutex
	sessions map[api.SessionID]*Session
}

// NewManager returns a Manager backed by pool.
func NewManager(pool *Pool) *Manager {
	return &Manager{pool: pool, sessions: make(map[api.SessionID]*Session)}
}

// PID returns the OS process id of the process a session is pinned to, and
// whether the session is known and has a live process id. It lets the daemon
// sample per-session resource usage without exposing the process itself.
func (m *Manager) PID(id api.SessionID) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.client == nil {
		return 0, false
	}
	pid := s.client.PID()
	return pid, pid != 0
}

// Open creates a session on a process for key: it takes a process from the pool
// (reusing an idle one or spawning), runs session/new on it, pins the session to
// that process, and registers it so the process is not evicted while it hosts
// sessions.
func (m *Manager) Open(ctx context.Context, key Key, params NewSessionParams) (*Session, error) {
	lease, err := m.pool.Acquire(ctx, key, "")
	if err != nil {
		return nil, err
	}
	client, ok := lease.Process().(*Client)
	if !ok {
		lease.Release()
		return nil, errors.New("acp: pooled process is not an *acp.Client")
	}

	// mcpServers is required and must be an array, not null: send [] when none
	// are configured.
	if params.MCPServers == nil {
		params.MCPServers = []json.RawMessage{}
	}
	var res NewSessionResult
	if err := client.Call(ctx, MethodNewSession, params, &res); err != nil {
		lease.Release()
		return nil, err
	}
	// Mark the process as session-hosting before releasing the turn so eviction
	// cannot reap it in the gap.
	m.pool.AddSession(key, client)
	lease.Release()

	s := &Session{ID: api.SessionID(res.SessionID), key: key, client: client}
	if res.Modes != nil {
		s.CurrentModeID = res.Modes.CurrentModeID
		s.AvailableModes = toAPIModes(res.Modes.AvailableModes)
	}
	if res.Models != nil {
		s.CurrentModelID = res.Models.CurrentModelID
		s.AvailableModels = toAPIModels(res.Models.AvailableModels)
	}
	// configOptions (newer path) supplement/override the legacy fields per axis;
	// a provider may send both (claude: legacy modes + a "model" config option).
	for _, opt := range res.ConfigOptions {
		switch canonicalCategory(opt.Category) {
		case configCategoryModel:
			s.CurrentModelID = opt.CurrentValue
			s.AvailableModels = toAPIModelsFromOptions(opt.Options)
			s.modelConfigID = opt.ID
		case configCategoryMode:
			s.CurrentModeID = opt.CurrentValue
			s.AvailableModes = toAPIModesFromOptions(opt.Options)
			s.modeConfigID = opt.ID
		case configCategoryThoughtLevel:
			s.CurrentThoughtLevelID = opt.CurrentValue
			s.AvailableThoughtLevels = toAPIThoughtLevelsFromOptions(opt.Options)
			s.thoughtConfigID = opt.ID
		}
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return s, nil
}

// toAPIModesFromOptions / toAPIModelsFromOptions / toAPIThoughtLevelsFromOptions
// convert a config option's choices (value/name/description) to the TEND wire
// shape, using each choice's value as the selector id.
func toAPIModesFromOptions(in []acpConfigOptionChoice) []api.SessionMode {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.SessionMode, len(in))
	for i, c := range in {
		out[i] = api.SessionMode{ID: c.Value, Name: c.Name, Description: c.Description}
	}
	return out
}

func toAPIModelsFromOptions(in []acpConfigOptionChoice) []api.SessionModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.SessionModel, len(in))
	for i, c := range in {
		out[i] = api.SessionModel{ID: c.Value, Name: c.Name, Description: c.Description}
	}
	return out
}

func toAPIThoughtLevelsFromOptions(in []acpConfigOptionChoice) []api.SessionThoughtLevel {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.SessionThoughtLevel, len(in))
	for i, c := range in {
		out[i] = api.SessionThoughtLevel{ID: c.Value, Name: c.Name, Description: c.Description}
	}
	return out
}

// toAPIModes converts ACP modes to the TEND wire shape.
func toAPIModes(in []acpMode) []api.SessionMode {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.SessionMode, len(in))
	for i, m := range in {
		out[i] = api.SessionMode{ID: m.ID, Name: m.Name, Description: m.Description}
	}
	return out
}

// toAPIModels converts ACP models to the TEND wire shape (ACP's modelId becomes
// the generic id).
func toAPIModels(in []acpModel) []api.SessionModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.SessionModel, len(in))
	for i, m := range in {
		out[i] = api.SessionModel{ID: m.ModelID, Name: m.Name, Description: m.Description}
	}
	return out
}

// Prompt routes a prompt to its session's process, runs it as one turn (waiting
// if that process is mid-turn for another session), and returns the result.
func (m *Manager) Prompt(ctx context.Context, sessionID api.SessionID, params PromptParams) (PromptResult, error) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s == nil {
		return PromptResult{}, ErrNoSession
	}

	lease, err := m.pool.AcquireOn(ctx, s.key, s.client, sessionID)
	if err != nil {
		return PromptResult{}, err
	}
	defer lease.Release()

	params.SessionID = string(sessionID)
	var res PromptResult
	if err := s.client.Call(ctx, MethodPrompt, params, &res); err != nil {
		return PromptResult{}, err
	}
	return res, nil
}

// SetMode sets a session's active mode on its process. A config-backed mode
// (from configOptions) changes via session/set_config_option; a legacy mode via
// session/set_mode. Like Prompt it serializes through the pool.
func (m *Manager) SetMode(ctx context.Context, sessionID api.SessionID, modeID string) error {
	if cfg := m.configID(sessionID, configCategoryMode); cfg != "" {
		return m.setConfigOption(ctx, sessionID, cfg, modeID)
	}
	return m.callOnSession(ctx, sessionID, MethodSetMode, SetModeParams{SessionID: string(sessionID), ModeID: modeID})
}

// SetModel sets a session's active model. A config-backed model (from
// configOptions, e.g. claude-agent-acp) changes via session/set_config_option;
// a legacy model via session/set_model.
func (m *Manager) SetModel(ctx context.Context, sessionID api.SessionID, modelID string) error {
	if cfg := m.configID(sessionID, configCategoryModel); cfg != "" {
		return m.setConfigOption(ctx, sessionID, cfg, modelID)
	}
	return m.callOnSession(ctx, sessionID, MethodSetModel, SetModelParams{SessionID: string(sessionID), ModelID: modelID})
}

// SetThoughtLevel sets a session's active reasoning/thought level. Thought
// levels come only from configOptions, so this always routes to
// session/set_config_option.
func (m *Manager) SetThoughtLevel(ctx context.Context, sessionID api.SessionID, thoughtLevelID string) error {
	cfg := m.configID(sessionID, configCategoryThoughtLevel)
	if cfg == "" {
		return ErrNoSession
	}
	return m.setConfigOption(ctx, sessionID, cfg, thoughtLevelID)
}

// configID returns the config-option id backing a selector category on a
// session, or "" for a legacy (or unknown) selector.
func (m *Manager) configID(sessionID api.SessionID, category string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionID]
	if s == nil {
		return ""
	}
	switch category {
	case configCategoryMode:
		return s.modeConfigID
	case configCategoryModel:
		return s.modelConfigID
	case configCategoryThoughtLevel:
		return s.thoughtConfigID
	}
	return ""
}

// setConfigOption changes a config option's value on the session's process.
func (m *Manager) setConfigOption(ctx context.Context, sessionID api.SessionID, configID, value string) error {
	return m.callOnSession(ctx, sessionID, MethodSetConfigOption, SetConfigOptionParams{
		SessionID: string(sessionID),
		ConfigID:  configID,
		Value:     value,
	})
}

// callOnSession runs a request on the session's process, acquiring the process
// turn so it does not race an in-flight prompt. The result is discarded; these
// ACP methods reply with null on success.
func (m *Manager) callOnSession(ctx context.Context, sessionID api.SessionID, method string, params any) error {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	lease, err := m.pool.AcquireOn(ctx, s.key, s.client, sessionID)
	if err != nil {
		return err
	}
	defer lease.Release()
	return s.client.Call(ctx, method, params, nil)
}

// Cancel asks the agent to abort the in-flight turn on a session by sending the
// session/cancel notification to its process. It is a best-effort signal: the
// turn ends when the agent acknowledges it by returning from session/prompt.
func (m *Manager) Cancel(ctx context.Context, sessionID api.SessionID) error {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	return s.client.Notify(ctx, MethodCancel, CancelParams{SessionID: string(sessionID)})
}

// Close drops a session, releasing its hold on the hosting process so the
// process becomes eligible for idle eviction once it hosts no sessions. It does
// not stop the process (other sessions may share it).
func (m *Manager) Close(sessionID api.SessionID) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	if s != nil {
		m.pool.RemoveSession(s.key, s.client)
	}
}
