package main

import "testing"

func TestCheckHost(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://a.example", "https://a.example", false},
		{"a.example", "https://a.example", false},
		{"http://a.example", "", true},
		{"http://localhost:8000", "http://localhost:8000", false},
		{"http://127.0.0.1", "http://127.0.0.1", false},
		{"http://127.0.0.53:80", "http://127.0.0.53:80", false},
		{"http://[::1]:8000", "http://[::1]:8000", false},
		{"ftp://x", "", true},
		{"https://user:pw@a.example", "", true},
		{"https://a.example/", "https://a.example", false},
		{"https://a.example/x/", "https://a.example", false},
		{"https://a.example/?q=1#frag", "https://a.example", false},
		{"https://user@a.example", "", true},
		{"", "", true},
	} {
		got, err := checkHost(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("checkHost(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("checkHost(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("checkHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCheckHostAllowInsecure(t *testing.T) {
	got, err := checkHostAllowInsecure("http://a.example", true)
	if err != nil || got != "http://a.example" {
		t.Fatalf("insecure http: got %q, err %v", got, err)
	}
	if _, err := checkHostAllowInsecure("ftp://a.example", true); err == nil {
		t.Fatal("ftp must be rejected even with insecure")
	}
}
