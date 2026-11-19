package admin

import (
	"encoding/json"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// JSON shapes of docs/ADMIN_API.md. Times are RFC 3339 strings in UTC.

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

type adminDTO struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	Role        string  `json:"role"`
	TOTPEnabled bool    `json:"totp_enabled"`
	CreatedAt   string  `json:"created_at"`
	LastLoginAt *string `json:"last_login_at"`
}

func toAdminDTO(a *db.Admin) adminDTO {
	return adminDTO{ID: a.ID, Email: a.Email, Role: a.Role, TOTPEnabled: a.TOTPEnabled(),
		CreatedAt: ts(a.CreatedAt), LastLoginAt: tsp(a.LastLoginAt)}
}

type projectDTO struct {
	ID               string   `json:"id"`
	Slug             string   `json:"slug"`
	Name             string   `json:"name"`
	Environment      string   `json:"environment"`
	StorageProfileID *string  `json:"storage_profile_id"`
	AllowedOrigins   []string `json:"allowed_origins"`
	QuotaBytes       *int64   `json:"quota_bytes"`
	UsedBytes        int64    `json:"used_bytes"`
	DefaultMode      string   `json:"default_mode"`
	Disabled         bool     `json:"disabled"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

func toProjectDTO(p *db.Project) projectDTO {
	origins := p.AllowedOrigins
	if origins == nil {
		origins = []string{}
	}
	return projectDTO{ID: p.ID, Slug: p.Slug, Name: p.Name, Environment: p.Environment,
		StorageProfileID: p.StorageProfileID, AllowedOrigins: origins, QuotaBytes: p.QuotaBytes,
		UsedBytes: p.UsedBytes, DefaultMode: p.DefaultMode, Disabled: p.DisabledAt != nil, CreatedAt: ts(p.CreatedAt), UpdatedAt: ts(p.UpdatedAt)}
}

type apiKeyDTO struct {
	ID         string   `json:"id"`
	ProjectID  string   `json:"project_id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	Namespaces []string `json:"namespaces"`
	LastUsedAt *string  `json:"last_used_at"`
	ExpiresAt  *string  `json:"expires_at"`
	RevokedAt  *string  `json:"revoked_at"`
	CreatedAt  string   `json:"created_at"`
}

func toAPIKeyDTO(k *db.APIKey) apiKeyDTO {
	scopes := k.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	namespaces := k.Namespaces
	if namespaces == nil {
		namespaces = []string{}
	}
	return apiKeyDTO{ID: k.ID, ProjectID: k.ProjectID, Name: k.Name, Prefix: k.Prefix, Scopes: scopes, Namespaces: namespaces,
		LastUsedAt: tsp(k.LastUsedAt), ExpiresAt: tsp(k.ExpiresAt), RevokedAt: tsp(k.RevokedAt), CreatedAt: ts(k.CreatedAt)}
}

type storageProfileDTO struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Provider       string  `json:"provider"`
	Endpoint       string  `json:"endpoint"`
	Region         string  `json:"region"`
	PublicBucket   string  `json:"public_bucket"`
	PrivateBucket  string  `json:"private_bucket"`
	PublicBaseURL  string  `json:"public_base_url"`
	PrivateBaseURL *string `json:"private_base_url"`
	AccessKeyID    string  `json:"access_key_id"` // masked
	IsPlatform     bool    `json:"is_platform"`
	LastTestAt     *string `json:"last_test_at"`
	LastTestOK     *bool   `json:"last_test_ok"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

type presetDTO struct {
	ID             string          `json:"id"`
	Slug           string          `json:"slug"`
	Kind           string          `json:"kind"`
	Description    string          `json:"description"`
	CurrentVersion int             `json:"current_version"`
	Params         json.RawMessage `json:"params"`
	CreatedAt      string          `json:"created_at"`
}

func toPresetDTO(p *db.Preset) presetDTO {
	params := p.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	return presetDTO{ID: p.ID, Slug: p.Slug, Kind: p.Kind, Description: p.Description,
		CurrentVersion: p.CurrentVersion, Params: params, CreatedAt: ts(p.CreatedAt)}
}

type auditDTO struct {
	ID        int64           `json:"id"`
	ActorType string          `json:"actor_type"`
	ActorID   *string         `json:"actor_id"`
	ProjectID *string         `json:"project_id"`
	Action    string          `json:"action"`
	Target    *string         `json:"target"`
	Details   json.RawMessage `json:"details"`
	IP        *string         `json:"ip"`
	CreatedAt string          `json:"created_at"`
}

func toAuditDTO(e *db.AuditEntry) auditDTO {
	details := e.Details
	if len(details) == 0 {
		details = json.RawMessage("null")
	}
	return auditDTO{ID: e.ID, ActorType: e.ActorType, ActorID: e.ActorID, ProjectID: e.ProjectID, Action: e.Action,
		Target: e.Target, Details: details, IP: e.IP, CreatedAt: ts(e.CreatedAt)}
}

type itemsResponse[T any] struct {
	Items []T `json:"items"`
}
