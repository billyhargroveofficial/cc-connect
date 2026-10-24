package telegram

import (
	"strings"
	"testing"
)

func TestFlattenRichDetails(t *testing.T) {
	in := "Итог\n\n<details>\n<summary>1. Как работает RNN</summary>\n\nтекст\n\n</details>\n"
	got := flattenRichDetails(in)
	if strings.Contains(got, "<") || !strings.Contains(got, "**1. Как работает RNN**") || !strings.Contains(got, "текст") {
		t.Fatalf("got %q", got)
	}
}

func TestCapRichLengthKeepsFooter(t *testing.T) {
	body := strings.Repeat("абзац текста. ", 40) + "\n\n" + strings.Repeat("ещё абзац. ", 400)
	in := body + "\n\n`gpt 6 astra max 30%/500k`"
	got := capRichLength(in, 1000)
	if n := len([]rune(got)); n > 1000 {
		t.Fatalf("len %d > 1000", n)
	}
	if !strings.HasSuffix(got, "`gpt 6 astra max 30%/500k`") || !strings.Contains(got, "…") {
		t.Fatalf("footer/ellipsis missing: %q", got[len(got)-80:])
	}
	if short := "коротко\n\n`f`"; capRichLength(short, 1000) != short {
		t.Fatal("short text changed")
	}
}
