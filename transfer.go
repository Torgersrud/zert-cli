package main

// Transfer module — the one answer to "how does zert move a one-shot copy
// stream through a vm". Both copy directions run through it, each with its
// own codec: extract's unpack reads a stream out of the vm, upload's pack
// writes one in. What every path through this module gets, once:
//
//   - the pipe: the codec runs concurrently with the ssh child over a pipe
//     whose ends this module alone opens, counts, aborts and closes — and
//     whose unread tail it drains on the way out of the vm — a caller
//     never touches one;
//   - the spawn-failure release: a child that never started is gone the
//     same way a finished one is, the closed pipe handing the codec its
//     end instead of a hang;
//   - the failure-precedence rule, pinned at this interface: the ssh
//     child's failure is authoritative — its exit code preserved as an
//     *exitError — and the codec's failure reaches the customer only when
//     the child ended clean, a closed pipe dismissed as the fallout of a
//     truncated transfer.
//
// The module never owns the codec's wording. The error a codec returns is
// the payload that aborts the pipe, raw — it can resurface through the
// child's own "run ssh:" report, where it must read as the codec said it —
// and the wording constructor shapes only the failure that survives
// precedence, so each copy path keeps its own words for its own failure.
// Naming the machine, and the listing and session prologue around a copy,
// stay with the caller: that is the door module's (#30), not the stream's.

import (
	"errors"
	"io"
)

// countReader counts the bytes that leave the transfer module's pipe — the
// compressed total a copy path reports to the customer, drained tail
// included.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// countWriter counts the bytes that enter the transfer module's pipe — the
// compressed total a copy path reports to the customer.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// transferResult applies the one precedence rule every copy stream reads
// its failures by: the ssh child's failure is authoritative — its exit
// code preserved, so zert exits with what ssh exited with — and the
// codec's failure reaches the customer only when the child ended clean,
// shaped by the copy path's wording constructor. The closed pipe never
// reaches the customer at all: it is the fallout of a truncated transfer,
// not a failure of the copy.
func transferResult(sshErr, codecErr error, wording func(error) error) error {
	if sshErr != nil {
		return sshErr
	}
	if codecErr != nil && !errors.Is(codecErr, io.ErrClosedPipe) {
		return wording(codecErr)
	}
	return nil
}

// transferFromVM runs one one-shot copy stream out of the vm: remoteCmd's
// stdout crosses a module-owned pipe into unpack, the copy path's codec
// (extract's untar), and the answer is the bytes that crossed the pipe and
// the one failure that outranks the other. The pipe is the module's —
// unpack gets a reader, never a pipe end — and a child that never started
// is released the same way a finished one is: the write end closes after
// execInVM returns either way, handing the codec its end. compressed
// counts every byte that left the pipe, so a copy path's report and the
// stream agree.
func transferFromVM(creds *Credentials, vm *vmRow, remoteCmd string, stderr io.Writer, unpack func(io.Reader) error, wording func(error) error) (compressed int64, err error) {
	pr, pw := io.Pipe()
	cr := &countReader{r: pr}
	codecDone := make(chan error, 1)
	go func() {
		err := unpack(cr)
		if err == nil {
			// a codec may stop reading before the stream ends — tar stops at
			// its end-of-archive marker — so drain the tail: the child's
			// stdout copy must finish instead of stalling on a pipe nobody
			// reads anymore, and the drained bytes still count
			_, err = io.Copy(io.Discard, cr)
		}
		if err != nil {
			// abort: close the read end so a still-writing child sees EPIPE
			// and exec's stdout copy unblocks Wait — raw, the codec's own
			// words, because this payload can resurface through the child's
			// failure
			_ = pr.CloseWithError(err)
		}
		codecDone <- err
	}()
	// the codec runs concurrently with the transfer: it simply blocks on
	// the pipe until the ssh child starts writing
	sshErr := execInVM(creds, vm, remoteCmd, nil, pw, stderr)
	_ = pw.Close() // the child is gone — or never started: hand the codec its EOF
	codecErr := <-codecDone
	return cr.n, transferResult(sshErr, codecErr, wording)
}

// transferToVM runs one one-shot copy stream into the vm, transferFromVM's
// mirror: pack, the copy path's codec (upload's tar.gz builder), writes the
// local tree across a module-owned pipe into remoteCmd's stdin, and the
// answer is the bytes that crossed the pipe and the one failure that
// outranks the other. The pipe is the module's — pack gets a writer, never
// a pipe end — and the codec's own error is the payload that hands the
// child its end: EOF on success, the raw error on failure, so a failure
// resurfaces through the child's own "run ssh:" report carrying the
// codec's words. compressed counts every byte that entered the pipe, so a
// copy path's report and the stream agree.
func transferToVM(creds *Credentials, vm *vmRow, remoteCmd string, stdout, stderr io.Writer, pack func(io.Writer) error, wording func(error) error) (compressed int64, err error) {
	pr, pw := io.Pipe()
	cw := &countWriter{w: pw}
	codecDone := make(chan error, 1)
	go func() {
		err := pack(cw)
		// done or failed, the child hears it from the pipe: EOF on
		// success, or the codec's raw error — the payload that can
		// resurface through the child's own "run ssh:" report, where it
		// must read as the codec said it
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		codecDone <- err
	}()
	// the codec runs concurrently with the transfer: its pipe writes
	// simply block until the ssh child is alive and reading
	sshErr := execInVM(creds, vm, remoteCmd, pr, stdout, stderr)
	_ = pr.Close() // the child is gone — or never started: a codec blocked mid-write fails instead of hanging
	codecErr := <-codecDone
	return cw.n, transferResult(sshErr, codecErr, wording)
}
