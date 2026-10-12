package telegram

import (
	"encoding/json"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestIncomingRichContent(t *testing.T) {
	const payload = `{"blocks":[
		{"type":"paragraph","text":"Hello "},
		{"type":"photo","photo":[{"file_id":"small","file_unique_id":"s","width":90,"height":90},{"file_id":"large","file_unique_id":"l","width":1280,"height":1280}],"caption":{"text":"A caption"}},
		{"type":"paragraph","text":"world"}
	]}`
	var rich models.RichMessage
	if err := json.Unmarshal([]byte(payload), &rich); err != nil {
		t.Fatalf("unmarshal rich message: %v", err)
	}

	text, photos := incomingRichContent(&rich)
	if want := "Hello \nA caption\nworld"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if len(photos) != 1 || photos[0].FileID != "large" {
		t.Errorf("photos = %+v, want only the largest size", photos)
	}
}

func TestIncomingRichContentNil(t *testing.T) {
	text, photos := incomingRichContent(nil)
	if text != "" || len(photos) != 0 {
		t.Errorf("got %q, %v for a nil rich message", text, photos)
	}
}
