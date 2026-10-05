package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func stubLogin(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/login" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}))
}

func overridePrompts(t *testing.T, email, password string) {
	t.Helper()
	oldLine, oldSecret := promptLine, promptSecret
	promptLine = func(string) (string, error) { return email, nil }
	promptSecret = func(string) (string, error) { return password, nil }
	t.Cleanup(func() { promptLine, promptSecret = oldLine, oldSecret })
}

func TestLoginSuccessSavesCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts := stubLogin(t, 200, map[string]any{"token": "tok-abc", "ssh_pubkey": nil})
	defer ts.Close()
	overridePrompts(t, "bob@example.com", "hunter2")

	if err := cmdLogin([]string{"--host", ts.URL}); err != nil {
		t.Fatal(err)
	}
	c, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "tok-abc" || c.Email != "bob@example.com" || c.Host != ts.URL {
		t.Fatalf("saved credentials wrong: %+v", *c)
	}
}

func TestLoginBadPassword(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts := stubLogin(t, 401, map[string]string{"detail": "invalid credentials"})
	defer ts.Close()
	overridePrompts(t, "bob@example.com", "wrong")

	err := cmdLogin([]string{"--host", ts.URL})
	if err == nil || !strings.Contains(err.Error(), "invalid credentials") {
		t.Fatalf("want invalid-credentials error, got %v", err)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials saved despite failed login")
	}
}

func TestLoginHostPrecedence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts := stubLogin(t, 200, map[string]any{"token": "tok-abc"})
	defer ts.Close()
	overridePrompts(t, "bob@example.com", "x")
	t.Setenv("ZERT_HOST", ts.URL)

	if err := cmdLogin(nil); err != nil {
		t.Fatal(err)
	}
	c, _ := loadCredentials()
	if c.Host != ts.URL {
		t.Fatalf("ZERT_HOST not honored: %s", c.Host)
	}
}

func TestLoginRejectsInsecureHostBeforeNetwork(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	overridePrompts(t, "bob@example.com", "x")

	// no server needed: checkHost must fail before any dial
	if err := cmdLogin([]string{"--host", "http://example.com"}); err == nil {
		t.Fatal("expected error for non-loopback http host")
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials saved despite rejected host")
	}
}

func TestMeSessionExpiredMapping(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"invalid or expired token"}`, 401)
	}))
	defer ts.Close()
	_, err := getMe(api{host: ts.URL, token: "stale"})
	if err == nil || !strings.Contains(err.Error(), "session expired — run `zert login`") {
		t.Fatalf("want session-expired message, got %v", err)
	}
}
