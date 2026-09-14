package bridge

import (
	"testing"

	"github.com/jaxhemopo/tg-cli-bridge/internal/config"
)

func TestSwitchMenuIncludesCodex(t *testing.T) {
	for _, preset := range switchPresets {
		if preset.name == "codex" {
			return
		}
	}
	t.Fatal("switch menu does not include Codex")
}

func TestModelSelectionOnlyAppliesToClaudeWrappers(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"claude --dangerously-skip-permissions", true},
		{"claude-glm --dangerously-skip-permissions", true},
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
