package core

import (
	"errors"
	"testing"
	"time"
)

func TestProgressRateLimitWait(t *testing.T) {
	wait, ok := progressRateLimitWait(errors.New("telegram: edit message: too many requests, Too Many Requests: retry after 28: retry_after 28"))
	if !ok || wait < 28*time.Second || wait > 29*time.Second {
		t.Fatalf("wait=%v ok=%v", wait, ok)
	}
	if _, ok := progressRateLimitWait(errors.New("message is not modified")); ok {
		t.Fatal("non-429 error treated as rate limit")
	}
}
