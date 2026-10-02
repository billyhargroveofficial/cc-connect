package core

import (
	"context"
	"encoding/json"
	"time"
)

// NativeEvent keeps provider events intact for clients with a rich transcript.
// Legacy renderers continue to consume Events; they need not implement this.
type NativeEvent struct {
	Backend      string          `json:"backend"`
	Method       string          `json:"method"`
	Params       json.RawMessage `json:"params"`
	RootThreadID string          `json:"rootThreadId,omitempty"`
	RequestID    json.RawMessage `json:"requestId,omitempty"`
	Timestamp    time.Time       `json:"timestamp"`
}

type NativeEventHandler func(NativeEvent)

// SetNativeEventHandler is called before StartSession. The callback must return
// promptly and must not call RPC synchronously from a provider's reader loop.
type NativeEventObserver interface {
	SetNativeEventHandler(NativeEventHandler)
}

type AgentRPCSession interface {
	RPC(ctx context.Context, method string, params any, result any) error
}

// SkillAttachment is a workflow resolved and authorized by the caller.
type SkillAttachment struct {
	Name string
	Path string
}

// AgentSkillSession accepts explicit workflow references in addition to the
// ordinary message. Sessions without this capability retain their Send API.
type AgentSkillSession interface {
	SendWithSkills(prompt string, messageID string, images []ImageAttachment, files []FileAttachment, skills []SkillAttachment) error
}

// RPCRejectionError represents an explicit negative provider acknowledgement.
// Transport errors, timeouts, cancellation and malformed replies must never
// use this type: delivery may already have happened in those cases.
type RPCRejectionError struct {
	Message string
}

func (e *RPCRejectionError) Error() string { return e.Message }

type DynamicToolCall struct {
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	CallID    string          `json:"callId"`
	Namespace string          `json:"namespace,omitempty"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type DynamicToolResult struct {
	ContentItems []map[string]any `json:"contentItems"`
	Success      bool             `json:"success"`
}

type DynamicToolHandler func(context.Context, DynamicToolCall) (DynamicToolResult, error)

type DynamicToolAgent interface {
	SetDynamicTools([]map[string]any, DynamicToolHandler)
}
