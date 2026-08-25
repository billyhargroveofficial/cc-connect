package core

import (
	"strings"
	"testing"
)

// Regression: tool_max_len was applied only to the structured progress-card
// payload, so platforms using the compact or legacy progress style (Telegram
// normalizes "card" to "compact") dumped the full tool input into the chat.
func TestFormatToolInput_TruncatesLongInput(t *testing.T) {
	long := strings.Repeat("x", 500)
	out := formatToolInput("Read", long, 80)
	if strings.Contains(out, long) {
		t.Fatalf("tool_max_len ignored: full %d-rune input present in %q", len(long), out)
	}
	if !strings.Contains(out, "...") {
		t.Errorf("expected truncation marker, got %q", out)
	}
}

func TestFormatToolInput_ZeroMaxLenKeepsFullInput(t *testing.T) {
	long := strings.Repeat("y", 300)
	out := formatToolInput("Read", long, 0)
	if !strings.Contains(out, long) {
		t.Errorf("maxLen=0 must not truncate, got %q", out)
	}
}

func TestFormatToolInput_Empty(t *testing.T) {
	if out := formatToolInput("Read", "", 80); out != "" {
		t.Errorf("expected empty string, got %q", out)
	}
}

func TestFormatToolInput_ShellUsesBashFence(t *testing.T) {
	out := formatToolInput("Bash", "git status", 80)
	if !strings.HasPrefix(out, "```bash\n") {
		t.Errorf("expected bash fence, got %q", out)
	}
}

func TestFormatToolInput_ShortInputUsesInlineCode(t *testing.T) {
	out := formatToolInput("Read", "main.go", 80)
	if out != "`main.go`" {
		t.Errorf("expected inline code, got %q", out)
	}
}

func TestFormatToolInput_PreFencedPassesThrough(t *testing.T) {
	in := "```go\nfmt.Println()\n```"
	if out := formatToolInput("Read", in, 0); out != in {
		t.Errorf("pre-fenced input should pass through, got %q", out)
	}
}

// Truncation happens before fencing, so a fence opened by the formatter is
// always closed even when the raw input is cut mid-line.
func TestFormatToolInput_TruncatedFenceStaysBalanced(t *testing.T) {
	out := formatToolInput("Bash", "echo "+strings.Repeat("z", 500), 80)
	if strings.HasPrefix(out, "```") && strings.Count(out, "```") != 2 {
		t.Errorf("unbalanced code fence: %q", out)
	}
}

func TestFormatToolInputCompact_CollapsesToOneLine(t *testing.T) {
	out := formatToolInputCompact("git status\ngit diff\n\ngit log", 0)
	if strings.Contains(out, "\n") {
		t.Fatalf("compact style must stay on one line, got %q", out)
	}
	if out != "`git status git diff git log`" {
		t.Errorf("unexpected collapse result: %q", out)
	}
}

func TestFormatToolInputCompact_Truncates(t *testing.T) {
	out := formatToolInputCompact(strings.Repeat("a", 500), 40)
	if len([]rune(out)) > 50 {
		t.Errorf("expected truncation to ~40 runes, got %d: %q", len([]rune(out)), out)
	}
}

// A backtick in the input would otherwise close the inline code span early and
// leak the rest of the tool input as raw markdown.
func TestFormatToolInputCompact_NeutralizesBackticks(t *testing.T) {
	out := formatToolInputCompact("echo `whoami`", 0)
	if strings.Count(out, "`") != 2 {
		t.Errorf("inline code span must contain exactly 2 backticks, got %q", out)
	}
}

func TestFormatToolInputCompact_Empty(t *testing.T) {
	if out := formatToolInputCompact("   \n  ", 80); out != "" {
		t.Errorf("whitespace-only input should render empty, got %q", out)
	}
}
