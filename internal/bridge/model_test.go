package bridge

import (
	"testing"

	"github.com/jaxhemopo/tg-cli-bridge/internal/config"
)

func TestSwitchMenuHasOnlySupportedPresets(t *testing.T) {
	want := []string{"agy", "claude", "codex"}
	if len(switchPresets) != len(want) {
		t.Fatalf("switch menu has %d presets, want %d", len(switchPresets), len(want))
	}
	for i, preset := range switchPresets {
		if preset.name != want[i] {
			t.Errorf("switch preset %d = %q, want %q", i, preset.name, want[i])
		}
	}
}

func TestModelMenuHasOnlyClaudeTiers(t *testing.T) {
	want := []string{"sonnet", "opus"}
	if len(modelTiers) != len(want) {
		t.Fatalf("model menu has %d tiers, want %d", len(modelTiers), len(want))
	}
	for i, tier := range modelTiers {
		if tier.name != want[i] {
			t.Errorf("model tier %d = %q, want %q", i, tier.name, want[i])
		}
	}
}

func TestModelSelectionOnlyAppliesToClaude(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"claude --dangerously-skip-permissions", true},
		{"/opt/homebrew/bin/claude --dangerously-skip-permissions", true},
		{"claude-wrapper --dangerously-skip-permissions", false},
		{"agy --dangerously-skip-permissions", false},
		{"codex exec --sandbox workspace-write", false},
	}
	for _, test := range tests {
		b := &Bridge{cfg: &config.Config{LaunchCommand: test.command}}
		if got := b.supportsModelSwitch(); got != test.want {
			t.Errorf("supportsModelSwitch(%q) = %v, want %v", test.command, got, test.want)
		}
	}
}

func TestModelChoiceDefaultsAndUpdates(t *testing.T) {
	b := &Bridge{
		cfg:         &config.Config{LaunchCommand: "claude"},
		modelChoice: make(map[int64]string),
	}
	if got := b.modelForChat(42); got != defaultModelChoice {
		t.Fatalf("default model = %q", got)
	}
	b.setModelForChat(42, "opus")
	if got := b.modelForChat(42); got != "opus" {
		t.Fatalf("updated model = %q", got)
	}
	if got := b.launchCommandForChat(42); got != "claude --model opus" {
		t.Fatalf("launch command = %q", got)
	}
}
