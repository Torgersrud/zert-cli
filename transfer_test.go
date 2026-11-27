package main

// The transfer module's own suite — the one place the copy stream's shared
// rules are pinned at their owner's interface, transferFromVM. The module
// never talks to the door — its caller has already resolved credentials
// and the machine — so these tests need no API fake, only the sshPath
// fake-ssh seam and a codec standing in for a copy path's local half.
// What a customer sees through each command (wordings, exit codes, the
// streams themselves) stays pinned by that command's own suite; a rule
// here is pinned once instead of per path.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// streamFile writes body to the file the fake ssh replays on stdout — the
// vm-bound stream of one transfer, codec-agnostic: raw bytes, no tar.
func streamFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stream.bin")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// runTransferFromVM wires one transferFromVM call against the fake ssh:
// stdoutPath is what the child streams back, exit the child's exit code,
// and codec/wording the copy path's local half. Credentials and machine
// are taken as resolved — the module asks nothing of the door.
func runTransferFromVM(t *testing.T, stdoutPath, exit string, codec func(io.Reader) error, wording func(error) error) (compressed int64, err error) {
	t.Helper()
	t.Setenv("FAKE_SSH_STDOUT", stdoutPath)
	fakeSSHStreamingStdout(t)
	if exit != "" {
		t.Setenv("FAKE_SSH_EXIT", exit)
	}
	return transferFromVM(
		&Credentials{Host: "transfer.test", Token: testToken},
		&vmRow{SandboxID: "vmlive", Live: true},
		"tar -czf - -C ~ -- 'out'",
		os.Stderr,
		codec,
		wording,
	)
}

// TestTransferFromVMChildExitOutranksCodec pins the failure-precedence rule
// the module owns: the ssh child's own failure is authoritative — its exit
// code preserved so zert exits with what ssh exited with — even when the
// codec also failed, the way a truncated transfer leaves it failing.
func TestTransferFromVMChildExitOutranksCodec(t *testing.T) {
	sentinel := errors.New("truncated stream")
	compressed, err := runTransferFromVM(t, streamFile(t, "stream bytes"), "1",
		func(r io.Reader) error {
			_, _ = io.ReadAll(r) // the whole stream crossed before the failure
			return sentinel
		},
		func(e error) error { return fmt.Errorf("extracting ~/out: %w", e) })
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("the child's exit code must outrank the codec's failure, got %v", err)
	}
	if compressed != int64(len("stream bytes")) {
		t.Fatalf("compressed = %d, want the bytes that crossed the pipe", compressed)
	}
}

// TestTransferFromVMCodecFailureKeepsTheCallersWording pins the other half
// of the rule: a codec failure under a child that ended clean reaches the
// customer shaped by the copy path's wording constructor, never the
// module's — the module moves streams, it does not phrase failures.
func TestTransferFromVMCodecFailureKeepsTheCallersWording(t *testing.T) {
	sentinel := errors.New("bad archive")
	_, err := runTransferFromVM(t, streamFile(t, "stream"), "",
		func(r io.Reader) error {
			_, _ = io.ReadAll(r)
			return sentinel
		},
		func(e error) error { return fmt.Errorf("extracting ~/out: %w", e) })
	if !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), "extracting ~/out: ") {
		t.Fatalf("a codec failure under a clean child must carry the caller's wording, got %v", err)
	}
}

// TestTransferFromVMClosedPipeIsFallout pins the rule's dismissal: a codec
// reporting the pipe was closed under it — the fallout of a truncated
// transfer, not a failure of the copy — never fails a copy whose child
// ended clean.
func TestTransferFromVMClosedPipeIsFallout(t *testing.T) {
	_, err := runTransferFromVM(t, streamFile(t, "stream"), "",
		func(r io.Reader) error {
			_, _ = io.ReadAll(r)
			return io.ErrClosedPipe
		},
		func(e error) error { return e })
	if err != nil {
		t.Fatalf("a closed pipe under a clean child is fallout, not failure, got %v", err)
	}
}

// TestTransferFromVMSpawnFailureReleasesTheCodec pins the module's
// spawn-failure handling: a child that never started is gone the same way
// a finished one is — the closed pipe hands the codec its end instead of a
// hang — and the spawn failure is the answer.
func TestTransferFromVMSpawnFailureReleasesTheCodec(t *testing.T) {
	sshPath = filepath.Join(t.TempDir(), "no-such-ssh")
	t.Cleanup(func() { sshPath = "ssh" })

	_, err := transferFromVM(
		&Credentials{Host: "transfer.test", Token: testToken},
		&vmRow{SandboxID: "vmlive", Live: true},
		"tar -czf - -C ~ -- 'out'",
		os.Stderr,
		func(r io.Reader) error { _, _ = io.ReadAll(r); return nil },
		func(e error) error { return e })
	if err == nil || !strings.Contains(err.Error(), "spawn ssh") {
		t.Fatalf("want the spawn failure as the answer, got %v", err)
	}
}
