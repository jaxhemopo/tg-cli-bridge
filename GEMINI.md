# tg-cli-bridge — project context

Go binary. Bridges a Telegram bot to an agentic CLI (Gemini CLI, AGY, Claude Code, etc.).

## Build & test

```bash
go build -o tg-cli-bridge ./cmd/tg-cli-bridge
go test ./...
go vet ./...
```

## How it works

Each incoming Telegram message spawns the agent CLI once as a subprocess, captures stdout, and sends the response back. No persistent process. Session continuity via the CLI's own `--resume` / `--continue` flag.

```
Telegram message → rpc.Run(CLI --prompt "text") → parse stdout → Telegram reply
```

## Key files

- `cmd/tg-cli-bridge/main.go` — CLI subcommands (init, run, install, status, logs…)
- `internal/bridge/bridge.go` — main loop, Telegram update handling, status bubble, command dispatch
- `internal/rpc/rpc.go` — subprocess execution, stdout streaming, progress callbacks
- `internal/output/output.go` — ANSI stripping, prose/code classification, menu parsing, `DiffSince`
- `internal/config/config.go` — TOML config, `KnownPresets` map, `UpdateCLI` for live switching
- `internal/launchd/launchd.go` — macOS LaunchAgent lifecycle

## Config location

Default: `~/.config/tg-cli-bridge/config.toml`

Required fields: `[telegram].bot_token`, `[telegram].allowed_user_ids`, `[session].launch_command`

## Telegram commands (runtime)

`/new`, `/switch <gemini|agy|claude>`, `/status`, `/yes`, `/help`

## Adding a new CLI

1. Add to `KnownPresets` in `internal/config/config.go`
2. Add to `cliPresets` in `cmd/tg-cli-bridge/main.go`
3. The CLI needs a headless flag (`--prompt` or `--print`) that prints output to stdout and exits

## Working directory context

Agents load `GEMINI.md` (or `CLAUDE.md`) from `working_dir` at startup. Put tool documentation there — see `examples/GEMINI.md.example` for a Google Workspace example.
