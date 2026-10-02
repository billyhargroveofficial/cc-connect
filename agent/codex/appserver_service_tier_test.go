package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestAppServerServiceTierAppliesToStartResumeAndNextTurn(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, tier := range []string{"priority", "", "legacy"} {
			t.Run(fmt.Sprintf("resume=%t/tier=%s", resume, tier), func(t *testing.T) {
				root, requests := nativeTestManagedServer(t)
				opts := map[string]any{
					"cmd": os.Args[0], "backend": "app_server", "app_server_url": "managed",
					"work_dir": t.TempDir(), "codex_home": root, "model": "gpt-6-sol", "native_events": true,
				}
				if tier != "legacy" {
					opts["service_tier"] = tier
				}
				agent, err := New(opts)
				if err != nil {
					t.Fatal(err)
				}
				threadID, method := "", "thread/start"
				if resume {
					threadID, method = "our-thread", "thread/resume"
				}
				session, err := agent.StartSession(context.Background(), threadID)
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				request := takeNativeTestRequest(t, requests, method)
				selected, present := request.Params["serviceTier"]
				switch tier {
				case "priority":
					if selected != "priority" {
						t.Fatalf("thread request lost the tier: %v", request.Params)
					}
				case "":
					if !present || selected != nil {
						t.Fatalf("automatic thread request must use null: %v", request.Params)
					}
					if resume {
						clear := takeNativeTestRequest(t, requests, "thread/settings/update")
						selected, present = clear.Params["serviceTier"]
						if !present || selected != nil || clear.Params["threadId"] != "our-thread" {
							t.Fatalf("automatic resume did not clear the saved tier: %v", clear.Params)
						}
					}
				case "legacy":
					if present {
						t.Fatalf("unconfigured legacy client changed its tier: %v", request.Params)
					}
				}
				if err := session.Send("Continue", "message", nil, nil); err != nil {
					t.Fatal(err)
				}
				turn := takeNativeTestRequest(t, requests, "turn/start")
				selected, present = turn.Params["serviceTier"]
				if tier == "priority" && selected != "priority" {
					t.Fatalf("tier was not sent to inference: %v", turn.Params)
				}
				if tier != "priority" && present {
					t.Fatalf("automatic/legacy turn overrode the tier: %v", turn.Params)
				}
			})
		}
	}
}

func TestAppServerServiceTierSettingsClearCannotBeOverriddenByStaleState(t *testing.T) {
	output := &nativeSignalWriter{writes: make(chan nativeTestMessage, 1)}
	s := &appServerSession{stdin: output, serviceTier: "priority", serviceTierConfigured: true}
	s.threadID.Store("root")
	done := make(chan error, 1)
	go func() {
		done <- s.RPC(context.Background(), "thread/settings/update", map[string]any{"threadId": "root", "serviceTier": nil}, nil)
	}()
	request := takeNativeTestRequest(t, output.writes, "thread/settings/update")
	var id int64
	if err := json.Unmarshal(request.ID, &id); err != nil {
		t.Fatal(err)
	}
	s.handleResponse(rpcResponseEnvelope{ID: id, Result: json.RawMessage(`{}`)})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if tier, configured := s.getServiceTier(); tier != "" || !configured {
		t.Fatalf("clear RPC left stale priority override: %q %t", tier, configured)
	}
	s.handleNotification("thread/settings/updated", json.RawMessage(`{"threadId":"root","threadSettings":{"serviceTier":"priority"}}`))
	if tier, _ := s.getServiceTier(); tier != "priority" {
		t.Fatalf("settings notification lost selected tier: %q", tier)
	}
	s.handleNotification("thread/settings/updated", json.RawMessage(`{"threadId":"foreign","threadSettings":{"serviceTier":null}}`))
	if tier, _ := s.getServiceTier(); tier != "priority" {
		t.Fatalf("foreign thread changed tier: %q", tier)
	}
	s.handleNotification("thread/settings/updated", json.RawMessage(`{"threadId":"root","threadSettings":{"serviceTier":null}}`))
	if tier, _ := s.getServiceTier(); tier != "" {
		t.Fatalf("null notification did not clear selected tier: %q", tier)
	}
}
