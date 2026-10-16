package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// codex exec --json does not surface multi-agent activity: spawn_agent and
// the subagents' own tool calls appear only in the rollout files (the stream
// carries an empty collab_tool_call "wait" at most). subagentWatcher tails the
// parent thread's rollout during a turn, follows every subagent it spawns into
// that subagent's rollout and emits compact "subagent" tool events, so chat
// progress shows which subagents run and what they are doing.

var subagentWatchInterval = 700 * time.Millisecond

type subagentWatchFile struct {
	threadID string
	label    string // "" for the parent thread
	path     string
	offset   int64
	fromEnd  bool // resumed parent: skip what earlier turns wrote
}

type subagentWatcher struct {
	cs    *codexSession
	home  string
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
	files []*subagentWatchFile
	known map[string]bool
}

func newSubagentWatcher(cs *codexSession, resume bool) *subagentWatcher {
	home, err := resolveCodexHome(cs.extraEnv)
	if err != nil {
		return nil
	}
	w := &subagentWatcher{cs: cs, home: home, stop: make(chan struct{}), done: make(chan struct{}), known: map[string]bool{}}
	if tid := cs.CurrentSessionID(); tid != "" {
		w.add(tid, "", resume)
	}
	return w
}

func (w *subagentWatcher) add(threadID, label string, fromEnd bool) {
	if threadID == "" || w.known[threadID] {
		return
	}
	w.known[threadID] = true
	f := &subagentWatchFile{threadID: threadID, label: label, fromEnd: fromEnd}
	w.files = append(w.files, f)
}

func (w *subagentWatcher) run() {
	defer close(w.done)
	t := time.NewTicker(subagentWatchInterval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			w.poll() // final drain
			return
		case <-w.cs.ctx.Done():
			return
		case <-t.C:
			w.poll()
		}
	}
}

// Stop ends the watcher after one final poll and waits for it.
func (w *subagentWatcher) Stop() {
	if w == nil {
		return
	}
	w.once.Do(func() { close(w.stop) })
	<-w.done
}

func (w *subagentWatcher) poll() {
	if len(w.files) == 0 {
		if tid := w.cs.CurrentSessionID(); tid != "" {
			w.add(tid, "", false)
		}
	}
	for i := 0; i < len(w.files); i++ { // files may grow while polling
		w.pollFile(w.files[i])
	}
}

func (w *subagentWatcher) pollFile(f *subagentWatchFile) {
	if f.path == "" {
		f.path = findSessionFileInCodexHome(w.home, f.threadID)
		if f.path == "" {
			return
		}
		if f.fromEnd {
			if st, err := os.Stat(f.path); err == nil {
				f.offset = st.Size()
			}
		}
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return
	}
	defer fh.Close()
	if _, err := fh.Seek(f.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReader(fh)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return // partial line stays for the next poll
		}
		f.offset += int64(len(line))
		w.handleLine(f, line)
	}
}

type rolloutLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Input     string `json:"input"`
		Arguments string `json:"arguments"`
		Action    *struct {
			Query string `json:"query"`
			URL   string `json:"url"`
		} `json:"action"`
		Item *struct {
			Type          string `json:"type"`
			Kind          string `json:"kind"`
			AgentThreadID string `json:"agent_thread_id"`
			AgentPath     string `json:"agent_path"`
		} `json:"item"`
	} `json:"payload"`
}

func (w *subagentWatcher) handleLine(f *subagentWatchFile, line []byte) {
	if !strings.Contains(string(line), "SubAgentActivity") && (f.label == "" || !strings.Contains(string(line), "call")) {
		return
	}
	var rl rolloutLine
	if json.Unmarshal(line, &rl) != nil {
		return
	}
	p := rl.Payload
	if rl.Type == "event_msg" && p.Item != nil && p.Item.Type == "SubAgentActivity" {
		name := path.Base(strings.TrimSpace(p.Item.AgentPath))
		if f.label != "" {
			name = f.label + "/" + name
		}
		switch p.Item.Kind {
		case "started":
			w.add(p.Item.AgentThreadID, name, false)
			w.emit("▶ " + name)
		case "completed":
			w.emit("✓ " + name)
		default:
			w.emit(p.Item.Kind + " " + name)
		}
		return
	}
	if f.label == "" || rl.Type != "response_item" {
		return
	}
	var what string
	switch p.Type {
	case "custom_tool_call", "function_call":
		input := p.Input
		if input == "" {
			input = p.Arguments
		}
		switch p.Name {
		case "send_message":
			what = "↩ отчёт"
		case "spawn_agent", "wait_agent", "close_agent":
			return // its own SubAgentActivity lines cover this
		default:
			what = summarizeSubagentToolInput(p.Name, input)
		}
	case "web_search_call":
		if p.Action != nil {
			what = "🌐 " + firstNonEmpty(p.Action.Query, p.Action.URL)
		}
	}
	if strings.TrimSpace(what) != "" {
		w.emit(f.label + " › " + what)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var (
	subagentQueryRe = regexp.MustCompile(`\bq\s*["']?\s*:\s*(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)')`)
	subagentOpenRe  = regexp.MustCompile(`ref_id\s*["']?\s*:\s*["'](https?://[^"']+)["']`)
	subagentCmdRe   = regexp.MustCompile(`\bcmd\s*["']?\s*:\s*(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)')`)
)

// summarizeSubagentToolInput turns a subagent's code-mode tool call (JS that
// calls tools.web__run / tools.exec_command) into one short progress phrase.
func summarizeSubagentToolInput(name, input string) string {
	pick := func(re *regexp.Regexp) []string {
		var out []string
		for _, m := range re.FindAllStringSubmatch(input, 3) {
			v := firstNonEmpty(m[1:]...)
			v = strings.ReplaceAll(v, `\"`, `"`)
			if v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	if qs := pick(subagentQueryRe); len(qs) > 0 {
		return "🌐 " + strings.Join(qs, " | ")
	}
	if us := pick(subagentOpenRe); len(us) > 0 {
		return "📄 " + strings.Join(us, " | ")
	}
	if cs := pick(subagentCmdRe); len(cs) > 0 {
		return "💻 " + strings.Join(cs, " ; ")
	}
	if strings.Contains(input, "web__run") {
		return "🌐 web"
	}
	s := strings.Join(strings.Fields(input), " ")
	if len([]rune(s)) > 80 {
		s = string([]rune(s)[:80]) + "…"
	}
	if s == "" {
		return name
	}
	return name + ": " + s
}

func (w *subagentWatcher) emit(text string) {
	evt := core.Event{Type: core.EventToolUse, ToolName: "subagent", ToolInput: text}
	select {
	case w.cs.events <- evt:
	case <-w.cs.ctx.Done():
	}
}
