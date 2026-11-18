// Package access defines the permission model of the Admin API: the actions
// a caller may perform, the three agent access levels and the sensitive
// actions that are never part of a level (ADR 0014, docs/AGENTS.md).
//
// Every Admin API route is annotated with exactly one Action. Admin sessions
// are checked by role (viewer → read actions only); agent tokens by their
// level plus explicitly granted sensitive actions.
package access

import "slices"

// Action is a permission checked on one Admin API route.
type Action string

// Read actions (level "read").
const (
	ProjectsRead  Action = "projects:read"  // projects, presets, compression modes
	AssetsRead    Action = "assets:read"    // asset explorer, previews, moderation queue
	WebhooksRead  Action = "webhooks:read"  // endpoints and deliveries
	JobsRead      Action = "jobs:read"      // the job queue
	AnalyticsRead Action = "analytics:read" // analytics, billing, rates
	KeysRead      Action = "keys:read"      // API keys, signing keys, cloud connections (metadata only)
	StorageRead   Action = "storage:read"   // storage profiles (credentials masked)
	SystemRead    Action = "system:read"    // health, capabilities, versions
	AuditRead     Action = "audit:read"     // the audit log
)

// Edit actions (level "edit"): create and change, never remove.
const (
	ProjectsWrite   Action = "projects:write"   // create projects, change settings
	PresetsWrite    Action = "presets:write"    // preset versions, resets, playground runs
	WebhooksWrite   Action = "webhooks:write"   // create and test endpoints, retry deliveries
	JobsWrite       Action = "jobs:write"       // retry / cancel jobs
	ModerationWrite Action = "moderation:write" // approve / flag / reject assets
)

// Delete actions (level "full").
const (
	AssetsDelete   Action = "assets:delete"
	WebhooksDelete Action = "webhooks:delete"
	KeysRevoke     Action = "keys:revoke" // revoke API keys and cloud connections, retire signing keys
)

// Sensitive actions: they hand out secrets, touch credentials or money, and
// are never part of a level. An agent gets them only by explicit grant.
const (
	KeysIssue     Action = "keys:issue"     // create API keys and signing keys (returns secrets)
	StorageManage Action = "storage:manage" // create / change / test / delete storage profiles
	EdgePublish   Action = "edge:publish"   // read the edge config (secrets) and publish it
	BillingManage Action = "billing:manage" // change billing rates
	CloudConnect  Action = "cloud:connect"  // connect cloud drives (OAuth tokens)
)

// Pseudo-actions of routes that agents never reach through a grant.
const (
	// SessionOnly: the signed-in human's own account (logout, TOTP, passkeys).
	SessionOnly Action = "session:only"
	// AgentsView: list agent tokens and approvals (any admin role).
	AgentsView Action = "agents:view"
	// AgentsManage: issue, change and revoke agent tokens, decide approvals,
	// flip the global switch (owner and admin roles).
	AgentsManage Action = "agents:manage"
	// AgentSelf: an agent reading its own identity and approvals.
	AgentSelf Action = "agent:self"
)

// Levels.
const (
	LevelRead = "read"
	LevelEdit = "edit"
	LevelFull = "full"
)

var (
	readActions = []Action{ProjectsRead, AssetsRead, WebhooksRead, JobsRead, AnalyticsRead, KeysRead, StorageRead, SystemRead, AuditRead}
	editActions = []Action{ProjectsWrite, PresetsWrite, WebhooksWrite, JobsWrite, ModerationWrite}
	fullActions = []Action{AssetsDelete, WebhooksDelete, KeysRevoke}

	// Sensitive lists the actions that may be granted on top of a level.
	Sensitive = []Action{KeysIssue, StorageManage, EdgePublish, BillingManage, CloudConnect}
)

// Levels lists the valid levels from least to most privileged.
var Levels = []string{LevelRead, LevelEdit, LevelFull}

// ValidLevel reports whether l is a known level.
func ValidLevel(l string) bool { return slices.Contains(Levels, l) }

// ValidSensitive reports whether a is a grantable sensitive action.
func ValidSensitive(a string) bool { return slices.Contains(Sensitive, Action(a)) }

// LevelActions returns the actions a level grants.
func LevelActions(level string) []Action {
	switch level {
	case LevelRead:
		return slices.Clone(readActions)
	case LevelEdit:
		return slices.Concat(readActions, editActions)
	case LevelFull:
		return slices.Concat(readActions, editActions, fullActions)
	}
	return nil
}

// IsRead reports whether a only reads data.
func IsRead(a Action) bool { return slices.Contains(readActions, a) }

// IsDestructive reports whether a removes data or access, or hands out
// secrets. These are the actions held for approval in "destructive" mode.
func IsDestructive(a Action) bool {
	return slices.Contains(fullActions, a) || slices.Contains(Sensitive, a)
}

// Grant is the effective permission set of an agent token.
type Grant struct {
	Level string
	Extra []string
}

// Allows reports whether the grant permits a.
func (g Grant) Allows(a Action) bool {
	switch a {
	case "", SessionOnly, AgentsView, AgentsManage:
		return false
	case AgentSelf:
		return true
	}
	if slices.Contains(LevelActions(g.Level), a) {
		return true
	}
	// Sensitive actions require at least the edit level: a read-only agent
	// can never be turned into one that changes credentials.
	return g.Level != LevelRead && slices.Contains(g.Extra, string(a)) && slices.Contains(Sensitive, a)
}

// Actions returns every action the grant permits, sorted.
func (g Grant) Actions() []string {
	out := []string{}
	for _, a := range slices.Concat(LevelActions(g.Level), Sensitive) {
		if g.Allows(a) && !slices.Contains(out, string(a)) {
			out = append(out, string(a))
		}
	}
	slices.Sort(out)
	return out
}

// RoleAllows reports whether an admin role permits a. Owners and admins may
// do everything; viewers may only read (and manage their own sign-in).
func RoleAllows(role string, a Action) bool {
	switch a {
	case SessionOnly, AgentsView:
		return role == "owner" || role == "admin" || role == "viewer"
	case AgentSelf, "":
		return false
	}
	switch role {
	case "owner", "admin":
		return true
	case "viewer":
		return IsRead(a)
	}
	return false
}
