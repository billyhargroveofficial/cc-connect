package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractRichLocalMedia(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "shots")
	outside := filepath.Join(root, "secret")
	os.MkdirAll(allowed, 0o755)
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(allowed, "a.png"), []byte("png"), 0o644)
	os.WriteFile(filepath.Join(outside, "b.png"), []byte("png"), 0o644)
	os.Symlink(filepath.Join(outside, "b.png"), filepath.Join(allowed, "link.png"))
	r := &richPlatform{localMediaDirs: parseRichLocalMediaDirs(map[string]any{"rich_local_media_dirs": allowed})}

	md := "x ![скрин](" + filepath.Join(allowed, "a.png") + ") ![чужой](" + filepath.Join(outside, "b.png") + ") ![ссылка](" +
		filepath.Join(allowed, "link.png") + ") ![выход](" + allowed + "/../secret/b.png) ![](https://e.com/c.png)"
	out, media := r.extractRichLocalMedia(md, true)
	if len(media) != 1 || !strings.HasSuffix(media[0].path, "a.png") {
		t.Fatalf("media = %+v", media)
	}
	if !strings.Contains(out, "![скрин](tg://photo?id=m1)") || !strings.Contains(out, "_чужой_") || !strings.Contains(out, "_ссылка_") || !strings.Contains(out, "_выход_") {
		t.Fatalf("out = %s", out)
	}
	if !strings.Contains(out, "![](https://e.com/c.png)") {
		t.Fatalf("remote image must stay: %s", out)
	}
	if edit, _ := r.extractRichLocalMedia(md, false); strings.Contains(edit, "tg://photo") {
		t.Fatalf("edits must not reference uploads: %s", edit)
	}
}
