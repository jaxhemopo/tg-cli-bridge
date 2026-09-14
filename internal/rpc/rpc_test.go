package rpc

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// We can't test against a real agent CLI in CI, but we can validate the
// command-shape using /bin/sh -c as a stand-in for the "agent."

func TestRun_PassesPromptToCommand(t *testing.T) {
	// Use printf as the "agent" — it'll echo whatever args we hand it.
	// The bridge will tack on --prompt "<text>"; printf doesn't care about
	// flag names, so we get back: "%s %s\n--prompt the-prompt\n".
	res := Run(context.Background(), Options{
		LaunchCommand: "printf %s\\n",
		PromptFlag:    "--prompt",
		Prompt:        "hello world",
		Timeout:       5 * time.Second,
	})
	if res.Err != nil {
		t.Fatalf("Run: %v (stderr=%q)", res.Err, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hello world") {
		t.Errorf("expected prompt in stdout, got: %q", res.Stdout)
	}
}

func TestRun_RespectsResumeFlag(t *testing.T) {
	// Stand-in "agent" is `printf` again — we check ResumeArgs are present.
	res := Run(context.Background(), Options{
		LaunchCommand: "printf %s\\n",
		PromptFlag:    "--prompt",
		ResumeArgs:    []string{"--resume", "latest"},
		Resume:        true,
		Prompt:        "continue please",
		Timeout:       5 * time.Second,
	})
	if res.Err != nil {
		t.Fatalf("Run: %v", res.Err)
	}
	if !strings.Contains(res.Stdout, "--resume") {
		t.Errorf("expected --resume in stdout, got: %q", res.Stdout)
	}
}

func TestCommandArgs_AgentShapes(t *testing.T) {
	tests := []struct {
		name       string
		opts       Options
		wantBinary string
		wantArgs   []string
	}{
		{
			name: "AGY new session",
			opts: Options{
				LaunchCommand: "agy --dangerously-skip-permissions",
				PromptFlag:    "--print",
				Prompt:        "hello world",
			},
			wantBinary: "agy",
			wantArgs:   []string{"--dangerously-skip-permissions", "--print", "hello world"},
		},
		{
			name: "AGY resumed session",
			opts: Options{
				LaunchCommand: "agy --dangerously-skip-permissions",
				PromptFlag:    "--print",
				ResumeArgs:    []string{"--continue"},
				Resume:        true,
				Prompt:        "hello world",
			},
			wantBinary: "agy",
			wantArgs:   []string{"--dangerously-skip-permissions", "--continue", "--print", "hello world"},
		},
		{
			name: "Codex new session with positional prompt",
			opts: Options{
				LaunchCommand: "codex exec --sandbox workspace-write",
				PromptFlag:    "--",
				Prompt:        "hello world",
			},
			wantBinary: "codex",
			wantArgs:   []string{"exec", "--sandbox", "workspace-write", "--", "hello world"},
		},
		{
			name: "Codex resumed session",
			opts: Options{
				LaunchCommand: "codex exec --sandbox workspace-write",
				PromptFlag:    "--",
				ResumeArgs:    []string{"resume", "--last"},
				Resume:        true,
				Prompt:        "hello world",
			},
			wantBinary: "codex",
			wantArgs:   []string{"exec", "--sandbox", "workspace-write", "resume", "--last", "--", "hello world"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBinary, gotArgs, err := commandArgs(tt.opts)
			if err != nil {
				t.Fatalf("commandArgs: %v", err)
			}
			if gotBinary != tt.wantBinary {
				t.Errorf("binary = %q, want %q", gotBinary, tt.wantBinary)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestRun_RejectsEmptyLaunchCommand(t *testing.T) {
	res := Run(context.Background(), Options{Prompt: "x", Timeout: time.Second})
	if res.Err == nil {
		t.Fatal("expected error for empty launch_command")
	}
}

func TestRun_HonoursTimeout(t *testing.T) {
	// A tiny stand-in agent ignores its argv and runs longer than our timeout.
	agent := filepath.Join(t.TempDir(), "slow-agent")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\nexec sleep 2\n"), 0o700); err != nil {
		t.Fatalf("write slow agent: %v", err)
	}
	start := time.Now()
	res := Run(context.Background(), Options{
		LaunchCommand: agent,
		Prompt:        "ignored",
		Timeout:       200 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if res.Err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Run blocked %v; should have aborted near 200ms", elapsed)
	}
}
