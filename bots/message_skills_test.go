package bots

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type skillRecordingAgent struct {
	*runtimeFakeAgent
	skills chan []core.SkillAttachment
}

func (a *skillRecordingAgent) StartSession(ctx context.Context, resume string) (core.AgentSession, error) {
	session, err := a.runtimeFakeAgent.StartSession(ctx, resume)
	if err != nil {
		return nil, err
	}
	return &skillRecordingSession{runtimeFakeSession: session.(*runtimeFakeSession), skills: a.skills}, nil
}

type skillRecordingSession struct {
	*runtimeFakeSession
	skills chan []core.SkillAttachment
}

func (s *skillRecordingSession) SendWithSkills(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment, skills []core.SkillAttachment) error {
	s.skills <- append([]core.SkillAttachment(nil), skills...)
	return s.Send(prompt, messageID, images, files)
}

func configureSkillRuntime(runtime *Runtime, factory *runtimeFakeFactory, workspace *Workspace) <-chan []core.SkillAttachment {
	workspace.Configure(WorkspaceConfig{NativeSkillDirs: []string{}, BackendSkillDirs: map[string][]string{}, SystemSkillDirs: []string{}})
	runtime.cfg.ResolveSkills = workspace.ResolveSkills
	skills := make(chan []core.SkillAttachment, 8)
	runtime.cfg.AgentFactory = func(backend string, options map[string]any) (core.Agent, error) {
		agent, err := factory.create(backend, options)
		if err == nil && backend == "codex" {
			return &skillRecordingAgent{runtimeFakeAgent: agent.(*runtimeFakeAgent), skills: skills}, nil
		}
		return agent, err
	}
	return skills
}

func createMessageSkill(t *testing.T, workspace *Workspace, botID string) Skill {
	t.Helper()
	skill, err := workspace.CreateSkill(botID, "review", "---\nname: review\ndescription: Review the work.\n---\n\nRead the changes and verify their correctness.\n")
	if err != nil {
		t.Fatal(err)
	}
	return skill
}

func skillRef(skill Skill) SkillAttachment {
	return SkillAttachment{ID: skill.ID, Name: skill.Name, Path: skill.Path}
}

func userMessageSkills(t *testing.T, store *Store, botID, turnID string) []SkillAttachment {
	t.Helper()
	events, err := store.Events(botID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != "message" || events[i].TurnID != turnID {
			continue
		}
		var message struct {
			Role   string
			Skills []SkillAttachment
		}
		if err := json.Unmarshal(events[i].Data, &message); err != nil {
			t.Fatal(err)
		}
		if message.Role == "user" {
			return message.Skills
		}
	}
	t.Fatal("missing user message")
	return nil
}

func TestWorkspaceAttachedSkillsCanonicalizeAndDeduplicateWithoutTrustingClientMetadata(t *testing.T) {
	_, workspace, bot := workspaceFixture(t)
	skill := createMessageSkill(t, workspace, "")
	alias := filepath.Join(bot.WorkDir, "review-link.md")
	if err := os.Symlink(skill.Path, alias); err != nil {
		t.Fatal(err)
	}
	input := []SkillAttachment{{ID: skill.ID, Name: "forged", Path: "/etc/passwd"}, {Path: alias}}
	before := append([]SkillAttachment(nil), input...)
	got, err := workspace.ResolveSkills(bot.ID, input)
	if err != nil || !reflect.DeepEqual(got, []SkillAttachment{skillRef(skill)}) {
		t.Fatalf("canonical references: %+v %v", got, err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("resolver mutated the caller's references")
	}
	botAfter, _ := workspace.store.GetBot(bot.ID)
	if len(botAfter.DisabledSkills) != 0 {
		t.Fatal("message attachment changed permanent skill enablement")
	}
}

func TestRuntimeSkillAttachmentsRejectForeignDisabledDeletedAndUnavailableWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"foreign_bot", "foreign_tenant", "disabled", "deleted", "arbitrary_path", "name_only", "no_resolver", "too_many"} {
		t.Run(scenario, func(t *testing.T) {
			store, runtime, factory, bot := setupRuntime(t)
			workspace := NewWorkspace(store)
			configureSkillRuntime(runtime, factory, workspace)
			skill := createMessageSkill(t, workspace, bot.ID)
			input := []SkillAttachment{skillRef(skill)}
			switch scenario {
			case "foreign_bot":
				other, err := store.CreateBot(Bot{Name: "Other"})
				if err != nil {
					t.Fatal(err)
				}
				input = []SkillAttachment{skillRef(createMessageSkill(t, workspace, other.ID))}
			case "foreign_tenant":
				_, foreignWorkspace, _ := workspaceFixture(t)
				input = []SkillAttachment{skillRef(createMessageSkill(t, foreignWorkspace, ""))}
			case "disabled":
				if _, err := workspace.SetDisabledSkills(bot.ID, []string{skill.ID}); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := os.Remove(skill.Path); err != nil {
					t.Fatal(err)
				}
			case "arbitrary_path":
				input = []SkillAttachment{{ID: "/etc/passwd", Name: skill.Name, Path: skill.Path}}
			case "name_only":
				input = []SkillAttachment{{Name: skill.Name}}
			case "no_resolver":
				runtime.cfg.ResolveSkills = nil
			case "too_many":
				input = make([]SkillAttachment, maxMessageSkills+1)
				for i := range input {
					input[i] = skillRef(skill)
				}
			}
			beforeBot, _ := store.GetBot(bot.ID)
			beforeEvents, _ := store.Events(bot.ID, 0)
			if _, err := runtime.SubmitMessage(context.Background(), bot.ID, MessageRequest{Text: "inspect", Skills: input}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid selection accepted: %v", err)
			}
			afterBot, _ := store.GetBot(bot.ID)
			afterEvents, _ := store.Events(bot.ID, 0)
			if !reflect.DeepEqual(beforeBot, afterBot) || !reflect.DeepEqual(beforeEvents, afterEvents) {
				t.Fatal("invalid selection changed the bot or journal")
			}
			factory.mu.Lock()
			created := len(factory.created)
			factory.mu.Unlock()
			if created != 0 {
				t.Fatal("invalid selection launched a provider")
			}
		})
	}
}

func TestRuntimeSkillOnlyMessagesUseNativeCodexInputOrExactPiWorkflowReference(t *testing.T) {
	for _, backend := range []string{"codex", "pi"} {
		t.Run(backend, func(t *testing.T) {
			store, runtime, factory, bot := setupRuntime(t)
			if backend == "pi" {
				bot, _ = store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend, bot.Model = "pi", "deepseek/flash"; return nil })
			}
			workspace := NewWorkspace(store)
			nativeSkills := configureSkillRuntime(runtime, factory, workspace)
			skill := createMessageSkill(t, workspace, bot.ID)
			receipt, err := runtime.SubmitMessage(context.Background(), bot.ID, MessageRequest{Skills: []SkillAttachment{{ID: skill.ID, Name: "forged", Path: "/etc/passwd"}}})
			if err != nil {
				t.Fatal(err)
			}
			sent := nextRuntimeSend(t, factory)
			if backend == "codex" {
				if got := <-nativeSkills; !reflect.DeepEqual(got, []core.SkillAttachment{{Name: skill.Name, Path: skill.Path}}) {
					t.Fatalf("native skill identities lost: %+v", got)
				}
				if sent.prompt != "" {
					t.Fatalf("structured input duplicated into prompt: %q", sent.prompt)
				}
			} else if !strings.Contains(sent.prompt, skill.Path) || !strings.Contains(sent.prompt, "$review") || strings.Contains(sent.prompt, "/etc/passwd") {
				t.Fatalf("Pi lost the selected installed workflow: %q", sent.prompt)
			}
			if got := userMessageSkills(t, store, bot.ID, receipt.TurnID); !reflect.DeepEqual(got, []SkillAttachment{skillRef(skill)}) {
				t.Fatalf("message skill journal: %+v", got)
			}
			sent.session.complete("reviewed")
			waitRuntimeTurn(t, runtime, bot.ID, receipt.TurnID)
		})
	}
}

func TestRuntimeHandoffKeepsHistoricalSkillNamesWithoutReactivatingThem(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	workspace := NewWorkspace(store)
	nativeSkills := configureSkillRuntime(runtime, factory, workspace)
	skill := createMessageSkill(t, workspace, bot.ID)
	receipt, err := runtime.SubmitMessage(context.Background(), bot.ID, MessageRequest{Skills: []SkillAttachment{skillRef(skill)}})
	if err != nil {
		t.Fatal(err)
	}
	first := nextRuntimeSend(t, factory)
	if got := <-nativeSkills; len(got) != 1 || got[0].Name != skill.Name {
		t.Fatalf("initial native skill: %+v", got)
	}
	first.session.complete("workflow finished")
	waitRuntimeTurn(t, runtime, bot.ID, receipt.TurnID)

	if err := runtime.WithIdleBot(bot.ID, func() error {
		_, err := store.UpdateBot(bot.ID, func(bot *Bot) error {
			bot.Backend, bot.Model = "pi", "deepseek/deepseek-flash"
			return nil
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	receipt, err = runtime.SubmitMessage(context.Background(), bot.ID, MessageRequest{Text: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	second := nextRuntimeSend(t, factory)
	if !strings.Contains(second.prompt, `Historical skill attachments (metadata only; do not activate automatically): "$review"`) {
		t.Fatalf("handoff lost inert historical skill metadata: %q", second.prompt)
	}
	if strings.Contains(second.prompt, skill.Path) || strings.Contains(second.prompt, "Read each SKILL.md") {
		t.Fatalf("handoff reactivated a stale skill selection: %q", second.prompt)
	}
	second.session.complete("continued")
	waitRuntimeTurn(t, runtime, bot.ID, receipt.TurnID)
}

func TestRuntimeQueuedSkillsSurviveRestartAndResolveAgainAtDispatch(t *testing.T) {
	for _, change := range []string{"unchanged", "disabled", "deleted"} {
		t.Run(change, func(t *testing.T) {
			store, runtime, factory, bot := setupRuntime(t)
			workspace := NewWorkspace(store)
			configureSkillRuntime(runtime, factory, workspace)
			skill := createMessageSkill(t, workspace, "")
			submitRuntimeMessage(t, runtime, bot.ID, "active")
			nextRuntimeSend(t, factory)
			queued, err := runtime.SubmitMessage(context.Background(), bot.ID, MessageRequest{Text: "review queued work", Skills: []SkillAttachment{skillRef(skill)}})
			if err != nil {
				t.Fatal(err)
			}
			root := store.Root()
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			factory = &runtimeFakeFactory{sends: make(chan runtimeFakeSend, 8)}
			recovered := NewRuntime(reopened, RuntimeConfig{AgentFactory: factory.create, AgentOptions: runtimeFakeAgentOptions()})
			defer recovered.Close()
			workspace = NewWorkspace(reopened)
			nativeSkills := configureSkillRuntime(recovered, factory, workspace)
			queue := runtimeMessageQueue(t, recovered, bot.ID)
			if !queue.Paused || len(queue.Messages) != 1 || !reflect.DeepEqual(queue.Messages[0].Skills, []SkillAttachment{skillRef(skill)}) {
				t.Fatalf("recovered skill queue: %+v", queue)
			}
			queue.Messages[0].Skills[0].Name = "caller mutation"
			if again := runtimeMessageQueue(t, recovered, bot.ID); again.Messages[0].Skills[0].Name != skill.Name {
				t.Fatal("queue snapshot mutated the stored selection")
			}
			if change == "disabled" {
				if _, err := workspace.SetDisabledSkills(bot.ID, []string{skill.ID}); err != nil {
					t.Fatal(err)
				}
			} else if change == "deleted" {
				if err := os.Remove(skill.Path); err != nil {
					t.Fatal(err)
				}
			}
			if err := recovered.ResumeQueue(context.Background(), bot.ID); err != nil {
				t.Fatal(err)
			}
			if change != "unchanged" {
				if result := waitRuntimeTurn(t, recovered, bot.ID, queued.TurnID); result.Status != "failed" {
					t.Fatalf("invalidated queued skill reached dispatch: %+v", result)
				}
				select {
				case unexpected := <-factory.sends:
					t.Fatalf("invalidated skill sent: %q", unexpected.prompt)
				default:
				}
				return
			}
			sent := nextRuntimeSend(t, factory)
			if got := <-nativeSkills; !reflect.DeepEqual(got, []core.SkillAttachment{{Name: skill.Name, Path: skill.Path}}) {
				t.Fatalf("dispatch lost restored skill: %+v", got)
			}
			if got := userMessageSkills(t, reopened, bot.ID, queued.TurnID); !reflect.DeepEqual(got, []SkillAttachment{skillRef(skill)}) {
				t.Fatalf("dispatched skill journal: %+v", got)
			}
			sent.session.complete("review complete")
			waitRuntimeTurn(t, recovered, bot.ID, queued.TurnID)
		})
	}
}

func TestRuntimeDirectAndQueuedSteeringCarryValidatedSkillsToTheirHarness(t *testing.T) {
	for _, backend := range []string{"codex", "pi"} {
		for _, delivery := range []string{"direct", "queued"} {
			t.Run(backend+"_"+delivery, func(t *testing.T) {
				store, runtime, factory, bot := setupRuntime(t)
				if backend == "pi" {
					bot, _ = store.UpdateBot(bot.ID, func(bot *Bot) error { bot.Backend, bot.Model = "pi", "deepseek/flash"; return nil })
				}
				workspace := NewWorkspace(store)
				configureSkillRuntime(runtime, factory, workspace)
				skill := createMessageSkill(t, workspace, bot.ID)
				active := submitRuntimeMessage(t, runtime, bot.ID, "active")
				sent := nextRuntimeSend(t, factory)
				sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "owned-active"}})
				message := MessageRequest{Text: "review this", Skills: []SkillAttachment{{ID: skill.ID, Name: "forged", Path: "/etc/passwd"}}, Mode: "steer"}
				if delivery == "queued" {
					message.Mode = "queue"
				}
				receipt, err := runtime.SubmitMessage(context.Background(), bot.ID, message)
				if err != nil {
					t.Fatal(err)
				}
				if delivery == "queued" {
					if _, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, receipt.QueueID); err != nil {
						t.Fatal(err)
					}
					if len(runtimeMessageQueue(t, runtime, bot.ID).Messages) != 0 {
						t.Fatal("accepted skill steer remained queued")
					}
				}
				sent.session.mu.Lock()
				params := sent.session.rpcParams[len(sent.session.rpcParams)-1]
				sent.session.mu.Unlock()
				if backend == "codex" {
					input := params["input"].([]any)
					if len(input) != 2 || !reflect.DeepEqual(input[1], map[string]any{"type": "skill", "name": skill.Name, "path": skill.Path}) {
						t.Fatalf("native steer skill: %+v", input)
					}
				} else if prompt := params["message"].(string); !strings.Contains(prompt, "$review") || !strings.Contains(prompt, skill.Path) || strings.Contains(prompt, "/etc/passwd") {
					t.Fatalf("Pi steer lost exact skill identity: %q", prompt)
				}
				if got := userMessageSkills(t, store, bot.ID, active.TurnID); !reflect.DeepEqual(got, []SkillAttachment{skillRef(skill)}) {
					t.Fatalf("steered skill journal: %+v", got)
				}
				sent.session.complete("steered")
				waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
			})
		}
	}
}

func TestRuntimeQueuedSteeringRechecksDisabledSkillsBeforeJournalingIntent(t *testing.T) {
	store, runtime, factory, bot := setupRuntime(t)
	workspace := NewWorkspace(store)
	configureSkillRuntime(runtime, factory, workspace)
	skill := createMessageSkill(t, workspace, bot.ID)
	active := submitRuntimeMessage(t, runtime, bot.ID, "active")
	sent := nextRuntimeSend(t, factory)
	sent.session.native("turn/started", map[string]any{"threadId": sent.session.id, "turn": map[string]any{"id": "owned-active"}})
	queued, err := runtime.SubmitMessage(context.Background(), bot.ID, MessageRequest{Text: "review", Skills: []SkillAttachment{skillRef(skill)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.SetDisabledSkills(bot.ID, []string{skill.ID}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Events(bot.ID, 0)
	if _, err := runtime.SteerQueuedMessage(context.Background(), bot.ID, queued.QueueID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("disabled queued steer accepted: %v", err)
	}
	after, _ := store.Events(bot.ID, 0)
	if !reflect.DeepEqual(before, after) || len(runtimeMessageQueue(t, runtime, bot.ID).Messages) != 1 {
		t.Fatal("invalid steer changed the queue or journal")
	}
	if err := runtime.CancelQueuedMessage(context.Background(), bot.ID, queued.QueueID); err != nil {
		t.Fatal(err)
	}
	sent.session.complete("done")
	waitRuntimeTurn(t, runtime, bot.ID, active.TurnID)
}

func TestCapabilitiesAdvertiseSkillAttachmentSupportOnlyWithScopedResolver(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "supported"}[supported], func(t *testing.T) {
			store, runtime, factory, bot := setupRuntime(t)
			workspace := NewWorkspace(store)
			if supported {
				configureSkillRuntime(runtime, factory, workspace)
			}
			server, err := NewTenantServer(store, runtime, ServerConfig{Workspace: workspace})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			response := httptest.NewRecorder()
			server.APIHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/studio/capabilities?botId="+bot.ID, nil))
			var payload map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != http.StatusOK || payload["skillAttachments"] != supported {
				t.Fatalf("skill capability contract: %d %s %v", response.Code, response.Body.String(), err)
			}
		})
	}
}
