package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestValidSandboxID(t *testing.T) {
	accept := []string{"abc123", "a-b_c", "a", "A" + strings.Repeat("b", 63)}
	reject := []string{
		"",
		";",
		"a b",
		"$()",
		"a`b",
		"../x",
		"-x",
		"a\nb",
		strings.Repeat("a", 65),
		"x; touch /tmp/pwned",
	}
	for _, id := range accept {
		if !validSandboxID(id) {
			t.Errorf("validSandboxID(%q) = false, want true", id)
		}
	}
	for _, id := range reject {
		if validSandboxID(id) {
			t.Errorf("validSandboxID(%q) = true, want false", id)
		}
	}
}

func TestProxyCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix quoting assertions")
	}
	for _, tc := range []struct{ self, want string }{
		{"/usr/bin/zert", "ProxyCommand='/usr/bin/zert' tunnel vm1"},
		{"/home/my dir/zert", "ProxyCommand='/home/my dir/zert' tunnel vm1"},
		{`/it's/zert`, `ProxyCommand='/it'\''s/zert' tunnel vm1`},
		{"/a;b", "ProxyCommand='/a;b' tunnel vm1"},
		{"/$HOME/zert", "ProxyCommand='/$HOME/zert' tunnel vm1"},
	} {
		if got := proxyCommand(tc.self, "vm1"); got != tc.want {
			t.Errorf("proxyCommand(%q) = %q, want %q", tc.self, got, tc.want)
		}
	}
}
