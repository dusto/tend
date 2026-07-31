package api

import (
	"fmt"
	"strconv"
	"strings"
)

// Contract version of each method set, as MAJOR.MINOR.PATCH. The sets version
// independently: bump a set on a change to that set (major for a breaking change).
const (
	// 0.2.0 added task.*; 0.3.0 added pane.open/list/read/close; 0.4.0 added
	// pane.run; 0.5.0 added file.apply_change_set; 0.6.0 added file.diff;
	// 0.7.0 added lsp.diagnostics; 0.8.0 added session.list/claim; 0.9.0 made
	// agent.start's task optional (task-less sessions) + workspace_id; 0.10.0
	// added session.set_mode/set_model; 0.11.0 added provider.list/start/stop;
	// 0.12.0 added slash.list; 0.13.0 added slash.complete; 0.14.0 added
	// slash.invoke; 0.15.0 added agent.prompt content blocks (attach
	// files/images/audio); 0.16.0 added session.set_thought_level (+ the
	// thought-level fields on SessionInfo, captured from ACP configOptions);
	// 0.17.0 added provider.health; 0.18.0 added SessionInfo.resource_usage
	// (per-session agent CPU/RSS); 0.19.0 added memory.search/get; 0.20.0 added
	// memory.write; 0.21.0 added memory.steering (steering activation model);
	// 0.22.0 added MemoryEntry/MemoryWriteParams.provenance (memory import);
	// 0.23.0 added memory.context (budget-bounded steering+notes digest); 0.24.0
	// added session.resume_seed (daemon-side context reconstruction: summary +
	// recent transcript + memory, condensed into a fresh-session seed); 0.25.0
	// added session.rename (+ SessionInfo.label: user-assigned session labels);
	// 0.26.0 added session.attach/detach (scope a session's prompt + waiting_*
	// delivery to attached clients); 0.27.0 added lsp.symbols/definition/
	// references/hover (editor-fronted code navigation); 0.28.0 added
	// lsp.code_actions (list-only; apply via file.apply_change_set).
	PluginToDaemonVersion = "0.28.0"
	// 0.2.0 added editor.open + editor.diff; 0.3.0 added editor.diagnostics; 0.4.0
	// added editor.symbols/definition/references/hover (LSP navigation reverse
	// methods); 0.5.0 added editor.code_actions.
	DaemonToEditorVersion = "0.5.0"
	// 0.2.0 added the agent_thought_chunk session event; 0.3.0 added the
	// agent_mode_updated and agent_model_updated session events; 0.4.0 added the
	// provider_started workspace event; 0.5.0 added the agent_plan session event;
	// 0.6.0 added the slash_commands_updated session event; 0.7.0 added the
	// agent_thought_level_updated session event; 0.8.0 added the
	// agent_prompt_usage session event; 0.9.0 added the memory_written and
	// memory_searched workspace events; 0.10.0 added the summary session event
	// (context compaction: a condensed record replacing a range of raw turns);
	// 0.11.0 added the user_prompt session event (the user's per-turn prompt text,
	// so a replay/resume sees the human side of the conversation); 0.12.0 added the
	// agent_context_usage and agent_token_usage session events (provider-reported
	// context-window fullness and authoritative per-turn token accounting);
	// 0.13.0 added the session_renamed session event (user-assigned label set or
	// cleared); 1.0.0 moved the approval_requested and approval_resolved events
	// from the owning session's stream (ScopeSession) to the repo-wide workspace
	// stream (ScopeWorkspace) — the approval channel — so a client receives every
	// pending approval live by subscribing to one stream, independent of which
	// sessions it follows (approval.list remains the durable snapshot); 1.1.0
	// added the filesystem_access approval kind + detail (an outside-worktree read
	// is a consent decision, delivered to clients on approval_requested /
	// approval.list) — additive, so a client that does not render it still sees
	// the other approval kinds.
	//
	// This is a BREAKING (major) bump, not a minor one. Removing approvals from
	// the session stream / prompt.raise silently strips live approval delivery
	// from a pre-1.0 client (it connects, but a gated tool only resurfaces on the
	// next reconnect-driven approval.list). A major bump makes such a client
	// reject this daemon at the handshake (versionAtLeast requires a shared major)
	// instead of losing prompts silently — the house rule is reject-at-handshake,
	// not degrade-in-place. A client is compatible again once it subscribes to the
	// workspace stream and pins daemon_to_client >= 1.0.0.
	DaemonToClientVersion = "1.1.0"
)

// Versions reports the contract version of each method set.
type Versions struct {
	PluginToDaemon string `json:"plugin_to_daemon"`
	DaemonToEditor string `json:"daemon_to_editor"`
	DaemonToClient string `json:"daemon_to_client"`
}

// CurrentVersions returns the versions this build implements.
func CurrentVersions() Versions {
	return Versions{
		PluginToDaemon: PluginToDaemonVersion,
		DaemonToEditor: DaemonToEditorVersion,
		DaemonToClient: DaemonToClientVersion,
	}
}

// Satisfies reports whether have meets every version required by req. Empty
// fields in req are not checked. A set is compatible when it shares the major
// version and have >= req. It returns a descriptive error on the first
// incompatible set, or nil when all required sets are satisfied.
func (have Versions) Satisfies(req Versions) error {
	for _, c := range []struct{ name, have, req string }{
		{"plugin_to_daemon", have.PluginToDaemon, req.PluginToDaemon},
		{"daemon_to_editor", have.DaemonToEditor, req.DaemonToEditor},
		{"daemon_to_client", have.DaemonToClient, req.DaemonToClient},
	} {
		if c.req == "" {
			continue
		}
		ok, err := versionAtLeast(c.have, c.req)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("api: %s version %s does not satisfy required %s", c.name, c.have, c.req)
		}
	}
	return nil
}

// versionAtLeast reports whether have >= req and shares its major version.
func versionAtLeast(have, req string) (bool, error) {
	h, err := parseVersion(have)
	if err != nil {
		return false, err
	}
	r, err := parseVersion(req)
	if err != nil {
		return false, err
	}
	if h[0] != r[0] {
		return false, nil // different major: incompatible
	}
	for i := range 3 {
		if h[i] != r[i] {
			return h[i] > r[i], nil
		}
	}
	return true, nil
}

func parseVersion(s string) ([3]int, error) {
	parts := strings.SplitN(s, ".", 3)
	var v [3]int
	if len(parts) != 3 {
		return v, fmt.Errorf("api: malformed version %q", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, fmt.Errorf("api: malformed version %q", s)
		}
		v[i] = n
	}
	return v, nil
}

// HelloParams is the connect-handshake request. Required carries the client's
// minimum acceptable versions per set; empty fields are not checked.
type HelloParams struct {
	Required Versions `json:"required,omitzero"`
}

// HelloResult reports the daemon's contract versions and process epoch.
type HelloResult struct {
	Versions    Versions    `json:"versions"`
	DaemonEpoch DaemonEpoch `json:"daemon_epoch"`
}
