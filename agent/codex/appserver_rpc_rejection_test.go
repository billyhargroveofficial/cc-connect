package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNativeRPCRejectionIsDistinctFromUncertainTransportFailure(t *testing.T) {
	for _, failure := range []string{"negative_ack", "eof", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			output := &nativeSignalWriter{writes: make(chan nativeTestMessage, 1)}
			s := &appServerSession{stdin: output}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.RPC(ctx, "turn/steer", nil, nil) }()
			request := takeNativeTestRequest(t, output.writes, "turn/steer")
			var id int64
			if err := json.Unmarshal(request.ID, &id); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "negative_ack":
				s.handleResponse(rpcResponseEnvelope{ID: id, Error: &rpcError{Code: -32600, Message: "expected turn differs"}})
			case "eof":
				s.rejectPending(io.EOF)
			case "cancel":
				cancel()
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
