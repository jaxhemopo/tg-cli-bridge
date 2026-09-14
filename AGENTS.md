# tg-cli-bridge contributor guide

This repository builds a Go binary that connects Telegram to AGY, Claude Code,
Codex, a Claude-compatible GLM wrapper, or another headless agent CLI.

## Validate changes

```bash
go test -race ./...
go vet ./...
```

## Architecture

Each Telegram message starts one short-lived CLI subprocess through
`internal/rpc`. The bridge captures stdout, filters terminal noise, and sends
the final reply to Telegram. The normal path does not use a persistent tmux
session.

The bridge records only whether each Telegram chat has started. It does not
store engine conversation IDs, so latest-session arguments such as AGY or
Claude `--continue` and Codex `resume --last` are not isolated per Telegram
chat. Preserve the documented one-engine/one-chat-per-working-directory rule
unless explicit session-ID tracking is implemented for every supported engine.

## Adding an engine preset

Keep these two preset lists in sync:

1. `internal/config/config.go` (`KnownPresets`) for `/switch`.
2. `cmd/tg-cli-bridge/main.go` (`cliPresets`) for the setup wizard.

Set the engine's fixed and model arguments in `launch_command`, its one-shot
prompt flag in `prompt_flag`, and its latest-session arguments in
`resume_args`. Use `prompt_flag = "--"` when the prompt is positional. Add an
exact argv test in `internal/rpc/rpc_test.go` for any new command shape.

See `CLAUDE.md` for the broader package map and design notes.
