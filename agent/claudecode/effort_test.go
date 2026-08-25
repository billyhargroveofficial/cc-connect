package claudecode

import "testing"

// The Claude CLI documents --effort as (low, medium, high, xhigh, max).
// normalizeEffort used to omit xhigh, so a valid level was silently dropped to
// "" and the flag was never passed.
func TestNormalizeEffort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"low", "low"},
		{"medium", "medium"},
		{"med", "medium"},
		{"high", "high"},
		{"xhigh", "xhigh"},
		{"XHigh", "xhigh"},
		{"x-high", "xhigh"},
		{"  xhigh  ", "xhigh"},
		{"max", "max"},
		{"", ""},
		{"nonsense", ""},
	}
	for _, c := range cases {
		if got := normalizeEffort(c.in); got != c.want {
			t.Errorf("normalizeEffort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The advertised list must match what normalizeEffort accepts, or /effort
// offers a level that is then discarded.
func TestAvailableReasoningEffortsAreAllAccepted(t *testing.T) {
	a := &Agent{}
	levels := a.AvailableReasoningEfforts()
	if len(levels) != 5 {
		t.Errorf("expected 5 levels, got %d: %v", len(levels), levels)
	}
	for _, level := range levels {
		if got := normalizeEffort(level); got != level {
			t.Errorf("advertised level %q normalizes to %q", level, got)
		}
	}
}
