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

	// Active inline-keyboard menu, if any. Set when the agent's reply
	// ended in a numbered options list.
	activeMenu *activeMenu

	// Track running turns for command cancellation
	runningCancels map[int64]context.CancelFunc
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
		{Command: "switch", Description: "Switch CLI: /switch " + strings.Join(names, " or /switch ")},
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
		runningCancels: make(map[int64]context.CancelFunc),
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
		b.reply(msg.Chat.ID, "⚠️ Unauthorized.")
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
			b.reply(msg.Chat.ID, fmt.Sprintf("⚠️ Failed to download attachment: %v", attachErr))
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

	// Keep the status bubble updated with elapsed time every 3 seconds.
	statusDone := make(chan struct{})
	go func() {
		startTime := time.Now()
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()

		var lastStatusText string
		for {
			select {
			case <-statusDone:
				return
			case <-t.C:
				elapsed := time.Since(startTime).Round(time.Second)
				statusMu.Lock()
				lbl := currentStatus
				statusMu.Unlock()

				statusText := fmt.Sprintf("%s (%s elapsed)", lbl, elapsed)
				if elapsed >= 30*time.Second {
					statusText += "\n\n💡 Send /cancel to stop this task."
				}

				if statusText != lastStatusText && statusMsgID != 0 {
					lastStatusText = statusText
					edit := tgbotapi.NewEditMessageText(chat, statusMsgID, statusText)
					_, _ = b.bot.Request(edit)
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

	// Scan workspace before agent runs
	preFiles := b.scanWorkspace()

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
				currentStatus = s
				statusMu.Unlock()
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
		b.reply(chat, errMsg)
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

	// Scan workspace after agent runs for any newly created or modified files
	postFiles := b.scanWorkspace()
	var modifiedPaths []string
	for path, post := range postFiles {
		pre, exists := preFiles[path]
		if !exists {
			modifiedPaths = append(modifiedPaths, path)
		} else if post.ModTime.After(pre.ModTime) {
			modifiedPaths = append(modifiedPaths, path)
		}
	}

	if body == "" {
		b.reply(chat, "(no output)")
	} else if menu := output.DetectMenu(body); menu != nil {
		b.sendMenuReply(chat, body, menu)
	} else {
		b.sendBody(chat, body)
	}

	if len(modifiedPaths) > 0 {
		b.sendFiles(chat, modifiedPaths)
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
func (b *Bridge) sendBody(chat int64, body string) {
	// body is already HTML (from formatReply).
	for _, chunk := range output.SplitForTelegram(body, b.cfg.MaxMessageChars) {
		m := tgbotapi.NewMessage(chat, chunk)
		m.ParseMode = tgbotapi.ModeHTML
		b.sendWithRetry(m)
	}
}

func (b *Bridge) sendWithRetry(c tgbotapi.Chattable) {
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
			log.Printf("Rate limited (429). Retrying after %d seconds...", seconds)
			time.Sleep(time.Duration(seconds) * time.Second)
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
func (b *Bridge) sendMenuReply(chat int64, body string, menu *output.Menu) {
	prose := stripMenuLines(body, menu)
	if strings.TrimSpace(prose) != "" {
		b.sendBody(chat, prose)
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
	b.activeMenu = &activeMenu{
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
		b.reply(chat,
			"✅ Bridge is live.\n"+
				fmt.Sprintf("Running: %s\n\n", b.cfg.LaunchCommand)+
				"Just send any text — I'll forward it to the agent and reply with what it says.\n\n"+
				"Commands:\n"+
				"/new — start a fresh session\n"+
				"/cancel — cancel the running command\n"+
				"/yes — pick option 1 from a numbered menu\n"+
				"/status — show bridge state\n"+
				strings.Join(names, ", ")+" — switch CLI")
	case "new":
		b.mu.Lock()
		delete(b.sessions, chat)
		delete(b.lastOutput, chat)
		b.activeMenu = nil
		b.mu.Unlock()
		b.reply(chat, "🆕 Session reset. Your next message starts fresh (no --resume).")
	case "cancel":
		b.mu.Lock()
		cancel, running := b.runningCancels[chat]
		b.mu.Unlock()
		if running && cancel != nil {
			cancel()
			b.reply(chat, "🛑 Command cancellation requested.")
		} else {
			b.reply(chat, "ℹ️ No command is currently running.")
		}
	case "status":
		b.mu.Lock()
		active := b.sessions[chat]
		b.mu.Unlock()
		state := "🟢 ready"
		if active {
			state += " (resumed)"
		}
		b.reply(chat, fmt.Sprintf("Launch: %s\nSession: %s",
			b.cfg.LaunchCommand, state))
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
			b.reply(chat, "Usage: /switch "+strings.Join(names, "  or  /switch "))
			return
		}
		preset, ok := config.KnownPresets[arg]
		if !ok {
			names := make([]string, 0, len(config.KnownPresets))
			for k := range config.KnownPresets {
				names = append(names, k)
			}
			sort.Strings(names)
			b.reply(chat, "Unknown CLI \""+arg+"\". Available: "+strings.Join(names, ", "))
			return
		}
		if err := config.UpdateCLI(b.cfg.SourcePath, preset); err != nil {
			b.reply(chat, "❌ Failed to update config: "+err.Error())
			return
		}
		b.reply(chat, fmt.Sprintf("✅ Switched to %s. Restarting…\nSend /new after it comes back.", arg))
		time.Sleep(600 * time.Millisecond) // let the reply flush before exit
		os.Exit(0)                          // LaunchAgent KeepAlive restarts with new config
	default:
		b.reply(chat, "Unknown command. Try /help.")
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

	b.mu.Lock()
	active := b.activeMenu
	b.mu.Unlock()
	if active == nil || cb.Message == nil || active.messageID != cb.Message.MessageID {
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
	b.activeMenu = nil
	b.mu.Unlock()

	// Now treat the choice as a normal turn so the agent advances.
	b.runTurn(ctx, active.chatID, strconv.Itoa(num))
}

// -- helpers ---------------------------------------------------------------

// statusFor maps a single (ANSI-stripped) output line to a human-readable
// status label, or "" if the line doesn't indicate anything interesting.
func statusFor(line string) string {
	low := strings.ToLower(line)
	switch {
	case containsAny(low, "gmail", "email", "mail", "inbox", "thread", "message"):
		return "📧 Checking email…"
	case containsAny(low, "gdrive", "google drive", "drive", "gdoc", "gsheet", "spreadsheet", "document"):
		return "📁 Browsing Drive…"
	case containsAny(low, "calendar", "event", "schedule"):
		return "📅 Checking calendar…"
	case containsAny(low, "readfile", "read_file", "reading"):
		return "📖 Reading files…"
	case containsAny(low, "writefile", "write_file", "editfile", "edit_file", "creating file", "writing"):
		return "✏️ Writing files…"
	case containsAny(low, "shell(", "bash(", "runcmd", "execute", "running"):
		return "⚙️ Running commands…"
	case containsAny(low, "web_search", "googlesearch", "searching", "research"):
		return "🔍 Searching…"
	case containsAny(low, "fetch", "http", "download", "request"):
		return "🌐 Fetching…"
	default:
		return ""
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func (b *Bridge) reply(chat int64, text string) {
	b.sendWithRetry(tgbotapi.NewMessage(chat, text))
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
