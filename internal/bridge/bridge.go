package bridge

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/jaxhemopo/tg-cli-bridge/internal/config"
	"github.com/jaxhemopo/tg-cli-bridge/internal/output"
	"github.com/jaxhemopo/tg-cli-bridge/internal/rpc"
)

// Bridge owns the bot and per-chat session state.
type Bridge struct {
	cfg *config.Config
	bot *tgbotapi.BotAPI

	mu sync.Mutex
	// Per-chat: has the user already started a session? If yes, the next
	// prompt is run with --resume so the agent retains context.
	sessions map[int64]bool
	// Per-chat: accumulated ANSI-stripped stdout from all previous turns.
	// Some CLIs (e.g. AGY with --continue) reprint the entire conversation
	// history on every invocation; we diff against this to extract only the
	// new reply.
	lastOutput map[int64]string

	// Per-chat active inline-keyboard menu, if any. Set when the agent's
	// reply ended in a numbered options list.
	activeMenus map[int64]*activeMenu

	// Track running turns for command cancellation.
	runningCancels map[int64]context.CancelFunc
	// Per-chat guard: true while a turn is in flight, to serialize turns.
	running map[int64]bool
	// Per-chat last user prompt, for /retry.
	lastPrompt map[int64]string
	// Per-chat chats that have disabled auto-sending agent-created files.
	filesDisabled map[int64]bool
}

// activeMenu tracks a numbered menu currently shown as an inline keyboard.
type activeMenu struct {
	chatID      int64
	messageID   int
	fingerprint string
	options     []output.MenuOption
}

// Run boots the bot and processes updates until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return fmt.Errorf("connecting to Telegram: %w", err)
	}
	log.Printf("Authorized as bot @%s", bot.Self.UserName)

	// Register slash-command hints so the / popup works in Telegram.
	names := make([]string, 0, len(config.KnownPresets))
	for k := range config.KnownPresets {
		names = append(names, k)
	}
	sort.Strings(names)
	cmds := []tgbotapi.BotCommand{
		{Command: "new", Description: "Start a fresh session"},
		{Command: "cancel", Description: "Cancel the running command"},
		{Command: "retry", Description: "Re-run your last message"},
		{Command: "switch", Description: "Switch CLI: /switch " + strings.Join(names, " or /switch ")},
		{Command: "files", Description: "Toggle auto-sending agent-created files (/files on|off)"},
		{Command: "status", Description: "Show bridge state"},
		{Command: "yes", Description: "Pick option 1 from a numbered menu"},
		{Command: "help", Description: "List all commands"},
	}
	if _, err := bot.Request(tgbotapi.NewSetMyCommands(cmds...)); err != nil {
		log.Printf("setMyCommands: %v", err)
	}

	b := &Bridge{
		cfg:            cfg,
		bot:            bot,
		sessions:       make(map[int64]bool),
		lastOutput:     make(map[int64]string),
		activeMenus:    make(map[int64]*activeMenu),
		runningCancels: make(map[int64]context.CancelFunc),
		running:        make(map[int64]bool),
		lastPrompt:     make(map[int64]string),
		filesDisabled:  make(map[int64]bool),
	}
	log.Printf("Bridge online (RPC mode). launch_command=%q", cfg.LaunchCommand)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			bot.StopReceivingUpdates()
			log.Println("Bridge shutting down.")
			return nil
		case update := <-updates:
			// Each update goes in its own goroutine so a long agent call
			// doesn't block other commands or callbacks.
			go b.handleUpdate(ctx, update)
		}
	}
}

// -- update routing --------------------------------------------------------

func (b *Bridge) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in handleUpdate: %v", r)
		}
	}()

	if update.CallbackQuery != nil {
		b.handleCallback(ctx, update.CallbackQuery)
		return
	}
	msg := update.Message
	if msg == nil {
		return
	}
	user := msg.From
	if user == nil {
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}

	log.Printf("Received from user_id=%d username=%s text=%q",
		user.ID, user.UserName, text)

	if _, ok := b.cfg.AllowedUserIDs[user.ID]; !ok {
		log.Printf("Unauthorized message from user_id=%d", user.ID)
		b.reply(ctx, msg.Chat.ID, "⚠️ Unauthorized.")
		return
	}

	if msg.IsCommand() {
		b.dispatchCommand(ctx, msg)
		return
	}

	var attachmentPath string
	var attachErr error
	if msg.Photo != nil || msg.Document != nil {
		attachmentPath, attachErr = b.handleIncomingFile(msg)
		if attachErr != nil {
			log.Printf("failed to handle incoming file: %v", attachErr)
			b.reply(ctx, msg.Chat.ID, fmt.Sprintf("⚠️ Failed to download attachment: %v", attachErr))
			return
		}
	}

	if text == "" && attachmentPath != "" {
		if msg.Photo != nil {
			text = "Describe and analyze this image."
		} else {
			text = "Analyze this file."
		}
	}

	if text == "" {
		return
	}

	if attachmentPath != "" {
		baseName := filepath.Base(attachmentPath)
		text = fmt.Sprintf("[Attachment: %s (saved to %s)]\n\n%s", baseName, attachmentPath, text)
	}

	b.runTurn(ctx, msg.Chat.ID, text)
}

// -- one user turn = one CLI invocation -----------------------------------

func (b *Bridge) runTurn(ctx context.Context, chat int64, prompt string) {
	// Serialize turns per chat: reject a new turn while one is in flight so
	// concurrent invocations don't race the session/diff state or clobber the
	// cancel handle.
	b.mu.Lock()
	if b.running[chat] {
		b.mu.Unlock()
		b.reply(ctx, chat, "⏳ Still working on your previous message. Send /cancel to stop it, or wait for it to finish.")
		return
	}
	b.running[chat] = true
	b.lastPrompt[chat] = prompt
	filesOn := !b.filesDisabled[chat]
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.running, chat)
		b.mu.Unlock()
	}()

	_, _ = b.bot.Request(tgbotapi.NewChatAction(chat, tgbotapi.ChatTyping))

	// Post a single status bubble we'll edit in-place while the agent works,
	// then delete entirely before sending the real reply.
	statusSent, err := b.bot.Send(tgbotapi.NewMessage(chat, "⏳ Working…"))
	statusMsgID := 0
	if err == nil {
		statusMsgID = statusSent.MessageID
	}

	// Keep the Telegram "typing" indicator alive every 4 s.
	typingDone := make(chan struct{})
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-typingDone:
				return
			case <-t.C:
				_, _ = b.bot.Request(tgbotapi.NewChatAction(chat, tgbotapi.ChatTyping))
			}
		}
	}()

	var (
		currentStatus = "⏳ Working…"
		statusMu      sync.Mutex
	)

	// Update status bubble when the active tool/status changes (with 5-second throttling)
	// and add the cancellation hint once at 30 seconds.
	statusChanged := make(chan struct{}, 1)
	statusDone := make(chan struct{})
	go func() {
		startTime := time.Now()
		var lastStatusLabel string
		var lastEditTime time.Time
		hintTimer := time.After(30 * time.Second)
		hasHint := false

		updateStatus := func(forceHint bool) {
			statusMu.Lock()
			lbl := currentStatus
			statusMu.Unlock()

			elapsed := time.Since(startTime)
			showHint := hasHint || forceHint || elapsed >= 30*time.Second
			if showHint {
				hasHint = true
			}

			statusText := lbl
			if showHint {
				statusText += "\n\n💡 Send /cancel to stop this task."
			}

			if statusMsgID != 0 && (lbl != lastStatusLabel || forceHint) {
				lastStatusLabel = lbl
				lastEditTime = time.Now()
				edit := tgbotapi.NewEditMessageText(chat, statusMsgID, statusText)
				_, _ = b.bot.Request(edit)
			}
		}

		// Initial state
		lastStatusLabel = "⏳ Working…"

		t := time.NewTicker(1 * time.Second)
		defer t.Stop()

		var pendingUpdate bool

		for {
			select {
			case <-statusDone:
				return
			case <-statusChanged:
				pendingUpdate = true
			case <-hintTimer:
				updateStatus(true)
			case <-t.C:
				if pendingUpdate && time.Since(lastEditTime) >= 5*time.Second {
					updateStatus(false)
					pendingUpdate = false
				}
			}
		}
	}()

	b.mu.Lock()
	resume := b.sessions[chat]
	b.mu.Unlock()

	// Track running turns for command cancellation
	turnCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.runningCancels[chat] = cancel
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.runningCancels, chat)
		b.mu.Unlock()
		cancel()
	}()

	// Snapshot workspace before the agent runs so we can detect files it
	// creates. Skipped entirely when the chat has /files off.
	var preFiles map[string]FileState
	if filesOn {
		preFiles = b.scanWorkspace()
	}

	res := rpc.Run(turnCtx, rpc.Options{
		LaunchCommand: b.cfg.LaunchCommand,
		PromptFlag:    b.cfg.PromptFlag,
		ResumeArgs:    b.cfg.ResumeArgs,
		Resume:        resume,
		WorkingDir:    b.cfg.WorkingDir,
		PathEnv:       b.cfg.PathEnv,
		Timeout:       b.cfg.TurnTimeout(),
		Prompt:        prompt,
		OnProgress: func(rawLine string) {
			if s := statusFor(output.StripANSI(rawLine)); s != "" {
				statusMu.Lock()
				changed := (currentStatus != s)
				currentStatus = s
				statusMu.Unlock()
				if changed {
					select {
					case statusChanged <- struct{}{}:
					default:
					}
				}
			}
		},
	})

	close(typingDone)
	close(statusDone)

	// Remove the status bubble before the real reply lands.
	if statusMsgID != 0 {
		_, _ = b.bot.Request(tgbotapi.NewDeleteMessage(chat, statusMsgID))
	}

	if res.Err != nil {
		log.Printf("Agent command exited with error: %v", res.Err)
		if res.Stderr != "" {
			log.Printf("Agent stderr: %s", res.Stderr)
		}
	}

	stdoutEmpty := strings.TrimSpace(res.Stdout) == ""
	if res.Err != nil && stdoutEmpty {
		var errMsg string
		if turnCtx.Err() == context.Canceled {
			errMsg = "🛑 Command cancelled."
		} else if turnCtx.Err() == context.DeadlineExceeded {
			errMsg = "⚠️ Agent timed out after 10 minutes."
		} else {
			errMsg = fmt.Sprintf("⚠️ agent failed: %v", res.Err)
			if res.Stderr != "" {
				errMsg += "\n" + truncate(res.Stderr, 400)
			}
		}
		b.reply(ctx, chat, errMsg)
		return
	}

	b.mu.Lock()
	b.sessions[chat] = true
	b.mu.Unlock()

	// Strip ANSI, then diff against the accumulated history. Some CLIs (AGY
	// with --continue) reprint every previous reply on each invocation; we
	// only want the new part at the end.
	raw := output.StripANSI(res.Stdout)
	b.mu.Lock()
	prev := b.lastOutput[chat]
	b.lastOutput[chat] = raw
	b.mu.Unlock()
	newRaw := strings.TrimLeft(output.DiffSince(prev, raw), "\n")
	if newRaw == "" {
		newRaw = raw // diff failed — fall back to full output
	}

	body := b.formatReply(newRaw)
	if res.Err != nil {
		if turnCtx.Err() == context.Canceled {
			body += "\n\n🛑 <b>Agent run cancelled by user.</b>"
		} else if turnCtx.Err() == context.DeadlineExceeded {
			body += "\n\n⚠️ <b>Agent timed out after 10 minutes.</b>"
		} else {
			body += fmt.Sprintf("\n\n⚠️ <b>Agent failed: %s</b>", res.Err.Error())
		}
	}

	// Detect files the agent *created* this turn. We deliberately ignore
	// merely-modified files (mtime bumps) so editing existing files like
	// CLAUDE.md or touching a log doesn't spam them back to the chat.
	var newPaths []string
	if filesOn {
		for path := range b.scanWorkspace() {
			if _, existed := preFiles[path]; !existed {
				newPaths = append(newPaths, path)
			}
		}
	}

	menu := output.DetectMenu(body)
	switch {
	case body == "":
		b.reply(ctx, chat, "(no output)")
	case menu != nil:
		b.sendMenuReply(ctx, chat, body, menu)
	case res.Err == nil && utf8.RuneCountInString(newRaw) > b.longReplyThreshold():
		b.sendLongReplyFile(ctx, chat, newRaw)
	default:
		b.sendBody(ctx, chat, body)
	}

	if len(newPaths) > 0 {
		b.sendFiles(ctx, chat, newPaths)
	}
}

// longReplyThreshold is the rune count above which a reply is sent as a file
// attachment instead of being chunked into many messages.
func (b *Bridge) longReplyThreshold() int {
	max := b.cfg.MaxMessageChars
	if max <= 0 {
		max = 3800
	}
	return max * 3
}

// sendLongReplyFile ships a very long reply as a downloadable .md file plus a
// short preview, instead of flooding the chat with many chunked messages.
func (b *Bridge) sendLongReplyFile(ctx context.Context, chat int64, raw string) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("reply_%d.md", time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		log.Printf("long reply: write temp failed: %v; falling back to chunks", err)
		b.sendBody(ctx, chat, b.formatReply(raw))
		return
	}
	defer os.Remove(path)

	doc := tgbotapi.NewDocument(chat, tgbotapi.FilePath(path))
	doc.Caption = "📄 Long reply attached as a file.\n\n" + truncate(raw, 300)
	if _, err := b.bot.Send(doc); err != nil {
		log.Printf("long reply: send doc failed: %v; falling back to chunks", err)
		b.sendBody(ctx, chat, b.formatReply(raw))
	}
}

// formatReply uses output classification to either hide tool calls/banners
// or wrap them in <pre> tags, while letting prose flow as Markdown.
func (b *Bridge) formatReply(s string) string {
	blocks := output.FormatForTelegram(s)
	if len(blocks) == 0 {
		return ""
	}

	// If we're hiding tool calls, and there's ANY prose, then we only show
	// the prose. If there's ONLY tool calls, we fall back to showing them
	// so the user doesn't get a silent blank reply.
	hide := b.cfg.HideToolCalls
	if hide {
		hasProse := false
		for _, blk := range blocks {
			if !blk.IsCode && strings.TrimSpace(blk.Text) != "" {
				hasProse = true
				break
			}
		}
		if !hasProse {
			hide = false // fallback
		}
	}

	var sb strings.Builder
	for i, blk := range blocks {
		text := strings.TrimSpace(blk.Text)
		if text == "" {
			continue
		}

		if blk.IsCode {
			if hide {
				continue
			}
			// Wrap tool calls / banners in <pre>. We must escape HTML
			// specials but we DON'T want MarkdownToHTML to try and
			// find more bold/italic inside a code box.
			sb.WriteString("<pre>")
			sb.WriteString(output.MarkdownToHTML(blk.Text)) // handles escaping + code block protection
			sb.WriteString("</pre>")
		} else {
			sb.WriteString(output.MarkdownToHTML(blk.Text))
		}

		if i < len(blocks)-1 {
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// sendBody chunks body into Telegram-safe pieces and ships each as
// Telegram-flavour HTML so markdown formatting renders.
func (b *Bridge) sendBody(ctx context.Context, chat int64, body string) {
	// body is already HTML (from formatReply).
	for _, chunk := range output.SplitForTelegram(body, b.cfg.MaxMessageChars) {
		m := tgbotapi.NewMessage(chat, chunk)
		m.ParseMode = tgbotapi.ModeHTML
		b.sendWithRetry(ctx, m)
	}
}

func (b *Bridge) sendWithRetry(ctx context.Context, c tgbotapi.Chattable) {
	for i := 0; i < 3; i++ {
		_, err := b.bot.Send(c)
		if err == nil {
			return
		}

		if err, ok := err.(tgbotapi.Error); ok && err.Code == 429 {
			seconds := err.RetryAfter
			if seconds == 0 {
				seconds = 5
			}
			if seconds > 30 {
				log.Printf("Rate limited (429) with long wait (%d seconds). Aborting retry.", seconds)
				break
			}
			log.Printf("Rate limited (429). Retrying after %d seconds...", seconds)
			select {
			case <-ctx.Done():
				log.Printf("Context cancelled while sleeping for rate limit retry: %v", ctx.Err())
				return
			case <-time.After(time.Duration(seconds) * time.Second):
			}
			continue
		}

		log.Printf("send failed (%v); retrying plain", err)
		if msg, ok := c.(tgbotapi.MessageConfig); ok {
			msg.ParseMode = ""
			_, err2 := b.bot.Send(msg)
			if err2 == nil {
				return
			}
			log.Printf("plain send also failed: %v", err2)
		}
		break
	}
}

// sendMenuReply ships the prose preceding a detected menu, then posts a
// follow-up message carrying the numbered options as inline-keyboard
// buttons. Tapping a button sends that number to the agent as the user's
// next message.
func (b *Bridge) sendMenuReply(ctx context.Context, chat int64, body string, menu *output.Menu) {
	prose := stripMenuLines(body, menu)
	if strings.TrimSpace(prose) != "" {
		b.sendBody(ctx, chat, prose)
	}

	text := buildMenuText(menu)
	kb := buildMenuKeyboard(menu)
	m := tgbotapi.NewMessage(chat, text)
	m.ReplyMarkup = kb
	sent, err := b.bot.Send(m)
	if err != nil {
		log.Printf("send menu failed: %v", err)
		return
	}
	b.mu.Lock()
	b.activeMenus[chat] = &activeMenu{
		chatID:      chat,
		messageID:   sent.MessageID,
		fingerprint: menuFingerprint(menu),
		options:     append([]output.MenuOption(nil), menu.Options...),
	}
	b.mu.Unlock()
}

// stripMenuLines removes the menu lines (and the immediately preceding
// blank line) from body so the menu doesn't appear twice (once as prose,
// once as buttons).
func stripMenuLines(body string, menu *output.Menu) string {
	if menu == nil {
		return body
	}
	lines := strings.Split(body, "\n")
	keep := lines[:0]
	for _, line := range lines {
		if isMenuLine(line, menu) {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimRight(strings.Join(keep, "\n"), "\n")
}

func isMenuLine(line string, menu *output.Menu) bool {
	trimmed := strings.TrimSpace(line)
	for _, opt := range menu.Options {
		// Both "1. Yes" and "> 1. Yes" forms.
		if strings.HasPrefix(trimmed, strconv.Itoa(opt.Number)+".") ||
			strings.HasPrefix(trimmed, "> "+strconv.Itoa(opt.Number)+".") {
			return true
		}
	}
	return false
}

// -- commands --------------------------------------------------------------

func (b *Bridge) dispatchCommand(ctx context.Context, msg *tgbotapi.Message) {
	chat := msg.Chat.ID
	switch msg.Command() {
	case "start", "help":
		names := make([]string, 0, len(config.KnownPresets))
		for k := range config.KnownPresets {
			names = append(names, "/switch "+k)
		}
		sort.Strings(names)
		b.reply(ctx, chat,
			"✅ Bridge is live.\n"+
				fmt.Sprintf("Running: %s\n\n", b.cfg.LaunchCommand)+
				"Just send any text — I'll forward it to the agent and reply with what it says.\n\n"+
				"Commands:\n"+
				"/new — start a fresh session\n"+
				"/cancel — cancel the running command\n"+
				"/retry — re-run your last message\n"+
				"/files on|off — toggle auto-sending files the agent creates\n"+
				"/yes — pick option 1 from a numbered menu\n"+
				"/status — show bridge state\n"+
				strings.Join(names, ", ")+" — switch CLI")
	case "new":
		b.mu.Lock()
		delete(b.sessions, chat)
		delete(b.lastOutput, chat)
		delete(b.activeMenus, chat)
		delete(b.lastPrompt, chat)
		b.mu.Unlock()
		b.reply(ctx, chat, "🆕 Session reset. Your next message starts fresh (no --resume).")
	case "cancel":
		b.mu.Lock()
		cancel, running := b.runningCancels[chat]
		b.mu.Unlock()
		if running && cancel != nil {
			cancel()
			b.reply(ctx, chat, "🛑 Command cancellation requested.")
		} else {
			b.reply(ctx, chat, "ℹ️ No command is currently running.")
		}
	case "retry":
		b.mu.Lock()
		last := b.lastPrompt[chat]
		b.mu.Unlock()
		if last == "" {
			b.reply(ctx, chat, "ℹ️ Nothing to retry yet.")
			return
		}
		b.runTurn(ctx, chat, last)
	case "files":
		arg := strings.ToLower(strings.TrimSpace(msg.CommandArguments()))
		b.mu.Lock()
		switch arg {
		case "on":
			delete(b.filesDisabled, chat)
		case "off":
			b.filesDisabled[chat] = true
		case "":
			disabled := b.filesDisabled[chat]
			b.mu.Unlock()
			state := "on"
			if disabled {
				state = "off"
			}
			b.reply(ctx, chat, fmt.Sprintf("📎 File auto-send is %s. Use /files on or /files off.", state))
			return
		default:
			b.mu.Unlock()
			b.reply(ctx, chat, "Usage: /files on  or  /files off")
			return
		}
		on := !b.filesDisabled[chat]
		b.mu.Unlock()
		if on {
			b.reply(ctx, chat, "📎 File auto-send enabled — I'll send back files the agent newly creates.")
		} else {
			b.reply(ctx, chat, "🔕 File auto-send disabled.")
		}
	case "status":
		b.mu.Lock()
		active := b.sessions[chat]
		busy := b.running[chat]
		filesOff := b.filesDisabled[chat]
		b.mu.Unlock()
		state := "🟢 ready"
		if busy {
			state = "⏳ running a command"
		} else if active {
			state = "🟢 ready (resumed)"
		}
		files := "on"
		if filesOff {
			files = "off"
		}
		b.reply(ctx, chat, fmt.Sprintf("Launch: %s\nSession: %s\nFile auto-send: %s",
			b.cfg.LaunchCommand, state, files))
	case "yes", "y":
		// Shorthand for picking option 1 in a numbered menu.
		b.runTurn(ctx, chat, "1")
	case "switch":
		arg := strings.ToLower(strings.TrimSpace(msg.CommandArguments()))
		if arg == "" {
			names := make([]string, 0, len(config.KnownPresets))
			for k := range config.KnownPresets {
				names = append(names, k)
			}
			sort.Strings(names)
			b.reply(ctx, chat, "Usage: /switch "+strings.Join(names, "  or  /switch "))
			return
		}
		preset, ok := config.KnownPresets[arg]
		if !ok {
			names := make([]string, 0, len(config.KnownPresets))
			for k := range config.KnownPresets {
				names = append(names, k)
			}
			sort.Strings(names)
			b.reply(ctx, chat, "Unknown CLI \""+arg+"\". Available: "+strings.Join(names, ", "))
			return
		}
		if err := config.UpdateCLI(b.cfg.SourcePath, preset); err != nil {
			b.reply(ctx, chat, "❌ Failed to update config: "+err.Error())
			return
		}
		// We rely on os.Exit + LaunchAgent KeepAlive to restart with the new
		// config. If we're NOT running under launchd (ppid 1) — e.g. a
		// foreground `tg-cli-bridge run` — exiting would just kill the bridge
		// with no restart, so update the config and tell the user to restart.
		if os.Getppid() != 1 {
			b.reply(ctx, chat, fmt.Sprintf("✅ Config switched to %s, but I'm not running under launchd so I can't auto-restart. Restart me to apply it (then send /new).", arg))
			return
		}
		b.reply(ctx, chat, fmt.Sprintf("✅ Switched to %s. Restarting…\nSend /new after it comes back.", arg))
		time.Sleep(600 * time.Millisecond) // let the reply flush before exit
		os.Exit(0)                          // LaunchAgent KeepAlive restarts with new config
	default:
		b.reply(ctx, chat, "Unknown command. Try /help.")
	}
}

// -- inline-keyboard callbacks --------------------------------------------

func (b *Bridge) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	if cb.From == nil {
		return
	}
	if _, ok := b.cfg.AllowedUserIDs[cb.From.ID]; !ok {
		log.Printf("Unauthorized callback from user_id=%d", cb.From.ID)
		_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, "Unauthorized"))
		return
	}
	const prefix = "menu:"
	if !strings.HasPrefix(cb.Data, prefix) {
		_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		return
	}
	num, err := strconv.Atoi(strings.TrimPrefix(cb.Data, prefix))
	if err != nil {
		_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		return
	}
	if cb.Message == nil {
		_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		return
	}

	chat := cb.Message.Chat.ID
	b.mu.Lock()
	active := b.activeMenus[chat]
	b.mu.Unlock()
	if active == nil || active.messageID != cb.Message.MessageID {
		_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, "This menu is no longer active."))
		return
	}

	var label string
	for _, opt := range active.options {
		if opt.Number == num {
			label = opt.Label
			break
		}
	}

	_, _ = b.bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Picked %d", num)))
	summary := fmt.Sprintf("✓ Picked %d. %s", num, label)
	edit := tgbotapi.NewEditMessageText(active.chatID, active.messageID, summary)
	_, _ = b.bot.Request(edit)

	b.mu.Lock()
	delete(b.activeMenus, chat)
	b.mu.Unlock()

	// Now treat the choice as a normal turn so the agent advances.
	b.runTurn(ctx, chat, strconv.Itoa(num))
}

// -- helpers ---------------------------------------------------------------

// statusFor maps a single (ANSI-stripped) output line to a human-readable
// status label, or "" if the line doesn't indicate anything interesting.
//
// It matches structured tool-call markers of the form name(… — the way most
// agent CLIs print tool invocations (e.g. "● Bash(…)", "Read(…)") — rather
// than loose keywords. Loose matching (e.g. any line containing "message" or
// "running") produced misleading status labels on ordinary prose.
func statusFor(line string) string {
	low := strings.ToLower(line)
	switch {
	case hasToolCall(low, "gmail", "search_threads", "list_drafts", "get_thread"):
		return "📧 Checking email…"
	case hasToolCall(low, "drive", "google_drive", "list_recent_files", "read_file_content"):
		return "📁 Browsing Drive…"
	case hasToolCall(low, "calendar", "list_events", "create_event"):
		return "📅 Checking calendar…"
	case hasToolCall(low, "read", "readfile", "read_file", "cat", "view"):
		return "📖 Reading files…"
	case hasToolCall(low, "write", "writefile", "write_file", "edit", "edit_file", "str_replace", "create_file"):
		return "✏️ Writing files…"
	case hasToolCall(low, "bash", "shell", "run_terminal", "execute", "exec_command"):
		return "⚙️ Running commands…"
	case hasToolCall(low, "websearch", "web_search", "google_search", "search"):
		return "🔍 Searching…"
	case hasToolCall(low, "webfetch", "web_fetch", "fetch", "http_request"):
		return "🌐 Fetching…"
	default:
		return ""
	}
}

// hasToolCall reports whether the line invokes one of the named tools, i.e.
// contains "name(" — the call syntax — for any name. Requiring the opening
// paren avoids matching the same word used in ordinary prose.
func hasToolCall(line string, names ...string) bool {
	for _, n := range names {
		if strings.Contains(line, n+"(") {
			return true
		}
	}
	return false
}

func (b *Bridge) reply(ctx context.Context, chat int64, text string) {
	b.sendWithRetry(ctx, tgbotapi.NewMessage(chat, text))
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func menuFingerprint(m *output.Menu) string {
	var sb strings.Builder
	for _, opt := range m.Options {
		fmt.Fprintf(&sb, "%d:%s\n", opt.Number, opt.Label)
	}
	return sb.String()
}

func buildMenuText(m *output.Menu) string {
	var sb strings.Builder
	if m.Question != "" {
		sb.WriteString(m.Question)
		sb.WriteString("\n\n")
	}
	for _, opt := range m.Options {
		fmt.Fprintf(&sb, "%d. %s\n", opt.Number, opt.Label)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func buildMenuKeyboard(m *output.Menu) tgbotapi.InlineKeyboardMarkup {
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(m.Options))
	for _, opt := range m.Options {
		label := opt.Label
		const maxBtnLabel = 40
		if len([]rune(label)) > maxBtnLabel {
			runes := []rune(label)
			label = string(runes[:maxBtnLabel-1]) + "…"
		}
		btnText := fmt.Sprintf("%d. %s", opt.Number, label)
		data := fmt.Sprintf("menu:%d", opt.Number)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(btnText, data),
		))
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

// handleIncomingFile downloads a photo or document from Telegram to the workspace.
func (b *Bridge) handleIncomingFile(msg *tgbotapi.Message) (string, error) {
	var fileID string
	var fileName string

	if msg.Document != nil {
		fileID = msg.Document.FileID
		fileName = msg.Document.FileName
	} else if msg.Photo != nil && len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		fileID = photo.FileID
		fileName = fmt.Sprintf("photo_%d.jpg", time.Now().Unix())
	} else {
		return "", nil
	}

	// Never trust the Telegram-supplied name as a path: strip any directory
	// components so a name like "../../foo" can't escape the uploads dir.
	fileName = filepath.Base(filepath.Clean("/" + fileName))
	if fileName == "" || fileName == "/" || fileName == "." {
		fileName = fmt.Sprintf("file_%d", time.Now().Unix())
	}

	uploadsDir := filepath.Join(b.cfg.WorkingDir, "telegram_uploads")
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		return "", fmt.Errorf("creating uploads dir: %w", err)
	}

	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	file, err := b.bot.GetFile(fileConfig)
	if err != nil {
		return "", fmt.Errorf("getting file info: %w", err)
	}

	downloadURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.bot.Token, file.FilePath)
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("downloading file: %w", err)
	}
	defer resp.Body.Close()

	destPath := filepath.Join(uploadsDir, fileName)
	out, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("creating local file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("saving file content: %w", err)
	}

	return destPath, nil
}
