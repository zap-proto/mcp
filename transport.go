// Framing — length-prefixed frames over an io.ReadWriteCloser.
//
// A 4-byte big-endian length prefix precedes each frame, matching
// zap-proto/http. Both protocols ride the same transport and must agree on
// where a frame ends; they carry their own copy of that agreement rather than
// one importing the other, because a protocol that had to depend on a sibling
// protocol to find its frame boundaries would make the wire's shape a property
// of whichever sibling happened to define it first.
//
// Each exchange is one request frame followed by one response frame on the
// same connection. Connections are reused; the transport does not pipeline.

package zapmcp

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// Max bounds an inbound frame, so a peer announcing a multi-gigabyte length
// prefix costs a refusal rather than the process.
const Max = 64 << 20 // 64 MiB

// bufSize is the per-connection read buffer. Larger than bufio's 4 KiB default
// so back-to-back small frames drain with fewer read syscalls.
const bufSize = 16 << 10

// read reads one length-prefixed frame into buf, growing it only when the frame
// exceeds its capacity, and returns the frame slice. The caller reassigns its
// buffer from the return value so a grown buffer persists across calls — the
// steady-state read is allocation-free. [Unmarshal] copies what it keeps, so
// the buffer is free to reuse on the next call.
func read(r *bufio.Reader, buf []byte) ([]byte, error) {
	// Peek the prefix out of the bufio buffer rather than io.ReadFull into a
	// local array: passing a stack array through the io.Reader interface forces
	// it to the heap on every call. Peek returns a view, no copy, no escape.
	hdr, err := r.Peek(4)
	if err != nil {
		return buf, err
	}
	n := binary.BigEndian.Uint32(hdr)
	if _, err := r.Discard(4); err != nil {
		return buf, err
	}
	if n == 0 {
		return buf, fmt.Errorf("zapmcp: zero-length frame")
	}
	if n > Max {
		return buf, fmt.Errorf("zapmcp: frame size %d exceeds Max=%d", n, Max)
	}
	if uint32(cap(buf)) < n {
		buf = make([]byte, n)
	} else {
		buf = buf[:n]
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return buf, fmt.Errorf("zapmcp: short frame: %w", err)
	}
	return buf, nil
}

// write writes a prefix + frame in ONE Write. buf must have been built with 4
// leading bytes reserved for the prefix, which halves the per-message syscall
// count against writing the header and body separately.
func write(w io.Writer, buf []byte) error {
	n := len(buf) - 4
	if n <= 0 {
		return fmt.Errorf("zapmcp: refusing to write zero-length frame")
	}
	if uint64(n) > Max {
		return fmt.Errorf("zapmcp: frame size %d exceeds Max=%d", n, Max)
	}
	binary.BigEndian.PutUint32(buf[0:4], uint32(n))
	_, err := w.Write(buf)
	return err
}
