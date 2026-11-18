package access

import (
	"slices"
	"testing"
)

func TestLevelsAreNested(t *testing.T) {
	r, e, f := LevelActions(LevelRead), LevelActions(LevelEdit), LevelActions(LevelFull)
	for _, a := range r {
		if !slices.Contains(e, a) || !slices.Contains(f, a) {
			t.Fatalf("%s missing from higher levels", a)
		}
	}
	for _, a := range e {
		if !slices.Contains(f, a) {
			t.Fatalf("%s missing from full", a)
		}
	}
	for _, s := range Sensitive {
		if slices.Contains(f, s) {
			t.Fatalf("sensitive %s is part of a level", s)
		}
	}
}

func TestGrant(t *testing.T) {
	cases := []struct {
		g    Grant
		a    Action
		want bool
	}{
		{Grant{Level: LevelRead}, AssetsRead, true},
		{Grant{Level: LevelRead}, ProjectsWrite, false},
		{Grant{Level: LevelRead, Extra: []string{"keys:issue"}}, KeysIssue, false}, // never for read
		{Grant{Level: LevelEdit}, ModerationWrite, true},
		{Grant{Level: LevelEdit}, AssetsDelete, false},
		{Grant{Level: LevelEdit, Extra: []string{"keys:issue"}}, KeysIssue, true},
		{Grant{Level: LevelFull}, AssetsDelete, true},
		{Grant{Level: LevelFull}, StorageManage, false},
		{Grant{Level: LevelFull, Extra: []string{"projects:write"}}, StorageManage, false},
		{Grant{Level: LevelFull, Extra: []string{"agents:manage"}}, AgentsManage, false},
		{Grant{Level: LevelFull}, SessionOnly, false},
		{Grant{Level: LevelFull}, AgentsView, false},
		{Grant{Level: LevelRead}, AgentSelf, true},
		{Grant{Level: "god"}, AssetsRead, false},
	}
	for _, c := range cases {
		if got := c.g.Allows(c.a); got != c.want {
			t.Errorf("%+v.Allows(%s) = %v, want %v", c.g, c.a, got, c.want)
		}
	}
}

func TestRoleAllows(t *testing.T) {
	if !RoleAllows("viewer", AssetsRead) || RoleAllows("viewer", ProjectsWrite) || RoleAllows("viewer", AgentsManage) {
		t.Fatal("viewer")
	}
	if !RoleAllows("viewer", SessionOnly) || !RoleAllows("viewer", AgentsView) {
		t.Fatal("viewer own account / agent list")
	}
	if !RoleAllows("admin", StorageManage) || !RoleAllows("owner", AgentsManage) || RoleAllows("owner", AgentSelf) {
		t.Fatal("admin/owner")
	}
}

func TestIsDestructive(t *testing.T) {
	for _, a := range []Action{AssetsDelete, WebhooksDelete, KeysRevoke, KeysIssue, StorageManage, EdgePublish, BillingManage, CloudConnect} {
		if !IsDestructive(a) {
			t.Errorf("%s should be destructive", a)
		}
	}
	for _, a := range []Action{ProjectsWrite, PresetsWrite, ModerationWrite, AssetsRead} {
		if IsDestructive(a) {
			t.Errorf("%s should not be destructive", a)
		}
	}
}
