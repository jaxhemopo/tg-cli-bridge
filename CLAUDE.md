# tg-cli-bridge

Go binary that bridges a Telegram bot to an agentic CLI (Gemini CLI, AGY/Antigravity, Claude Code, plain shell, etc.).

## Build

```bash
go build -o tg-cli-bridge ./cmd/tg-cli-bridge
go test ./...
go vet ./...
```

## Install & run (macOS)

```bash
tg-cli-bridge init          # interactive config wizard
tg-cli-bridge run           # foreground test
tg-cli-bridge install       # register LaunchAgent + start
```

## Key files

| Path | What it does |
|------|-------------|
| `cmd/tg-cli-bridge/main.go` | CLI entrypoint, all subcommands |
| `internal/bridge/bridge.go` | Core loop: receives Telegram updates, runs agent, sends reply |
| `internal/rpc/rpc.go` | Spawns the CLI process, captures stdout, streams progress |
| `internal/output/output.go` | ANSI stripping, prose/code classification, menu detection, `DiffSince` |
| `internal/config/config.go` | TOML load/validate, `KnownPresets`, `UpdateCLI` |
| `internal/launchd/launchd.go` | macOS LaunchAgent install/uninstall/status |

## Architecture — RPC mode

Each Telegram message spawns the agent CLI once as a subprocess:

```
Telegram message
  → bridge receives it
  → posts ⏳ status bubble
  → rpc.Run: exec CLI with --prompt "text" [--resume latest]
  → streams stdout; edits bubble in-place as keywords are detected
  → CLI exits
  → delete status bubble
  → send prose-only reply to Telegram
```

**No persistent process.** The CLI starts fresh each turn and exits. Session continuity comes from the CLI's own `--resume` / `--continue` flag (Gemini and AGY both support this).

## Design decisions

- **One spawn per message** — avoids tmux pane-watching and Telegram rate-limit hammering from constant redraws.
- **Prose-only output** — `output.FormatForTelegram` classifies lines as prose vs code/tool-call banners. Only prose reaches Telegram so tool-call boxes don't pollute the chat.
- **Status bubble** — single ⏳ message edits in-place as agent stdout reveals what it's doing (email, drive, shell, etc.). Deleted before the real reply lands so the chat stays clean.
- **`DiffSince`** — some CLIs (AGY `--continue`) reprint the entire conversation history on every invocation. `DiffSince(prev, curr)` extracts only the new content.
- **`KnownPresets`** in `config.go` — maps short names (`gemini`, `agy`, `claude`) to the right flags. Powers both the `init` wizard and the `/switch` Telegram command.

## Telegram bot commands

| Command | Effect |
|---------|--------|
| `/new` | Clear session — next message starts fresh |
| `/switch <name>` | Switch CLI live (e.g. `/switch gemini`). Updates config and restarts. |
| `/status` | Show current launch_command and LaunchAgent state |
| `/yes` | Send "1" to a numbered menu |
| `/help` | List commands |

## Adding a new CLI preset

1. Add an entry to `KnownPresets` in `internal/config/config.go`.
2. Add the same entry to `cliPresets` in `cmd/tg-cli-bridge/main.go` (for the init wizard).
3. Verify the CLI supports a headless `--prompt` / `--print` flag and test with `tg-cli-bridge run`.

## Context files

Put a `CLAUDE.md` (or `GEMINI.md`) in the `working_dir` you configure. The agent loads it at startup and uses it to understand what tools and context are available. See `examples/GEMINI.md.example` for a Google Workspace template.
