package api

import "testing"

// TestVersionsSatisfies checks the compatibility rule (same major, and have >=
// req). The pairs are fixed and deliberately do not reference the current
// version constants, so bumping a version never edits this table — a bump that
// forced test edits here would just train us to rubber-stamp the new number.
func TestVersionsSatisfies(t *testing.T) {
	cases := []struct {
		name      string
		have, req string // compared on the plugin_to_daemon set
		ok        bool
	}{
		{"equal", "0.14.0", "0.14.0", true},
		{"newer minor satisfies older", "0.14.0", "0.8.0", true},
		{"newer patch satisfies older", "0.14.2", "0.14.1", true},
		{"older minor fails", "0.14.0", "0.15.0", false},
		{"older patch fails", "0.14.0", "0.14.1", false},
		{"different major fails", "1.0.0", "0.14.0", false},
	}
	for _, c := range cases {
		err := Versions{PluginToDaemon: c.have}.Satisfies(Versions{PluginToDaemon: c.req})
		if (err == nil) != c.ok {
			t.Errorf("%s: %s satisfies %s? err=%v, want ok=%v", c.name, c.have, c.req, err, c.ok)
		}
	}
}

// TestVersionsSatisfiesInvariants covers the properties that legitimately
// involve the build's own versions — these self-update with the constants
// rather than hardcoding a number.
func TestVersionsSatisfiesInvariants(t *testing.T) {
	cur := CurrentVersions()
	// The build always satisfies exactly what it advertises.
	if err := cur.Satisfies(cur); err != nil {
		t.Errorf("current must satisfy itself: %v", err)
	}
	// An empty requirement is satisfied by anything (unpinned sets are skipped).
	if err := cur.Satisfies(Versions{}); err != nil {
		t.Errorf("empty requirement must be satisfied: %v", err)
	}
	// A pinned set the peer cannot meet (a far-future major it does not share)
	// fails — checked on a set other than plugin_to_daemon to exercise all sets.
	if err := cur.Satisfies(Versions{DaemonToEditor: "99.0.0"}); err == nil {
		t.Error("expected incompatibility for an unreachable daemon_to_editor major")
	}
}

func TestVersionsSatisfiesMalformed(t *testing.T) {
	if err := (Versions{PluginToDaemon: "x.y"}).Satisfies(Versions{PluginToDaemon: "0.1.0"}); err == nil {
		t.Fatal("malformed version should error")
	}
}
