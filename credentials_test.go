package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := &Credentials{Host: "https://zert.example.com", Email: "bob@example.com", Token: "tok-123"}
	if err := saveCredentials(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if *got != *want {
		t.Fatalf("round trip mismatch: %+v != %+v", *got, *want)
	}
}

func TestCredentialsInsecureRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := &Credentials{Host: "http://zert.example.com", Email: "bob@example.com", Token: "tok-123", Insecure: true}
	if err := saveCredentials(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if *got != *want {
		t.Fatalf("insecure flag lost: %+v != %+v", *got, *want)
	}
}

func TestCredentialsFileMode0600(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveCredentials(&Credentials{Host: "h", Email: "e", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	path, _ := credentialsPath()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0600 {
		t.Fatalf("mode = %o, want 0600", perm)
	}
}

func TestCredentialsMissingFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "zert"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zert", "credentials"), []byte("host = h\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("expected error for token-less credentials file")
	}
}

func TestLogoutDeletesFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveCredentials(&Credentials{Host: "h", Email: "e", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := deleteCredentials(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredentials(); err == nil {
		t.Fatal("credentials still loadable after logout")
	}
}
