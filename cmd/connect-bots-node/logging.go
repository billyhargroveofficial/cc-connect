package main

import (
	"log/slog"
	"os"
	"regexp"
)

var telegramLogCredential = regexp.MustCompile(`[0-9]{5,}:[A-Za-z0-9_-]{10,}`)

func configureLogging() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			var text string
			switch attr.Value.Kind() {
			case slog.KindString:
				text = attr.Value.String()
			case slog.KindAny:
				err, ok := attr.Value.Any().(error)
				if !ok {
					return attr
				}
				text = err.Error()
			default:
				return attr
			}
			attr.Value = slog.StringValue(telegramLogCredential.ReplaceAllString(text, "[redacted]"))
			return attr
		},
	})))
}
