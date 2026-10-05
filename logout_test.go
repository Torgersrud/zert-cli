package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const notConfirmedMsg = "logged out locally (server did not confirm token revocation; tokens expire after 30 days)"

func TestLogoutConfirmedByServer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var gotMethod, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != logoutPath {
			http.Error(w, "nope", 404)
			return
		}
		gotMethod = r.Method
		gotAuth = r.Header.Get("authorization")
		w.WriteHeader(200)
	}))
	defer ts.Close()
	if err := saveCredentials(&Credentials{Host: ts.URL, Email: "e", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	msg := captureStderr(t, func() {
		if err := cmdLogout(nil); err != nil {
			t.Fatal(err)
		}
	})
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer t" {
		t.Errorf("authorization = %q, want Bearer t", gotAuth)
	}
	if got := strings.TrimSpace(msg); got != "logged out" {
		t.Errorf("stderr = %q, want %q", got, "logged out")
	}
	if strings.Contains(msg, "locally") {
		t.Errorf("stderr should not mention local-only logout: %q", msg)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials still loadable after logout")
	}
}

func TestLogoutServerNotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 404)
	}))
	defer ts.Close()
	if err := saveCredentials(&Credentials{Host: ts.URL, Email: "e", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	msg := captureStderr(t, func() {
		if err := cmdLogout(nil); err != nil {
			t.Fatal(err)
		}
	})
	if got := strings.TrimSpace(msg); got != notConfirmedMsg {
		t.Errorf("stderr = %q, want %q", got, notConfirmedMsg)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials still loadable after logout")
	}
}

func TestLogoutConnectionRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveCredentials(&Credentials{Host: "http://127.0.0.1:1", Email: "e", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	msg := captureStderr(t, func() {
		if err := cmdLogout(nil); err != nil {
			t.Fatal(err)
		}
	})
	if got := strings.TrimSpace(msg); got != notConfirmedMsg {
		t.Errorf("stderr = %q, want %q", got, notConfirmedMsg)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials still loadable after logout")
	}
}

func TestLogoutWithoutCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	msg := captureStderr(t, func() {
		if err := cmdLogout(nil); err != nil {
			t.Fatal(err)
		}
	})
	if got := strings.TrimSpace(msg); got != notConfirmedMsg {
		t.Errorf("stderr = %q, want %q", got, notConfirmedMsg)
	}
}
