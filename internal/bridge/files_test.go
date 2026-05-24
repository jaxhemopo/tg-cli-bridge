package bridge

import (
	"testing"
)

func TestIsSendableFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"test.png", true},
		{"test.JPG", true},
		{"test.pdf", true},
		{"test.txt", true},
		{"test.go", false},
		{"test.exe", false},
		{".gitignore", false},
	}

	for _, tt := range tests {
		if got := isSendableFile(tt.path); got != tt.want {
			t.Errorf("isSendableFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestIsImage(t *testing.T) {
	tests := []struct {
		ext  string
		want bool
	}{
		{".png", true},
		{".jpg", true},
		{".jpeg", true},
		{".gif", true},
		{".pdf", false},
		{".txt", false},
	}

	for _, tt := range tests {
		if got := isImage(tt.ext); got != tt.want {
			t.Errorf("isImage(%q) = %v, want %v", tt.ext, got, tt.want)
		}
	}
}
