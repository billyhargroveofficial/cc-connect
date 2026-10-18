package core

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLanguagePickWhisperKeepsMostConfident(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := strings.Cut(r.Header.Get("Content-Type"), "boundary=")
		mr := multipart.NewReader(r.Body, params)
		lang := ""
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "language" {
				b, _ := io.ReadAll(p)
				lang = string(b)
			}
		}
		if lang == "ru" { // wrong language: translated, low confidence
			io.WriteString(w, `{"text":"перевод","segments":[{"start":0,"end":3,"avg_logprob":-0.9}]}`)
			return
		}
		io.WriteString(w, `{"text":" original english","segments":[{"start":0,"end":3,"avg_logprob":-0.1}]}`)
	}))
	defer srv.Close()
	p := NewLanguagePickWhisper(NewOpenAIWhisper("k", srv.URL, "m"), []string{"ru", "en"})
	got, err := p.Transcribe(context.Background(), []byte("x"), "ogg", "")
	if err != nil || got != "original english" {
		t.Fatalf("got %q, %v", got, err)
	}
}
