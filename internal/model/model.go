package model

import "time"

type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

type LifecycleStatus string

const (
	StatusActive   LifecycleStatus = "active"
	StatusIdle     LifecycleStatus = "idle"
	StatusInactive LifecycleStatus = "inactive"
	StatusUnknown  LifecycleStatus = "unknown"
)

type Management string

const (
	Managed   Management = "managed"
	Unmanaged Management = "unmanaged"
)

type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
)

type Session struct {
	ID                string          `json:"id"`
	Provider          Provider        `json:"provider"`
	ProviderSessionID string          `json:"providerSessionId"`
	Management        Management      `json:"management"`
	Visibility        Visibility      `json:"visibility"`
	Status            LifecycleStatus `json:"status"`
	StatusSource      string          `json:"statusSource"`
	CWD               string          `json:"cwd,omitempty"`
	Source            string          `json:"source,omitempty"`
	MetadataPath      string          `json:"-"`
	LastSeenAt        time.Time       `json:"lastSeenAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

func SessionID(provider Provider, providerSessionID string) string {
	return string(provider) + ":" + providerSessionID
}

type NodeIdentity struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Platform    string    `json:"platform"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from,omitempty"`
	To        string    `json:"to"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

