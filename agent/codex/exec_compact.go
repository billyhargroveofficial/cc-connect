package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// codex exec has no non-interactive way to compact a thread, so for the exec
// backend the "/compact" command is served by a short-lived `codex app-server`:
// initialize → thread/resume → thread/compact/start, then wait for the
// compaction to finish. The compacted history lands in the same rollout, so
// the next `codex exec resume` continues from it.

const execCompactCommand = "/compact"

var execCompactTimeout = 10 * time.Minute

func isExecCompactPrompt(prompt string) bool {
	return strings.EqualFold(strings.TrimSpace(prompt), execCompactCommand)
}

// runExecCompact performs the compaction and emits one EventResult or
// EventError. It owns the session's turnInFlight guard until it returns.
func (cs *codexSession) runExecCompact() {
	defer cs.turnInFlight.Store(false)
	start := time.Now()
	err := cs.compactViaAppServer()
	if err != nil {
		slog.Warn("codexSession: compact failed", "thread_id", cs.CurrentSessionID(), "error", err)
		select {
		case cs.events <- core.Event{Type: core.EventError, Error: fmt.Errorf("compact: %w", err)}:
		case <-cs.ctx.Done():
		}
		return
	}
	cs.refreshContextUsageFromRollout()
	slog.Info("codexSession: compacted via app-server", "thread_id", cs.CurrentSessionID(), "duration", time.Since(start).Round(time.Millisecond))
	select {
	case cs.events <- core.Event{Type: core.EventResult, SessionID: cs.CurrentSessionID(), Done: true}:
	case <-cs.ctx.Done():
	}
}

func (cs *codexSession) compactViaAppServer() error {
	threadID := cs.CurrentSessionID()
	if threadID == "" {
		return fmt.Errorf("no thread to compact")
	}
	bin, err := resolveCodexExecutable(cs.cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cs.ctx, execCompactTimeout)
	defer cancel()
	args := append(append([]string{}, cs.cliExtraArgs...), "app-server")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = cs.workDir
	prepareCmdForKill(cmd)
	if len(cs.extraEnv) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), cs.extraEnv)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	type msg struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msgs := make(chan msg, 64)
	go func() {
		defer close(msgs)
		r := bufio.NewReaderSize(stdout, 1<<20)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				var m msg
				if json.Unmarshal(line, &m) == nil {
					select {
					case msgs <- m:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	enc := json.NewEncoder(stdin)
	send := func(id int64, method string, params any) error {
		payload := map[string]any{"jsonrpc": "2.0", "method": method}
		if params != nil {
			payload["params"] = params
		}
		if id > 0 {
			payload["id"] = id
		}
		return enc.Encode(payload)
	}
	// await returns when the response to id arrives, or when done(m) reports
	// a terminal notification (id == 0).
	await := func(id int64, done func(msg) (bool, error)) error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case m, ok := <-msgs:
				if !ok {
					return fmt.Errorf("app-server exited")
				}
				if id > 0 && m.ID != nil && *m.ID == id && m.Method == "" {
					if m.Error != nil {
						return fmt.Errorf("%s", m.Error.Message)
					}
					return nil
				}
				if done != nil && m.Method != "" {
					if fin, err := done(m); fin || err != nil {
						return err
					}
				}
			}
		}
	}

	if err := send(1, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "cc-connect-codex-compact", "title": "CC Connect Codex Compact", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		return err
	}
	if err := await(1, nil); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if err := send(0, "initialized", nil); err != nil {
		return err
	}
	if err := send(2, "thread/resume", map[string]any{"threadId": threadID, "excludeTurns": true}); err != nil {
		return err
	}
	if err := await(2, nil); err != nil {
		return fmt.Errorf("thread/resume: %w", err)
	}
	if err := send(3, "thread/compact/start", map[string]any{"threadId": threadID}); err != nil {
		return err
	}
	if err := await(3, nil); err != nil {
		return fmt.Errorf("thread/compact/start: %w", err)
	}
	return await(0, func(m msg) (bool, error) {
		switch m.Method {
		case "thread/compacted", "turn/completed":
			return true, nil
		case "turn/failed", "error":
			return true, fmt.Errorf("%s", strings.TrimSpace(string(m.Params)))
		case "item/completed":
			var p struct {
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if json.Unmarshal(m.Params, &p) == nil && p.Item.Type == "contextCompaction" {
				return true, nil
			}
		}
		return false, nil
	})
}
