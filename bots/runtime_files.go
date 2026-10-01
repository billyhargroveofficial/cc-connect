package bots

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (r *Runtime) publishFiles(botID string, args json.RawMessage) (any, error) {
	var input struct {
		Paths   []string `json:"paths"`
		Caption string   `json:"caption"`
	}
	if err := decodeToolArguments(args, &input); err != nil {
		return nil, err
	}
	if r.cfg.PublishFiles == nil {
		return nil, fmt.Errorf("file publishing is unavailable")
	}
	if len(input.Paths) == 0 || len(input.Paths) > 10 {
		return nil, fmt.Errorf("publish between 1 and 10 files")
	}
	state, err := r.state(botID)
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	turn := state.current
	state.mu.Unlock()
	if turn == nil {
		return nil, ErrConflict
	}
	attachments, err := r.cfg.PublishFiles(botID, input.Paths)
	if err != nil {
		return nil, err
	}
	// Only opaque IDs and authenticated download URLs enter the conversation.
	for i := range attachments {
		attachments[i].Path = ""
	}
	caption := strings.TrimSpace(input.Caption)
	if caption == "" {
		caption = "Готовые файлы"
	}
	if _, err := r.store.AppendEvent(botID, turn.id, "message", map[string]any{
		"role": "assistant", "content": caption, "attachments": attachments,
		"source": "files", "backend": turn.bot.Backend, "artifact": true,
	}); err != nil {
		return nil, err
	}
	return map[string]any{"attachments": attachments, "caption": caption}, nil
}
