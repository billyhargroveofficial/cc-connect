package pi

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNativeRPCRejectionIsDistinctFromUncertainTransportFailure(t *testing.T) {
	for _, failure := range []string{"negative_ack", "eof", "cancel", "malformed_ack"} {
		t.Run(failure, func(t *testing.T) {
			s, commands := nativeRPCFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.RPC(ctx, "steer", nil, nil) }()
			command := receiveNativeCommand(t, commands)
			switch failure {
			case "negative_ack":
				s.handleEvent(map[string]any{"type": "response", "id": command["id"], "command": "steer", "success": false, "error": "invalid input"})
			case "eof":
				s.failPendingRPC(io.EOF)
			case "cancel":
				cancel()
			case "malformed_ack":
				s.handleEvent(map[string]any{"type": "response", "id": command["id"], "command": "steer"})
			}
			err := <-done
			var rejected *core.RPCRejectionError
			if errors.As(err, &rejected) != (failure == "negative_ack") {
				t.Fatalf("uncertain delivery classified as rejection: %T %v", err, err)
			}
			if failure == "eof" && !errors.Is(err, io.EOF) {
				t.Fatalf("transport cause was lost: %v", err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause was lost: %v", err)
			}
		})
	}
}
