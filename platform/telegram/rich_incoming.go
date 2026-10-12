package telegram

import (
	"encoding/json"
	"strings"

	"github.com/go-telegram/bot/models"
)

// incomingRichContent flattens a Telegram rich message into plain text for the
// agent and collects the largest size of every photo embedded in it.
func incomingRichContent(rich *models.RichMessage) (string, []models.PhotoSize) {
	raw, _ := json.Marshal(rich)
	var doc any
	_ = json.Unmarshal(raw, &doc)
	var photos []models.PhotoSize

	var walk func(v any) string
	walk = func(v any) string {
		switch x := v.(type) {
		case string:
			return x
		case []any:
			var parts []string
			for _, item := range x {
				parts = append(parts, walk(item))
			}
			return strings.Join(parts, "")
		case map[string]any:
			if sizes, ok := x["photo"].([]any); ok && len(sizes) > 0 {
				data, _ := json.Marshal(sizes[len(sizes)-1])
				var photo models.PhotoSize
				if json.Unmarshal(data, &photo) == nil && photo.FileID != "" {
					photos = append(photos, photo)
				}
			}
			var parts []string
			for _, key := range []string{"label", "summary", "text", "expression", "alternative_text", "blocks", "items", "rows", "cells", "caption", "credit"} {
				child, ok := x[key]
				if !ok {
					continue
				}
				if list, ok := child.([]any); ok && key != "text" {
					for _, item := range list {
						if s := walk(item); s != "" {
							parts = append(parts, s)
						}
					}
				} else if s := walk(child); s != "" {
					parts = append(parts, s)
				}
			}
			if url, ok := x["url"].(string); ok {
				parts = append(parts, url)
			}
			return strings.Join(parts, "\n")
		}
		return ""
	}
	return walk(doc), photos
}
