package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestPlatformSDKCredentialNeverReachesLogSink(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{ReplaceAttr: redactLogAttribute}))
	credential := "123456789:abcdefghijklmnopqrstuvwxyz_0123456789"
	logger.Error("telegram unavailable", "error", errors.New("POST https://api.telegram.org/bot"+credential+"/getUpdates: connection refused"), "detail", slog.GroupValue(slog.String("url", "https://api.telegram.org/bot"+credential+"/sendMessage")))
	if strings.Contains(output.String(), credential) || !strings.Contains(output.String(), "connection refused") {
		t.Fatalf("log did not preserve a useful redacted error: %s", output.String())
	}
}
