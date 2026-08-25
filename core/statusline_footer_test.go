package core

import (
	"strings"
	"testing"
)

func TestPrettyClaudeModelName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"claude-opus-5", "Opus 5"},
		{"claude-sonnet-5", "Sonnet 5"},
		{"claude-haiku-4-5", "Haiku 4.5"},
		{"claude-opus-4-7[1m]", "Opus 4.7 1M"},
		{"opus-5", "Opus 5"},
		{"", ""},
		// Not a version tuple: leave it alone rather than mangle it.
		{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet-20241022"},
	}
	for _, c := range cases {
		if got := prettyClaudeModelName(c.in); got != c.want {
			t.Errorf("prettyClaudeModelName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func newFooterEngine(t *testing.T, command string) *Engine {
	t.Helper()
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.replyFooterEnabled = true
	e.display.FooterStyle = "statusline"
	e.display.FooterCommand = command
	return e
}

func TestBuildStatusLineCommandFooter_StripsAnsiAndTrailingSeparator(t *testing.T) {
	// Mimics a statusline whose last module rendered empty, leaving the
	// separator literal behind, and which colours its output.
	e := newFooterEngine(t, `printf '\033[38;5;8m\xf0\x9f\x93\x81 repo\033[0m \xe2\x97\x8f Opus 5 \xe2\x97\x8f '`)
	got := e.buildStatusLineCommandFooter(&stubAgent{}, nil, "/home/u/repo")
	if strings.Contains(got, "\x1b") {
		t.Errorf("ANSI escapes survived: %q", got)
	}
	if !strings.HasSuffix(got, "Opus 5") {
		t.Errorf("trailing separator not trimmed: %q", got)
	}
}

func TestBuildStatusLineCommandFooter_KeepsFirstLineOnly(t *testing.T) {
	e := newFooterEngine(t, `printf 'first line\nsecond line\n'`)
	if got := e.buildStatusLineCommandFooter(&stubAgent{}, nil, "/home/u/repo"); got != "first line" {
		t.Errorf("got %q, want %q", got, "first line")
	}
}

// A failing renderer must not swallow the footer: returning "" makes the caller
// fall back to the built-in one.
func TestBuildStatusLineCommandFooter_FailureReturnsEmpty(t *testing.T) {
	e := newFooterEngine(t, "exit 3")
	if got := e.buildStatusLineCommandFooter(&stubAgent{}, nil, "/home/u/repo"); got != "" {
		t.Errorf("expected empty string on failure, got %q", got)
	}
}

func TestBuildStatusLineCommandFooter_NoCommandConfigured(t *testing.T) {
	e := newFooterEngine(t, "")
	if got := e.buildStatusLineCommandFooter(&stubAgent{}, nil, "/home/u/repo"); got != "" {
		t.Errorf("expected empty string without a command, got %q", got)
	}
}

// The renderer receives the raw path; shortening is the renderer's job, and
// compacting here would shorten it twice.
func TestStatusLineFooterPayload_PassesRawWorkDir(t *testing.T) {
	payload := statusLineFooterPayload(&stubAgent{}, nil, "/home/u/very/deep/repo")
	ws, ok := payload["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace missing from payload: %#v", payload)
	}
	if ws["current_dir"] != "/home/u/very/deep/repo" {
		t.Errorf("current_dir = %v, want the raw path", ws["current_dir"])
	}
}
