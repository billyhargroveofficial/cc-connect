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
	Source      string       `json:"-"`
}

type Model struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Backend string   `json:"backend"`
	Efforts []string `json:"efforts"`
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
