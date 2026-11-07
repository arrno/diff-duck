package cli

import (
	"io"
	"testing"
)

func TestOnlyAnExplicitAdapterPinsTheProvider(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"default":               {nil, ""},
		"explicit":              {[]string{"--adapter", "scripted:normal"}, "scripted:normal"},
		"explicit default name": {[]string{"--adapter=claude-cli"}, "claude-cli"},
		"other flags only":      {[]string{"--port", "0", "--model", "m"}, ""},
	} {
		var opts serveOptions
		fs := serveFlags(&opts, io.Discard, "serve")
		if _, ok := parseServe(fs, tc.args, &opts); !ok {
			t.Fatalf("%s: did not parse", name)
		}
		if got := pinnedAdapter(fs); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
