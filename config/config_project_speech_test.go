package config

import (
	"testing"

	toml "github.com/BurntSushi/toml"
)

func TestProjectSpeechOverride(t *testing.T) {
	var cfg Config
	src := `
[speech]
enabled = true
provider = "openai"

[[projects]]
name = "a"

[[projects]]
name = "b"
[projects.speech]
enabled = true
provider = "groq"
language = "ru"
[projects.speech.groq]
api_key = "k"
model = "whisper-large-v3-turbo"
`
	if _, err := toml.Decode(src, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Projects[0].Speech != nil {
		t.Fatalf("project a should inherit global speech")
	}
	s := cfg.Projects[1].Speech
	if s == nil || s.Provider != "groq" || s.Groq.APIKey != "k" || s.Groq.Model != "whisper-large-v3-turbo" {
		t.Fatalf("project b speech = %+v", s)
	}
}
