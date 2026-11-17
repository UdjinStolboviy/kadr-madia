package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAdminBootstrapPrintsOnceAndIsIdempotent(t *testing.T) {
	t.Setenv("KADR_DEV", "1")
	t.Chdir(t.TempDir())
	var out, errb bytes.Buffer
	if code := run([]string{"migrate", "up"}, nil, &out, &errb); code != 0 {
		t.Fatalf("migrate: %s", errb.String())
	}
	out.Reset()
	if code := run([]string{"admin", "bootstrap", "--email", "owner@example.com", "--url", "https://admin.example.com"}, nil, &out, &errb); code != 0 {
		t.Fatalf("bootstrap: %s", errb.String())
	}
	for _, want := range []string{"owner@example.com", "https://admin.example.com", "Password:"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := run([]string{"admin", "bootstrap"}, nil, &out, &errb); code != 0 || strings.Contains(out.String(), "Password:") {
		t.Fatalf("second run must not print credentials (code %d):\n%s", code, out.String())
	}
}
