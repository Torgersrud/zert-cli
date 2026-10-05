package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"escape", "\x1b[31mred\x1b[0m", "?[31mred?[0m"},
		{"crlf-header", "5\r\nX-Evil: 1", "5?\nX-Evil: 1"},
		{"keeps-tab", "a\tb", "a\tb"},
		{"keeps-newline", "a\nb", "a\nb"},
		{"plain", "quota reached", "quota reached"},
	} {
		if got := sanitize(tc.in); got != tc.want {
			t.Errorf("%s: sanitize(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestSanitizeTruncates(t *testing.T) {
	got := sanitize(strings.Repeat("a", 10000))
	if n := len([]rune(got)); n != 500 {
		t.Fatalf("len = %d, want 500 runes", n)
	}
}

func TestSanitizeNoControlRemains(t *testing.T) {
	got := sanitize("x\x00\x01\x7f\ty\nz")
	for _, r := range got {
		if r == '\x00' || r == '\x01' || r == '\x7f' {
			t.Fatalf("control rune %#x survived sanitization: %q", r, got)
		}
	}
}

func TestMappedSanitizesRetryAfter(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	resp.Header.Set("retry-after", "10\r\nInjected: yes")
	err := mapped(resp, nil)
	if strings.ContainsAny(err.Error(), "\r") {
		t.Fatalf("CR leaked through retry-after: %q", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "too many requests, retry after 10?") {
		t.Fatalf("unexpected message: %q", err.Error())
	}
}
