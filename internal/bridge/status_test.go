package bridge

import "testing"

func TestStatusFor(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		// Tool calls (name( syntax) should be labelled.
		{"bash call", "● Bash(ls -la)", "⚙️ Running commands…"},
		{"read call", "Read(/etc/hosts)", "📖 Reading files…"},
		{"edit call", "Edit(main.go)", "✏️ Writing files…"},
		{"web search call", "WebSearch(golang generics)", "🔍 Searching…"},
		{"fetch call", "WebFetch(https://example.com)", "🌐 Fetching…"},

		// Prose containing the same words must NOT trigger a label — this is
		// the regression the tighter matcher fixes.
		{"prose with message", "I'll send you a message when it's done.", ""},
		{"prose with running", "The server is running on port 8080.", ""},
		{"prose with request", "Your request has been processed.", ""},
		{"prose with document", "Here is a summary of the document.", ""},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusFor(tt.line); got != tt.want {
				t.Errorf("statusFor(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}
