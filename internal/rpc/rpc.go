// Package rpc spawns the agent CLI in non-interactive prompt-mode for a
// single message and returns its output as a string.
//
// This is the simple replacement for the tmux+watcher+diff streaming design.
// Each Telegram message becomes one process invocation:
//
//	gemini --yolo --resume latest --prompt "<text>"
//
// The bridge waits for the process to exit and ships the captured stdout as
// a single Telegram message (or a few chunks if it's very long). No live
// streaming, no terminal screen diffing — much smoother to read on a phone.
package rpc

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Result captures everything we want to know about one agent invocation.
type Result struct {
	Stdout string
	Stderr string
	// Err is the process error if any (non-zero exit, timeout, …).
	Err error
}

// Options is everything Run needs to know.
type Options struct {
	// LaunchCommand is the base command, e.g. "gemini --yolo" or "agy".
	// Whitespace splits it into binary + base args.
	LaunchCommand string

	// PromptFlag is the flag the CLI uses to accept the message text,
	// typically "--prompt".
	PromptFlag string

	// ResumeArgs are appended to base args when Resume is true, e.g.
	// ["--resume", "latest"] for Gemini CLI.
	ResumeArgs []string

	// WorkingDir is the cwd the process runs in.
	WorkingDir string

	// PathEnv is the PATH the process inherits so its binary is locatable
	// even when launched from launchd.
	PathEnv string

	// Timeout caps how long we'll wait for the process to finish.
	Timeout time.Duration

	// Resume tells Run whether to attach ResumeArgs (i.e. continue an
	// existing agent session) or start a fresh one.
	Resume bool

	// Prompt is the user's message text to pass to the agent.
	Prompt string

	// OnProgress, if set, is called with each complete line of stdout as it
	// arrives. Lines are raw bytes (may contain ANSI); callers strip as needed.
	OnProgress func(line string)
}

// lineWriter tees stdout to a buffer (for the final Result) and to an
// OnProgress callback one complete line at a time.
type lineWriter struct {
	buf    *bytes.Buffer
	lineBuf bytes.Buffer
	onLine  func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if w.onLine == nil {
		return len(p), nil
	}
	for _, b := range p {
		if b == '\n' {
			w.onLine(w.lineBuf.String())
			w.lineBuf.Reset()
		} else {
			w.lineBuf.WriteByte(b)
		}
	}
	return len(p), nil
}

// flush emits any partial last line (no trailing newline before process exit).
func (w *lineWriter) flush() {
	if w.onLine != nil && w.lineBuf.Len() > 0 {
		w.onLine(w.lineBuf.String())
		w.lineBuf.Reset()
	}
}

// Run invokes the agent CLI once with the given prompt and returns its
// stdout. ctx cancellation aborts the process.
func Run(ctx context.Context, opts Options) Result {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	if opts.PromptFlag == "" {
		opts.PromptFlag = "--prompt"
	}

	parts := strings.Fields(opts.LaunchCommand)
	if len(parts) == 0 {
		return Result{Err: fmt.Errorf("rpc: empty launch_command")}
	}
	binary := parts[0]
	args := append([]string(nil), parts[1:]...)
	if opts.Resume {
		args = append(args, opts.ResumeArgs...)
	}
	args = append(args, opts.PromptFlag, opts.Prompt)

	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, binary, args...)
	if opts.WorkingDir != "" {
		cmd.Dir = opts.WorkingDir
	}
	if opts.PathEnv != "" {
		cmd.Env = append(cmd.Environ(),
			"PATH="+opts.PathEnv,
			// Stop macOS bash deprecation noise; some CLIs print it via a
			// shell helper script.
			"BASH_SILENCE_DEPRECATION_WARNING=1",
		)
	}

	var stdout, stderr bytes.Buffer
	lw := &lineWriter{buf: &stdout, onLine: opts.OnProgress}
	cmd.Stdout = lw
	cmd.Stderr = &stderr
	err := cmd.Run()
	lw.flush()

	return Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Err:    err,
	}
}
