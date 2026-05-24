package bridge

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type FileState struct {
	Path    string
	ModTime time.Time
	Size    int64
}

// scanWorkspace walks the working directory to record file states (modification times and sizes).
// It ignores hidden files/directories and common dependency folders to be fast and safe.
func (b *Bridge) scanWorkspace() map[string]FileState {
	state := make(map[string]FileState)
	dir := b.cfg.WorkingDir
	if dir == "" {
		return state
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Don't recurse into ignored directories
		base := filepath.Base(path)
		if info.IsDir() {
			if base != "." && strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			if base == "node_modules" || base == "venv" || base == ".venv" || base == "env" || base == "dist" || base == "build" || base == "telegram_uploads" {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip hidden files
		if base != "." && strings.HasPrefix(base, ".") {
			return nil
		}

		// We only care about normal files
		if info.Mode().IsRegular() {
			state[path] = FileState{
				Path:    path,
				ModTime: info.ModTime(),
				Size:    info.Size(),
			}
		}
		return nil
	})

	if err != nil {
		log.Printf("error walking workspace: %v", err)
	}

	return state
}

// isSendableFile returns true if the file extension is one of the supported media/document types.
func isSendableFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg",
		".pdf", ".csv", ".xlsx", ".txt", ".json", ".md", ".log":
		return true
	}
	return false
}

// isImage returns true if the file extension is an image.
func isImage(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return true
	}
	return false
}

// sendFiles uploads and sends the specified files to the Telegram chat.
func (b *Bridge) sendFiles(chat int64, filePaths []string) {
	const maxFiles = 5
	sentCount := 0

	for _, path := range filePaths {
		if !isSendableFile(path) {
			continue
		}

		if sentCount >= maxFiles {
			b.reply(chat, fmt.Sprintf("ℹ️ ...and %d other generated/modified files were found in the workspace.", len(filePaths)-sentCount))
			break
		}

		// Check if file is empty or missing
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			continue
		}

		ext := strings.ToLower(filepath.Ext(path))
		var sentErr error

		if isImage(ext) {
			photo := tgbotapi.NewPhoto(chat, tgbotapi.FilePath(path))
			photo.Caption = filepath.Base(path)
			_, sentErr = b.bot.Send(photo)
		} else {
			doc := tgbotapi.NewDocument(chat, tgbotapi.FilePath(path))
			doc.Caption = filepath.Base(path)
			_, sentErr = b.bot.Send(doc)
		}

		if sentErr != nil {
			log.Printf("failed to send file %s to Telegram: %v", path, sentErr)
		} else {
			sentCount++
		}
	}
}
