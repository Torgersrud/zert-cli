package main

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestKillRejectsTraversalID(t *testing.T) {
	// validation runs before requireLogin, so no creds are needed here
	if err := cmdKill([]string{"../me"}); err == nil || !strings.Contains(err.Error(), "invalid vm id") {
		t.Fatalf("want invalid-vm-id error, got %v", err)
	}
}

func TestKillMultipleRejectsAnyBadID(t *testing.T) {
	if err := cmdKill([]string{"vm1", "../me"}); err == nil || !strings.Contains(err.Error(), "invalid vm id") {
		t.Fatalf("want invalid-vm-id error, got %v", err)
	}
}

func TestKillNoArgs(t *testing.T) {
	if err := cmdKill(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("want usage error, got %v", err)
	}
}

func deletedIDs(t *testing.T, fs *fakeServer) []string {
	t.Helper()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	got := append([]string(nil), fs.deleted...)
	sort.Strings(got)
	return got
}

func TestKillAllDeletesEveryVM(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.live = []string{"vm1", "vm2", "vm3"}
	fs.mu.Unlock()

	if err := cmdKill([]string{"all"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"vm1", "vm2", "vm3"}
	if got := deletedIDs(t, fs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("deleted %v, want %v", got, want)
	}
}

func TestKillAllAcceptsQuotedStar(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.live = []string{"vm1"}
	fs.mu.Unlock()

	if err := cmdKill([]string{"*"}); err != nil {
		t.Fatal(err)
	}
	if got := deletedIDs(t, fs); len(got) != 1 || got[0] != "vm1" {
		t.Fatalf("deleted %v, want [vm1]", got)
	}
}

func TestKillAllEmptyIsNoop(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)

	if err := cmdKill([]string{"all"}); err != nil {
		t.Fatal(err)
	}
	if got := deletedIDs(t, fs); len(got) != 0 {
		t.Fatalf("no vms must mean no DELETEs, got %v", got)
	}
}

func TestKillAllContinuesAfterFailure(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.live = []string{"vm1", "vm2"}
	fs.deleteFail = map[string]int{"vm2": 500}
	fs.mu.Unlock()

	err := cmdKill([]string{"all"})
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("want exit 1 on partial failure, got %v", err)
	}
	got := deletedIDs(t, fs)
	sort.Strings(got)
	if len(got) != 2 || got[0] != "vm1" || got[1] != "vm2" {
		t.Fatalf("every id must be attempted, got %v", got)
	}
}

func TestKillMultipleExplicitIDs(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)

	if err := cmdKill([]string{"vm1", "vm2"}); err != nil {
		t.Fatal(err)
	}
	got := deletedIDs(t, fs)
	if len(got) != 2 || got[0] != "vm1" || got[1] != "vm2" {
		t.Fatalf("deleted %v, want [vm1 vm2]", got)
	}
}

func TestKillAllPassesStderr(t *testing.T) {
	fs := newFakeServer(t)
	setupHome(t, fs.URL)
	fs.mu.Lock()
	fs.live = []string{"vm1"}
	fs.deleteFail = map[string]int{"vm1": 401}
	fs.mu.Unlock()

	var buf strings.Builder
	err := killIDs(api{host: fs.URL}, &buf, []string{"vm1"})
	if err == nil {
		t.Fatal("want error from failing kill")
	}
	if !strings.Contains(buf.String(), "session expired") {
		t.Fatalf("want mapped 401 message on stderr, got %q", buf.String())
	}
}
