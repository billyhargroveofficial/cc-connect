package main

import (
	"log/slog"
	"os"
	"regexp"
)

// Telegram SDK errors can contain credential-bearing /bot<token>/ URLs before
// the platform hands an error to our manager. Redact them at the process sink.
var telegramLogCredential = regexp.MustCompile(`[0-9]{5,}:[A-Za-z0-9_-]{10,}`)

func configureLogging() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: redactLogAttribute,
	})))
}

func redactLogAttribute(_ []string, attr slog.Attr) slog.Attr {
	var text string
	switch attr.Value.Kind() {
	case slog.KindString:
		text = attr.Value.String()
	case slog.KindAny:
		if err, ok := attr.Value.Any().(error); ok {
			text = err.Error()
		} else {
			return attr
		}
	default:
		return attr
	}
	attr.Value = slog.StringValue(telegramLogCredential.ReplaceAllString(text, "[redacted]"))
	return attr
}
