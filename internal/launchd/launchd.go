// Package launchd manages the macOS LaunchAgent that keeps the bridge running.
//
// The plist is rendered fresh on every Install so it always points at the
// currently-running binary. Bootout-then-bootstrap is the modern equivalent
// of unload-then-load. KeepAlive + a 10s ThrottleInterval means launchd will
// restart the bridge on crash without burning CPU in a tight loop.
package launchd

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Label is the canonical launchd label for the bridge LaunchAgent.
const Label = "com.tg-cli-bridge.agent"

// PlistPath returns the on-disk location of the LaunchAgent plist.
func PlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

// Options bundles everything Install needs to know.
type Options struct {
	BinaryPath string // absolute path to the tg-cli-bridge binary
	ConfigPath string // absolute path to config.toml
	LogPath    string // absolute path to the log file (stdout + stderr)
	PathEnv    string // PATH to set inside the LaunchAgent's environment
}

// Render returns the XML plist for the supplied Options.
func Render(opts Options) string {
	tmpl := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>

    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>--config</string>
        <string>%s</string>
        <string>run</string>
    </array>

    <key>WorkingDirectory</key>
    <string>%s</string>

    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ThrottleInterval</key>
    <integer>10</integer>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>%s</string>
        <key>LANG</key>
        <string>en_US.UTF-8</string>
        <key>LC_ALL</key>
        <string>en_US.UTF-8</string>
    </dict>

    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`
	return fmt.Sprintf(tmpl,
		xmlEscape(Label),
		xmlEscape(opts.BinaryPath),
		xmlEscape(opts.ConfigPath),
		xmlEscape(filepath.Dir(opts.ConfigPath)),
		xmlEscape(opts.PathEnv),
		xmlEscape(opts.LogPath),
		xmlEscape(opts.LogPath),
	)
}

// Install writes the plist, boots out any prior instance, then bootstraps
// and kickstarts the LaunchAgent.
func Install(opts Options) (string, error) {
	target := PlistPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, []byte(Render(opts)), 0o644); err != nil {
		return "", err
	}
	_ = launchctl("bootout", domainTarget())                            // ignore: may not be loaded
	if err := launchctl("bootstrap", domain(), target); err != nil {
		return target, fmt.Errorf("bootstrap: %w", err)
	}
	_ = launchctl("enable", domainTarget())
	if err := launchctl("kickstart", "-k", domainTarget()); err != nil {
		return target, fmt.Errorf("kickstart: %w", err)
	}
	return target, nil
}

// Uninstall boots out the LaunchAgent and removes the plist.
func Uninstall() error {
	_ = launchctl("bootout", domainTarget())
	p := PlistPath()
	if _, err := os.Stat(p); err == nil {
		return os.Remove(p)
	}
	return nil
}

// Kickstart restarts the LaunchAgent in place.
func Kickstart() error { return launchctl("kickstart", "-k", domainTarget()) }

// Stop boots out the LaunchAgent (the modern equivalent of `launchctl unload`).
func Stop() error { return launchctl("bootout", domainTarget()) }

// IsLoaded returns true if launchd currently knows about the LaunchAgent.
func IsLoaded() bool {
	return exec.Command("launchctl", "print", domainTarget()).Run() == nil
}

// -- internals -------------------------------------------------------------

func domain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func domainTarget() string {
	return fmt.Sprintf("%s/%s", domain(), Label)
}

func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w (%s)",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
