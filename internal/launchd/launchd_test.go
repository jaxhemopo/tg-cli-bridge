package launchd

import (
	"strings"
	"testing"
)

func TestRender_ContainsAllOpts(t *testing.T) {
	opts := Options{
		BinaryPath: "/usr/local/bin/tg-cli-bridge",
		ConfigPath: "/Users/example/.config/tg-cli-bridge/config.toml",
		LogPath:    "/Users/example/.config/tg-cli-bridge/bridge.log",
		PathEnv:    "/opt/homebrew/bin:/usr/bin",
	}
	got := Render(opts)
	for _, want := range []string{
		opts.BinaryPath, opts.ConfigPath, opts.LogPath, opts.PathEnv,
		Label, "KeepAlive", "RunAtLoad", "ThrottleInterval",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered plist missing %q\n--- plist ---\n%s", want, got)
		}
	}
}

func TestRender_XMLEscapesSpecialChars(t *testing.T) {
	opts := Options{
		BinaryPath: "/path/with <special> & chars",
		ConfigPath: "/cfg",
		LogPath:    "/log",
		PathEnv:    "/p",
	}
	got := Render(opts)
	if strings.Contains(got, "<special>") {
		t.Error("rendered plist did not XML-escape '<special>'")
	}
	if !strings.Contains(got, "&lt;special&gt;") {
		t.Error("rendered plist missing escaped '&lt;special&gt;'")
	}
	if !strings.Contains(got, "&amp;") {
		t.Error("rendered plist did not XML-escape '&'")
	}
}

func TestLabel_IsStable(t *testing.T) {
	// If you ever rename this, the bootout step in Install will leave stale
	// LaunchAgents behind for users upgrading from older versions.
	if Label != "com.tg-cli-bridge.agent" {
		t.Errorf("Label changed unexpectedly: %s", Label)
	}
}
