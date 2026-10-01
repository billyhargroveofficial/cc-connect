package bots

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// The subprocess implements only Pi's startup probe and prompt acknowledgement.
// It exits while model work would still be in progress, without starting Pi or
// contacting a provider.
func TestRuntimePiPostAckExitHelper(t *testing.T) {
	if os.Getenv("CONNECT_BOTS_PI_EXIT_HELPER") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var command map[string]any
		if json.Unmarshal(scanner.Bytes(), &command) != nil {
			continue
		}
		response := map[string]any{"type": "response", "id": command["id"], "command": command["type"], "success": true}
		if command["type"] == "get_state" {
			response["data"] = map[string]any{"sessionId": "post-ack-exit-session"}
		}
		if err := encoder.Encode(response); err != nil {
			os.Exit(1)
		}
		if command["type"] == "prompt" {
			_ = encoder.Encode(map[string]any{"type": "agent_start"})
			if message := os.Getenv("CONNECT_BOTS_PI_EXIT_STDERR"); message != "" {
				fmt.Fprintln(os.Stderr, message)
			}
			os.Exit(1)
		}
	}
	os.Exit(0)
}

func TestRuntimePiUnexpectedExitAfterPromptAckSettlesWithError(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, stderr := range []string{"", "provider process crashed"} {
		name := "silent"
		if stderr != "" {
			name = "stderr"
		}
		t.Run(name, func(t *testing.T) {
			store, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			bot := store.ListBots()[0]
			bot, err = store.UpdateBot(bot.ID, func(bot *Bot) error {
				bot.Backend, bot.Model = "pi", "test-provider/test-model"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime := NewRuntime(store, RuntimeConfig{AgentOptions: map[string]map[string]any{
				"pi": {
					"cmd":      executable,
					"cli_args": []string{"-test.run=^TestRuntimePiPostAckExitHelper$", "--"},
					"env":      map[string]string{"CONNECT_BOTS_PI_EXIT_HELPER": "1", "CONNECT_BOTS_PI_EXIT_STDERR": stderr},
				},
			}})
			t.Cleanup(func() { _ = runtime.Close() })
			turnID, err := runtime.SendMessage(context.Background(), bot.ID, MessageRequest{Text: "work that crashes after acknowledgement"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := runtime.WaitTurn(ctx, bot.ID, turnID)
			if err != nil {
				t.Fatalf("acknowledged Pi prompt did not settle after process exit: %v", err)
			}
			if result.Status != "error" || result.Error == "" {
				t.Fatalf("unexpected exit was not a terminal error: %+v", result)
			}
			if stderr != "" && !strings.Contains(result.Error, stderr) {
				t.Fatalf("process failure detail was lost: %+v", result)
			}
			if runtime.Busy(bot.ID) {
				t.Fatal("exited Pi process left the bot indefinitely busy")
			}
			updated, err := store.GetBot(bot.ID)
			if err != nil || updated.Status != "error" {
				t.Fatalf("terminal error was not persisted: %+v %v", updated, err)
			}
			if updated.Threads["pi"] != "" {
				t.Fatal("fatal completion persisted a session file the helper never created")
			}
			events, err := store.Events(bot.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			acknowledged := false
			for _, event := range events {
				if event.Type != "native" || event.TurnID != turnID {
					continue
				}
				var native core.NativeEvent
				var response struct {
					Command string `json:"command"`
					Success bool   `json:"success"`
				}
				if json.Unmarshal(event.Data, &native) == nil && json.Unmarshal(native.Params, &response) == nil && native.Method == "response" && response.Command == "prompt" && response.Success {
					acknowledged = true
				}
			}
			if !acknowledged {
				t.Fatal("fixture exited before the native prompt was acknowledged")
			}
		})
	}
}
