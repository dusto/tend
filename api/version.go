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
	// 0.12.0 added slash.list.
	PluginToDaemonVersion = "0.12.0"
	// 0.2.0 added editor.open + editor.diff; 0.3.0 added editor.diagnostics.
	DaemonToEditorVersion = "0.3.0"
	// 0.2.0 added the agent_thought_chunk session event; 0.3.0 added the
	// agent_mode_updated and agent_model_updated session events; 0.4.0 added the
	// provider_started workspace event; 0.5.0 added the agent_plan session event;
	// 0.6.0 added the slash_commands_updated session event.
	DaemonToClientVersion = "0.6.0"
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
