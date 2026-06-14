package api

import "testing"

func TestVersionsSatisfies(t *testing.T) {
	cur := CurrentVersions() // plugin_to_daemon 0.7.0, daemon_to_editor 0.3.0
	cases := []struct {
		name string
		req  Versions
		ok   bool
	}{
		{"equal", cur, true},
		{"empty requires nothing", Versions{}, true},
		{"lower minor required", Versions{PluginToDaemon: "0.6.0"}, true},
		{"equal current", Versions{PluginToDaemon: "0.7.0"}, true},
		{"higher minor required", Versions{PluginToDaemon: "0.8.0"}, false},
		{"higher patch required", Versions{PluginToDaemon: "0.7.1"}, false},
		{"different major", Versions{PluginToDaemon: "1.0.0"}, false},
		{"other set incompatible", Versions{DaemonToEditor: "2.0.0"}, false},
	}
	for _, c := range cases {
		err := cur.Satisfies(c.req)
		if (err == nil) != c.ok {
			t.Errorf("%s: Satisfies err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestVersionsSatisfiesMalformed(t *testing.T) {
	if err := (Versions{PluginToDaemon: "x.y"}).Satisfies(Versions{PluginToDaemon: "0.1.0"}); err == nil {
		t.Fatal("malformed version should error")
	}
}
