package core

import (
	"strings"
	"testing"
)

type steerAgentSession struct {
	stubAgentSession
	steer bool
	sent  []string
}

func (s *steerAgentSession) Send(p string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.sent = append(s.sent, p)
	return nil
}
func (s *steerAgentSession) SupportsMidTurnSteer() bool { return s.steer }

func TestTrySteerMidTurn(t *testing.T) {
	e := newTestEngine()
	p := &stubPlatformEngine{n: "test"}
	as := &steerAgentSession{steer: true}
	state := &interactiveState{agentSession: as, busySession: &Session{}}
	msg := &Message{Content: "добавь ещё про погоду", SessionKey: "k"}

	if e.trySteerMidTurnLocked(state, p, msg) {
		t.Fatal("steer must be off by default")
	}
	e.SetSteerMidTurn(true)
	if !e.trySteerMidTurnLocked(state, p, msg) || len(as.sent) != 1 || as.sent[0] != msg.Content {
		t.Fatalf("expected steer, sent=%v", as.sent)
	}
	if e.trySteerMidTurnLocked(state, p, &Message{Content: "/stop"}) {
		t.Fatal("commands must not be steered")
	}
	state.busySession = nil
	if e.trySteerMidTurnLocked(state, p, msg) {
		t.Fatal("no steer while the agent is starting")
	}
	state.busySession = &Session{}
	state.pendingMessages = []queuedMessage{{content: "earlier"}}
	if e.trySteerMidTurnLocked(state, p, msg) {
		t.Fatal("no steer past queued messages")
	}
	state.pendingMessages = nil
	as.steer = false
	if e.trySteerMidTurnLocked(state, p, msg) {
		t.Fatal("agents without support must queue")
	}
}

func TestMergeQueuedBatch(t *testing.T) {
	sp := func(content, userID, userName, _, _, _ string) string { return "[" + userName + "] " + content }
	m := mergeQueuedBatch([]queuedMessage{
		{messageID: "1", content: "привет", userName: "vg", images: []ImageAttachment{{MimeType: "image/png"}}},
		{messageID: "2", content: "как дела", userName: "craacky", replyCtx: "ctx2"},
	}, sp)
	if m.messageID != "2" || m.replyCtx != "ctx2" || len(m.images) != 1 {
		t.Fatalf("merged = %+v", m)
	}
	for _, want := range []string{"пришло 2 сообщений", "[vg] привет", "[craacky] как дела", "сообщение 2"} {
		if !strings.Contains(m.content, want) {
			t.Errorf("missing %q in %q", want, m.content)
		}
	}
}

func TestBusyNoticeOverride(t *testing.T) {
	e := &Engine{i18n: NewI18n(LangEnglish)}
	if got := e.busyNotice("", MsgMessageQueued); got != e.i18n.T(MsgMessageQueued) {
		t.Fatalf("default queued notice = %q", got)
	}
	e.SetBusyNotices("в очереди", "в текущий шаг")
	if got := e.busyNotice(e.queuedNotice, MsgMessageQueued); got != "в очереди" {
		t.Fatalf("queued override = %q", got)
	}
	if got := e.busyNotice(e.steeredNotice, MsgMessageSteered); got != "в текущий шаг" {
		t.Fatalf("steered override = %q", got)
	}
}
