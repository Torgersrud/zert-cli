package main

import (
	"strings"
	"testing"
)

func TestKillRejectsTraversalID(t *testing.T) {
	// validation runs before requireLogin, so no creds are needed here
	if err := cmdKill([]string{"../me"}); err == nil || !strings.Contains(err.Error(), "invalid vm id") {
		t.Fatalf("want invalid-vm-id error, got %v", err)
	}
}
