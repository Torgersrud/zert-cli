package main

import (
	"strings"
	"testing"
)

func TestVMPathEscapesSlash(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"vm123", "/v1/vm/vm123"},
		{"a/b", "/v1/vm/a%2Fb"},
		{"../me", "/v1/vm/..%2Fme"},
		{"a b", "/v1/vm/a%20b"},
	} {
		if got := vmPath(tc.id); got != tc.want {
			t.Errorf("vmPath(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestKillRejectsTraversalID(t *testing.T) {
	// validation runs before requireLogin, so no creds are needed here
	if err := cmdKill([]string{"../me"}); err == nil || !strings.Contains(err.Error(), "invalid vm id") {
		t.Fatalf("want invalid-vm-id error, got %v", err)
	}
}
