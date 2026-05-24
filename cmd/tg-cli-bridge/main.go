// Command tg-cli-bridge bridges a persistent agentic CLI (Antigravity, Gemini,
// Claude Code, plain shell) to a Telegram chat.
//
// Subcommands:
//
//	tg-cli-bridge init        # interactive config wizard
//	tg-cli-bridge run         # foreground (debug)
//	tg-cli-bridge install     # install + start the LaunchAgent (macOS)
//	tg-cli-bridge uninstall   # remove the LaunchAgent
//	tg-cli-bridge start       # kickstart the LaunchAgent
//	tg-cli-bridge stop        # bootout the LaunchAgent
//	tg-cli-bridge status      # show service + tmux state
//	tg-cli-bridge logs        # tail -f the log file
//	tg-cli-bridge attach      # tmux attach -t <session>
//	tg-cli-bridge example     # print or write an annotated example config
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/jaxhemopo/tg-cli-bridge/internal/bridge"
	"github.com/jaxhemopo/tg-cli-bridge/internal/config"
	"github.com/jaxhemopo/tg-cli-bridge/internal/launchd"
)

const version = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// Global flags handled by the top-level FlagSet.
	root := flag.NewFlagSet("tg-cli-bridge", flag.ContinueOnError)
	root.SetOutput(os.Stderr)
	cfgPath := root.String("config", "", "Path to config.toml (default: "+config.DefaultPath()+")")
	showVersion := root.Bool("version", false, "Print version and exit")
	root.Usage = func() { printUsage(root.Output()) }

	// We need to know where the subcommand starts — but flag.Parse stops at the
	// first non-flag argument anyway, so this is straightforward.
	if err := root.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println("tg-cli-bridge", version)
		return 0
	}
	rest := root.Args()
	if len(rest) == 0 {
		printUsage(os.Stderr)
		return 1
	}
	cmd, subArgs := rest[0], rest[1:]

	log.SetFlags(log.LstdFlags)
	log.SetOutput(os.Stdout)

	switch cmd {
	case "init":
		return cmdInit(*cfgPath, subArgs)
	case "run":
		return cmdRun(*cfgPath)
	case "install":
		return cmdInstall(*cfgPath)
	case "uninstall":
		return cmdUninstall()
	case "start":
		return cmdStart()
	case "stop":
		return cmdStop()
	case "status":
		return cmdStatus(*cfgPath)
	case "logs":
		return cmdLogs(*cfgPath)
	case "attach":
		return cmdAttach(*cfgPath)
	case "example":
		return cmdExample(subArgs)
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		printUsage(os.Stderr)
		return 1
	}
}

// -- subcommands ----------------------------------------------------------

// cliPreset holds the known-good flags for a supported agent CLI.
type cliPreset struct {
	label      string
	launchCmd  string
	promptFlag string   // empty = use default (--prompt)
	resumeArgs []string // nil = use default (["--resume","latest"])
}

var cliPresets = []cliPreset{
	{
		label:     "Gemini CLI  (gemini --yolo)",
		launchCmd: "gemini --yolo",
	},
	{
		label:      "AGY / Antigravity  (agy --dangerously-skip-permissions)",
		launchCmd:  "agy --dangerously-skip-permissions",
		promptFlag: "--print",
		resumeArgs: []string{"--continue"},
	},
	{
		label:      "Claude Code  (claude --dangerously-skip-permissions)",
		launchCmd:  "claude --dangerously-skip-permissions",
		promptFlag: "--print",
		resumeArgs: []string{"--continue"},
	},
	{
		label: "Other / custom",
	},
}

func cmdInit(path string, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "Overwrite an existing config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if path == "" {
		path = config.DefaultPath()
	}
	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "error: %s already exists. Pass --force to overwrite.\n", path)
		return 1
	}

	fmt.Printf("Creating config at %s\n\n", path)
	r := bufio.NewReader(os.Stdin)

	botToken := prompt(r, "Telegram bot token (from @BotFather)", "", true)
	userIDRaw := prompt(r, "Your Telegram user ID (message @userinfobot)", "", true)
	userID, err := strconv.ParseInt(userIDRaw, 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: user ID must be a number")
		return 1
	}

	// CLI selector.
	fmt.Println("\n  Which agent CLI are you bridging?")
	for i, p := range cliPresets {
		fmt.Printf("    %d. %s\n", i+1, p.label)
	}
	var preset cliPreset
	for {
		raw := prompt(r, "Choice", "1", false)
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 1 || n > len(cliPresets) {
			fmt.Printf("    (enter a number between 1 and %d)\n", len(cliPresets))
			continue
		}
		preset = cliPresets[n-1]
		break
	}
	if preset.launchCmd == "" {
		// "Other" — ask manually.
		preset.launchCmd = prompt(r, "Launch command", "", true)
		preset.promptFlag = prompt(r, "Prompt flag (flag the CLI uses for headless mode)", "--prompt", false)
		resumeRaw := prompt(r, "Resume arg (flag to continue a session, or leave blank)", "", false)
		if resumeRaw != "" {
			preset.resumeArgs = strings.Fields(resumeRaw)
		}
	}

	home, _ := os.UserHomeDir()
	var workingDir string
	for {
		workingDir = prompt(r, "Working directory (absolute path)", home, false)
		if strings.HasPrefix(workingDir, "~") {
			workingDir = filepath.Join(home, strings.TrimPrefix(workingDir, "~"))
		}
		if filepath.IsAbs(workingDir) {
			break
		}
		fmt.Println("    (must be an absolute path, e.g. /Users/you/workspace)")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	body := config.Render(config.RenderParams{
		BotToken:   botToken,
		UserID:     userID,
		LaunchCmd:  preset.launchCmd,
		WorkingDir: workingDir,
		PromptFlag: preset.promptFlag,
		ResumeArgs: preset.resumeArgs,
	})
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("\n✅ Wrote %s\n", path)
	fmt.Println("Next: `tg-cli-bridge run` to smoke-test in the foreground,")
	fmt.Println("then `tg-cli-bridge install` to register the LaunchAgent.")
	return 0
}

func cmdRun(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	// Cancel on SIGINT/SIGTERM so the watcher goroutine winds down cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := bridge.Run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func cmdInstall(cfgPath string) int {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr,
			"install is macOS-only. On Linux, use a systemd user unit instead.")
		return 1
	}
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}
	if _, err := os.Stat(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr,
			"error: config not found at %s. Run `tg-cli-bridge init` first.\n", cfgPath)
		return 1
	}
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: locating own binary:", err)
		return 1
	}
	logPath := filepath.Join(filepath.Dir(cfgPath), "bridge.log")
	target, err := launchd.Install(launchd.Options{
		BinaryPath: bin,
		ConfigPath: cfgPath,
		LogPath:    logPath,
		PathEnv:    config.DefaultPATH(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("✅ Installed and started: %s\n", target)
	fmt.Printf("   Logs: tail -f %s\n", logPath)
	return 0
}

func cmdUninstall() int {
	if err := launchd.Uninstall(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("✅ Uninstalled the LaunchAgent.")
	return 0
}

func cmdStart() int {
	if err := launchd.Kickstart(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("✅ Kickstarted.")
	return 0
}

func cmdStop() int {
	if err := launchd.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("✅ Stopped.")
	return 0
}

func cmdStatus(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("config:      %s\n", cfg.SourcePath)
	fmt.Printf("launch:      %s\n", cfg.LaunchCommand)
	fmt.Printf("working_dir: %s\n", cfg.WorkingDir)
	if launchd.IsLoaded() {
		fmt.Println("LaunchAgent: 🟢 loaded")
	} else {
		fmt.Println("LaunchAgent: 🔴 not loaded")
	}
	return 0
}

func cmdLogs(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	logPath := filepath.Join(filepath.Dir(cfg.SourcePath), "bridge.log")
	if _, err := os.Stat(logPath); err != nil {
		fmt.Fprintf(os.Stderr, "no log file at %s\n", logPath)
		return 1
	}
	// exec tail so Ctrl-C feels native.
	if err := syscall.Exec("/usr/bin/tail", []string{"tail", "-n", "100", "-f", logPath}, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "exec tail:", err)
		return 1
	}
	return 0
}

func cmdAttach(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: tmux not installed. Try: brew install tmux")
		return 1
	}
	if err := syscall.Exec(tmux, []string{"tmux", "attach", "-t", cfg.TmuxSession}, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "exec tmux:", err)
		return 1
	}
	return 0
}

func cmdExample(args []string) int {
	fs := flag.NewFlagSet("example", flag.ContinueOnError)
	out := fs.String("o", "", "Write to this path instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out != "" {
		if err := config.WriteExample(*out); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("✅ Wrote %s\n", *out)
		return 0
	}
	fmt.Println(strings.TrimSpace(config.ExampleConfig))
	return 0
}

// -- helpers --------------------------------------------------------------

func prompt(r *bufio.Reader, label, defaultVal string, required bool) string {
	for {
		suffix := ""
		if defaultVal != "" {
			suffix = fmt.Sprintf(" [%s]", defaultVal)
		}
		fmt.Printf("  %s%s: ", label, suffix)
		line, err := r.ReadString('\n')
		if err != nil {
			// EOF (e.g. piped stdin) — accept default if any, otherwise empty.
			line = ""
		}
		v := strings.TrimSpace(line)
		if v != "" {
			return v
		}
		if defaultVal != "" {
			return defaultVal
		}
		if !required {
			return ""
		}
		fmt.Println("    (required)")
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `tg-cli-bridge — bridge any agentic CLI to a Telegram chat.

Usage:
  tg-cli-bridge [--config PATH] <command> [args]

Commands:
  init        Interactive config wizard.
  run         Run the bridge in the foreground (debug mode).
  install     Install + start the LaunchAgent (macOS).
  uninstall   Remove the LaunchAgent.
  start       Start (or restart) the LaunchAgent.
  stop        Stop the LaunchAgent.
  status      Show service state and current config.
  logs        Tail the bridge log.
  attach      tmux attach -t <session> (if your CLI runs inside tmux).
  example     Print or write an annotated example config.

Global flags:
  --config PATH    Path to config.toml (default: %s)
  --version        Print version and exit
`, config.DefaultPath())
}
