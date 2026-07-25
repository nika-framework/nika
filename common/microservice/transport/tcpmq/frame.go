package tcpmq

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The wire format is a 4-byte big-endian length prefix followed by exactly that
// many bytes of JSON envelope.
//
// A length prefix is used rather than a delimiter (newline, NUL) because the
// payload is JSON and JSON can legally contain any byte inside a string once
// escaped — a delimiter would need escaping and unescaping on every frame, and
// getting that wrong is a framing desynchronisation bug that only shows up under
// unusual payloads. With a length prefix the reader always knows exactly how many
// bytes belong to the current message, so a malformed *payload* never desynchronises
// the *stream*: we can log it and read the next frame.
const frameHeaderBytes = 4

// DefaultMaxFrameBytes bounds a single frame. It matches the envelope decoder's
// own limit: a frame larger than that can never decode successfully, so reading
// it would only waste memory.
const DefaultMaxFrameBytes = 8 << 20 // 8 MiB

// ErrFrameTooLarge is returned when a peer announces a frame beyond the limit.
// It is fatal for the connection: we deliberately do not skip the body, because
// the announced length cannot be trusted, so the stream position is unknowable
// from here on.
var ErrFrameTooLarge = errors.New("tcpmq: frame exceeds the configured maximum")

// ErrZeroFrame is returned for a frame that announces no payload. It cannot be a
// valid envelope and almost always means a peer speaking a different protocol.
var ErrZeroFrame = errors.New("tcpmq: zero-length frame")

// writeFrame emits one length-prefixed frame and flushes it.
//
// Callers must hold the connection's write mutex: the header and the body are two
// writes, and an interleaved write from another goroutine between them would
// splice a foreign payload into this frame's declared length, corrupting every
// subsequent frame on the connection.
func writeFrame(w *bufio.Writer, payload []byte, max int) error {
	if len(payload) == 0 {
		return ErrZeroFrame
	}
	if max <= 0 {
		max = DefaultMaxFrameBytes
	}
	if len(payload) > max {
		return fmt.Errorf("%w: %d > %d bytes", ErrFrameTooLarge, len(payload), max)
	}

	var header [frameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return w.Flush()
}

// readFrameSize reads the length prefix and validates it against max.
//
// This is the security-critical half of the reader. The length is attacker
// controlled, so it is checked *before* any allocation happens: `make([]byte, n)`
// with an unvalidated n is a one-packet remote memory-exhaustion attack — four
// bytes of 0xFF ask the process for 4 GiB.
//
// The comparison is done in uint64 so it is also correct on 32-bit platforms,
// where converting a large uint32 to int would otherwise produce a negative
// number and pass a naive `n > max` check.
func readFrameSize(r *bufio.Reader, max int) (int, error) {
	var header [frameHeaderBytes]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, err
	}

	n := binary.BigEndian.Uint32(header[:])
	if n == 0 {
		return 0, ErrZeroFrame
	}
	if max <= 0 {
		max = DefaultMaxFrameBytes
	}
	if uint64(n) > uint64(max) {
		return 0, fmt.Errorf("%w: %d > %d bytes", ErrFrameTooLarge, n, max)
	}
	return int(n), nil
}

// readFrameBody reads exactly size bytes. size must already have been bounded by
// readFrameSize.
func readFrameBody(r *bufio.Reader, size int) ([]byte, error) {
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		// A frame that announced more than it delivered is a truncated frame, not
		// a clean end of stream; surface it as such so the caller drops the
		// connection instead of treating it as a graceful disconnect.
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return payload, nil
}

// readFrame reads one whole frame. The server splits the two halves so it can
// apply a generous idle deadline while waiting for a header and a tighter read
// deadline once a header has arrived; the client's reply reader has no such need.
func readFrame(r *bufio.Reader, max int) ([]byte, error) {
	size, err := readFrameSize(r, max)
	if err != nil {
		return nil, err
	}
	return readFrameBody(r, size)
}
