package rpc

import (
	"context"
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

func TestRun_RejectsEmptyLaunchCommand(t *testing.T) {
	res := Run(context.Background(), Options{Prompt: "x", Timeout: time.Second})
	if res.Err == nil {
		t.Fatal("expected error for empty launch_command")
	}
}

func TestRun_HonoursTimeout(t *testing.T) {
	// `sleep` will run longer than our timeout.
	start := time.Now()
	res := Run(context.Background(), Options{
		LaunchCommand: "sleep",
		PromptFlag:    "",
		Prompt:        "2",
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
