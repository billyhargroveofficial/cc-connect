// Package bots is the Connect Bots product layer. Legacy cc-connect engines and
// transports remain independent of this package.
package bots

import (
	"encoding/json"
	"time"
)

type TelegramBinding struct {
	Enabled        bool     `json:"enabled"`
	TokenEnv       string   `json:"tokenEnv"`
	AllowedUserIDs []string `json:"allowedUserIds,omitempty"`
	Username       string   `json:"username,omitempty"`
	Status         string   `json:"status,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type Bot struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Role           string            `json:"role"`
	Avatar         string            `json:"avatar"`
	Chief          bool              `json:"chief"`
	Backend        string            `json:"backend"`
	Model          string            `json:"model"`
	Effort         string            `json:"effort"`
	ServiceTier    string            `json:"serviceTier"`
	WorkDir        string            `json:"workDir"`
	Status         string            `json:"status"`
	Threads        map[string]string `json:"threads"`
	DisabledSkills []string          `json:"disabledSkills"`
	Telegram       *TelegramBinding  `json:"telegram,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

type Event struct {
	Seq    uint64          `json:"seq"`
	BotID  string          `json:"botId"`
	TurnID string          `json:"turnId,omitempty"`
	Type   string          `json:"type"`
	Time   time.Time       `json:"time"`
	Data   json.RawMessage `json:"data"`
}

type Attachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
}

type MessageRequest struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Mode        string       `json:"mode,omitempty"`
	Source      string       `json:"-"`
}

// QueuedMessage is a durable owner message awaiting its own turn. Attachment
// paths never cross the HTTP boundary; the workspace resolves IDs at dispatch.
type QueuedMessage struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Source      string       `json:"source"`
	CreatedAt   time.Time    `json:"createdAt"`
	Status      string       `json:"status"`
}

type MessageReceipt struct {
	TurnID  string `json:"turnId"`
	Status  string `json:"status"`
	QueueID string `json:"queueId,omitempty"`
}

type MessageQueue struct {
	Messages []QueuedMessage `json:"messages"`
	Paused   bool            `json:"paused"`
}

type Model struct {
	ID                 string        `json:"id"`
	Name               string        `json:"name"`
	Backend            string        `json:"backend"`
	Efforts            []string      `json:"efforts"`
	ServiceTiers       []ServiceTier `json:"serviceTiers"`
	DefaultServiceTier string        `json:"defaultServiceTier,omitempty"`
}

// ServiceTier options come from the selected harness's model catalog. An empty
// bot selection leaves the service tier to that harness's automatic selection.
type ServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Capabilities struct {
	Models   []Model                        `json:"models"`
	Voice    bool                           `json:"voice"`
	Backends map[string]BackendCapabilities `json:"backends"`
}

type BackendCapabilities struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Goals     bool   `json:"goals"`
	Subagents bool   `json:"subagents"`
}

type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Scope       string `json:"scope"`
	Enabled     bool   `json:"enabled"`
	Editable    bool   `json:"editable"`
}
