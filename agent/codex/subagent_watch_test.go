package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestSummarizeSubagentToolInput(t *testing.T) {
	cases := map[string]string{
		`text(await tools.web__run({search_query:[{q:'KOSPI October 16'},{"q":"Samsung"}]}))`:    "🌐 KOSPI October 16 | Samsung",
		`text(await tools.web__run({open:[{ref_id:"https://example.com/a"}]}))`:                  "📄 https://example.com/a",
		`text(await tools.exec_command({cmd:"rg -i 'x' /tmp/f.jsonl","max_output_tokens":300}))`: "💻 rg -i 'x' /tmp/f.jsonl",
	}
	for in, want := range cases {
		if got := summarizeSubagentToolInput("exec", in); got != want {
			t.Errorf("summarize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubagentWatcherFollowsChildRollout(t *testing.T) {
	t.Setenv("CC_CODEX_SUBAGENT_TOOLS", "1")
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "10", "16")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "rollout-2026-10-16T10-00-00-parent-1.jsonl")
	child := filepath.Join(dir, "rollout-2026-10-16T10-00-01-child-2.jsonl")
	write := func(p, s string) {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s + "\n")
		f.Close()
	}
	write(parent, `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"SubAgentActivity","kind":"started","agent_thread_id":"child-2","agent_path":"/root/pong"}}}`)
	write(child, `{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":"tools.web__run({search_query:[{q:'pong'}]})"}}`)
	write(parent, `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"SubAgentActivity","kind":"completed","agent_thread_id":"child-2","agent_path":"/root/pong"}}}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &codexSession{ctx: ctx, events: make(chan core.Event, 16), extraEnv: []string{"CODEX_HOME=" + home}}
	cs.threadID.Store("parent-1")
	w := newSubagentWatcher(cs, false)
	go w.run()
	time.Sleep(50 * time.Millisecond)
	w.Stop()
	close(cs.events)
	var got []string
	for e := range cs.events {
		got = append(got, e.ToolInput)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"▶ pong", "pong › 🌐 pong", "✓ pong"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in events:\n%s", want, joined)
		}
	}
}

func TestSubagentWatcherHidesChildToolsByDefault(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "10", "16")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "rollout-x-parent-1.jsonl"), []byte(`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"SubAgentActivity","kind":"started","agent_thread_id":"child-2","agent_path":"/root/pong"}}}`+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "rollout-x-child-2.jsonl"), []byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":"tools.web__run({search_query:[{q:'pong'}]})"}}`+"\n"+`{"type":"response_item","payload":{"type":"function_call","name":"send_message","arguments":"{}"}}`+"\n"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs := &codexSession{ctx: ctx, events: make(chan core.Event, 16), extraEnv: []string{"CODEX_HOME=" + home}}
	cs.threadID.Store("parent-1")
	w := newSubagentWatcher(cs, false)
	go w.run()
	time.Sleep(50 * time.Millisecond)
	w.Stop()
	close(cs.events)
	var got []string
	for e := range cs.events {
		got = append(got, e.ToolInput)
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "🌐") || !strings.Contains(joined, "▶ pong") || !strings.Contains(joined, "pong › ↩ отчёт") {
		t.Errorf("unexpected events:\n%s", joined)
	}
}
