package telegram

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChatLogIgnored(t *testing.T) {
	f := filepath.Join(t.TempDir(), "ignore.json")
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	os.WriteFile(f, []byte(`{"1":{"until":"`+future+`"},"2":{"until":"`+past+`"},"3":{"until":"`+future+`"}}`), 0o644)
	cl, err := newChatLog(map[string]any{"ignore_file": f, "ignore_exempt": "3"})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]bool{1: true, 2: false, 3: false, 4: false} {
		if got := cl.ignored(id); got != want {
			t.Errorf("ignored(%d) = %v, want %v", id, got, want)
		}
	}
	os.WriteFile(f, []byte(`{"5":{"until":"`+time.Now().Add(time.Hour).Format("2006-01-02T15:04:05-0700")+`"}}`), 0o644)
	if !cl.ignored(5) {
		t.Error("BSD date offset format must parse")
	}
	os.WriteFile(f, []byte(`not json`), 0o644)
	if cl.ignored(1) {
		t.Error("malformed file must ignore nobody")
	}
}
