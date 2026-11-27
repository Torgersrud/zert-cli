package main

// The transfer module's own suite — the one place the copy stream's shared
// rules are pinned at their owner's interface, transferFromVM and
// transferToVM. The module never talks to the door — its caller has
// already resolved credentials and the machine — so these tests need no
// API fake, only the sshPath fake-ssh seam and a codec standing in for a
// copy path's local half. What a customer sees through each command
// (wordings, exit codes, the streams themselves) stays pinned by that
// command's own suite; a rule here is pinned once instead of per path.

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
		vmLink{creds: &Credentials{Host: "transfer.test", Token: testToken},
			vm: &vmRow{SandboxID: "vmlive", Live: true}},
		"tar -czf - -C ~ -- 'out'",
		os.Stderr,
		codec,
		wording,
	)
}

// runTransferToVM wires one transferToVM call against the fake ssh: stream
// is the child's data behaviour (one of the stream* constants), exit the
// child's exit code, and pack/wording the copy path's local half.
// Credentials and machine are taken as resolved — the module asks nothing
// of the door.
func runTransferToVM(t *testing.T, stream, exit string, pack func(io.Writer) error, wording func(error) error) (compressed int64, err error) {
	t.Helper()
	fakeSSHScript(t, stream)
	if exit != "" {
		t.Setenv("FAKE_SSH_EXIT", exit)
	}
	return transferToVM(
		vmLink{creds: &Credentials{Host: "transfer.test", Token: testToken},
			vm: &vmRow{SandboxID: "vmlive", Live: true}},
		"tar -xzf - -C ~",
		io.Discard,
		os.Stderr,
		pack,
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

// TestTransferFromVMDrainsTheTailTheCodecLeaves pins the pipe's tail
// drain: a codec may stop reading before the stream ends — tar stops at
// its end-of-archive marker — and the child's stream copy must still
// finish instead of stalling on a pipe nobody reads, with the drained
// bytes still counted in the answer.
func TestTransferFromVMDrainsTheTailTheCodecLeaves(t *testing.T) {
	body := strings.Repeat("z", 256*1024)
	compressed, err := runTransferFromVM(t, streamFile(t, body), "",
		func(r io.Reader) error {
			_, err := io.ReadAll(io.LimitReader(r, 4*1024))
			return err
		},
		func(e error) error { return e })
	if err != nil {
		t.Fatalf("the tail the codec left must not fail the copy: %v", err)
	}
	if compressed != int64(len(body)) {
		t.Fatalf("compressed = %d, want %d: the drained tail still counts", compressed, len(body))
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
		vmLink{creds: &Credentials{Host: "transfer.test", Token: testToken},
			vm: &vmRow{SandboxID: "vmlive", Live: true}},
		"tar -czf - -C ~ -- 'out'",
		os.Stderr,
		func(r io.Reader) error { _, _ = io.ReadAll(r); return nil },
		func(e error) error { return e })
	if err == nil || !strings.Contains(err.Error(), "spawn ssh") {
		t.Fatalf("want the spawn failure as the answer, got %v", err)
	}
}

// TestTransferToVMStreamsTheWholeCodecIntoTheChild pins the to-VM stream's
// happy path at its owner's interface: the codec's whole output crosses
// the module's pipe into the child — the EOF that ends the child's read
// comes from the module, not the codec, so nothing is truncated and
// nothing hangs — and the answer counts every byte that entered the pipe.
func TestTransferToVMStreamsTheWholeCodecIntoTheChild(t *testing.T) {
	body := strings.Repeat("u", 128*1024)
	stdinPath := filepath.Join(t.TempDir(), "stream.bin")
	t.Setenv("FAKE_SSH_STDIN", stdinPath)

	compressed, err := runTransferToVM(t, streamRecordStdin, "",
		func(w io.Writer) error {
			_, err := io.WriteString(w, body)
			return err
		},
		func(e error) error { return e })
	if err != nil {
		t.Fatalf("a clean copy must not fail: %v", err)
	}
	got, rerr := os.ReadFile(stdinPath)
	if rerr != nil || string(got) != body {
		t.Fatalf("the child must receive the codec's whole stream, got %d bytes (read err %v)", len(got), rerr)
	}
	if compressed != int64(len(body)) {
		t.Fatalf("compressed = %d, want %d: every byte that entered the pipe counts", compressed, len(body))
	}
}

// TestTransferToVMChildExitOutranksCodec pins the failure-precedence rule
// on the upload side of the seam: the ssh child's own failure is
// authoritative — its exit code preserved so zert exits with what ssh
// exited with — even when the codec also failed, the way a truncated
// transfer leaves it failing.
func TestTransferToVMChildExitOutranksCodec(t *testing.T) {
	sentinel := errors.New("source vanished mid-pack")
	_, err := runTransferToVM(t, streamSwallowStdin, "1",
		func(w io.Writer) error {
			_, _ = io.WriteString(w, "partial stream")
			return sentinel
		},
		func(e error) error { return fmt.Errorf("packing /abs: %w", e) })
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("the child's exit code must outrank the codec's failure, got %v", err)
	}
}

// TestTransferToVMCodecFailureSurfacesThroughTheChildsReport pins how a
// codec failure reaches the customer on the upload side: the raw error is
// the payload that ends the pipe, so the child's own stdin copy fails with
// it and the customer sees it through the one "run ssh:" report — the
// codec's own words kept, the module's wording constructor uninvoked.
func TestTransferToVMCodecFailureSurfacesThroughTheChildsReport(t *testing.T) {
	sentinel := errors.New("source vanished mid-pack")
	_, err := runTransferToVM(t, streamSwallowStdin, "",
		func(w io.Writer) error {
			_, _ = io.WriteString(w, "partial stream")
			return sentinel
		},
		func(e error) error { return fmt.Errorf("packing /abs: %w", e) })
	if !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), "run ssh: ") {
		t.Fatalf("a codec failure must reach the customer through the child's report, carrying the codec's own words, got %v", err)
	}
}

// TestTransferToVMClosedPipeIsFallout pins the rule's dismissal on the
// upload side: a child that closes its stdin early and ends clean leaves
// the codec's blocked writes failing on a closed pipe — the fallout of a
// truncated transfer, not a failure of the copy. The module's release of
// the pipe's read end is what turns a would-be hang into that dismissed
// fallout.
func TestTransferToVMClosedPipeIsFallout(t *testing.T) {
	_, err := runTransferToVM(t, streamExitRightAway, "",
		func(w io.Writer) error {
			// more than the child's stdin can buffer: some write is
			// always left blocked when the child leaves
			_, err := io.WriteString(w, strings.Repeat("u", 512*1024))
			return err
		},
		func(e error) error { return e })
	if err != nil {
		t.Fatalf("a closed pipe under a clean child is fallout, not failure, got %v", err)
	}
}

// TestTransferToVMSpawnFailureReleasesTheCodec pins the module's
// spawn-failure handling on the upload side: a child that never started is
// gone the same way a finished one is — the closed read end hands the
// codec's blocked write its failure instead of a hang — and the spawn
// failure is the answer.
func TestTransferToVMSpawnFailureReleasesTheCodec(t *testing.T) {
	sshPath = filepath.Join(t.TempDir(), "no-such-ssh")
	t.Cleanup(func() { sshPath = "ssh" })

	_, err := transferToVM(
		vmLink{creds: &Credentials{Host: "transfer.test", Token: testToken},
			vm: &vmRow{SandboxID: "vmlive", Live: true}},
		"tar -xzf - -C ~",
		io.Discard,
		os.Stderr,
		func(w io.Writer) error {
			_, err := io.WriteString(w, "stream no child will ever read")
			return err
		},
		func(e error) error { return e })
	if err == nil || !strings.Contains(err.Error(), "spawn ssh") {
		t.Fatalf("want the spawn failure as the answer, got %v", err)
	}
}
