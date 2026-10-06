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

func TestDetailSanitizes(t *testing.T) {
	if got := detail([]byte(`{"detail":"a\u001b[31mb"}`)); strings.ContainsAny(got, "\x1b") {
		t.Fatalf("escape survived: %q", got)
	}
	if got := detail([]byte("")); got != "unexpected server response" {
		t.Fatalf("got %q", got)
	}
}
