package main

import (
	"strings"
	"testing"
)

func countPrefix(entries []string, prefix string) []string {
	var got []string
	for _, kv := range entries {
		if strings.HasPrefix(kv, prefix) {
			got = append(got, kv)
		}
	}
	return got
}

func TestChildEnv(t *testing.T) {
	base := []string{"PATH=/bin", "ZERT_HOST=http://old", "ZERT_TUNNEL_TOKEN=old", "OTHER=1"}
	got := childEnv(base, "tok", "https://new")

	if h := countPrefix(got, "ZERT_HOST="); len(h) != 1 || h[0] != "ZERT_HOST=https://new" {
		t.Fatalf("ZERT_HOST entries = %v, want exactly [ZERT_HOST=https://new]", h)
	}
	if tk := countPrefix(got, "ZERT_TUNNEL_TOKEN="); len(tk) != 1 || tk[0] != "ZERT_TUNNEL_TOKEN=tok" {
		t.Fatalf("ZERT_TUNNEL_TOKEN entries = %v, want exactly [ZERT_TUNNEL_TOKEN=tok]", tk)
	}
	for _, kv := range got {
		if kv == "ZERT_HOST=http://old" || kv == "ZERT_TUNNEL_TOKEN=old" {
			t.Fatalf("stale value not removed: %s", kv)
		}
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "OTHER=1") {
		t.Fatalf("unrelated entries dropped: %v", got)
	}
}

func TestResolveTunnelTargetMatchingHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveCredentials(&Credentials{Host: "http://127.0.0.1:9000", Token: "tok", Email: "e"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZERT_TUNNEL_TOKEN", "")
	t.Setenv("ZERT_HOST", "http://127.0.0.1:9000")

	host, token, err := resolveTunnelTarget()
	if err != nil {
		t.Fatal(err)
	}
	if host != "http://127.0.0.1:9000" || token != "tok" {
		t.Fatalf("got (%q, %q), want (http://127.0.0.1:9000, tok)", host, token)
	}
}

func TestResolveTunnelTargetMismatchedHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveCredentials(&Credentials{Host: "https://a.example", Token: "tok", Email: "e"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZERT_TUNNEL_TOKEN", "")
	t.Setenv("ZERT_HOST", "https://b.example")

	_, _, err := resolveTunnelTarget()
	if err == nil || !strings.Contains(err.Error(), "ZERT_HOST differs from the host you logged in to") {
		t.Fatalf("want ZERT_HOST-differs error, got %v", err)
	}
}

func TestResolveTunnelTargetEnvTokenEnvHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ZERT_TUNNEL_TOKEN", "envtok")
	t.Setenv("ZERT_HOST", "http://127.0.0.1:1234")

	host, token, err := resolveTunnelTarget()
	if err != nil {
		t.Fatal(err)
	}
	if host != "http://127.0.0.1:1234" || token != "envtok" {
		t.Fatalf("got (%q, %q), want (http://127.0.0.1:1234, envtok)", host, token)
	}
}
