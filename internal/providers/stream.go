package providers

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// maxStreamLineBytes bounds a single stream line. Tool-call argument fragments
// can be large but not unbounded; a cap prevents a hostile or buggy upstream
// from exhausting memory through one endless line.
const maxStreamLineBytes = 4 << 20

// sseEvent is one parsed Server-Sent Events frame.
type sseEvent struct {
	// Event is the "event:" field, empty for the default message event.
	Event string
	// Data is the concatenation of all "data:" lines, joined with newlines per
	// the SSE specification.
	Data string
	// ID is the "id:" field, used to resume a stream.
	ID string
	// Retry is the server-suggested reconnection delay in milliseconds.
	Retry int
}

// done reports whether the frame is the OpenAI end-of-stream sentinel.
func (e sseEvent) done() bool {
	return strings.TrimSpace(e.Data) == "[DONE]"
}

// empty reports whether the frame carried no payload and should be skipped.
func (e sseEvent) empty() bool {
	return strings.TrimSpace(e.Data) == "" && e.Event == ""
}

// sseReader parses an SSE stream incrementally.
//
// It is a hand-written parser rather than a library because the only features
// CoreRouter needs are the "event", "data" and comment fields, and because the
// streaming path is hot enough that avoiding reflection-based decoding is worth
// the few dozen lines.
type sseReader struct {
	scanner *bufio.Scanner
	// pending holds a decoded frame when the caller asked for the next event
	// but the reader had already advanced past it.
	eof bool
}

// newSSEReader wraps a reader in an SSE parser.
func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxStreamLineBytes)
	// Split on newlines; Scanner strips \n and leaves a trailing \r to trim.
	sc.Split(bufio.ScanLines)
	return &sseReader{scanner: sc}
}

// Next returns the next event.
//
// It returns (event, true, nil) for a frame, (zero, false, nil) at clean EOF and
// (zero, false, err) on a read error. A clean EOF without a trailing blank line is
// common because many providers simply close the connection, so it is not an
// error.
func (r *sseReader) Next() (sseEvent, bool, error) {
	var (
		ev        sseEvent
		sawField  bool
		dataLines []string
	)

	for {
		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return sseEvent{}, false, err
			}
			// EOF: flush a frame that was not terminated by a blank line.
			if sawField {
				ev.Data = strings.Join(dataLines, "\n")
				return ev, true, nil
			}
			return sseEvent{}, false, nil
		}

		line := r.scanner.Text()
		// A carriage return is left in place by ScanLines when the stream uses
		// CRLF, which is legal in SSE.
		line = strings.TrimSuffix(line, "\r")

		if line == "" {
			// Blank line dispatches the buffered frame.
			if !sawField {
				continue
			}
			ev.Data = strings.Join(dataLines, "\n")
			return ev, true, nil
		}

		if strings.HasPrefix(line, ":") {
			// Comment. Providers commonly send ": ping" as a keepalive; it must
			// not reset the idle timer as if it were content.
			continue
		}

		field, value, found := strings.Cut(line, ":")
		if !found {
			// A field with no colon has an empty value.
			field, value = line, ""
		}
		// A single leading space after the colon is part of the framing.
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "event":
			ev.Event = value
			sawField = true
		case "data":
			dataLines = append(dataLines, value)
			sawField = true
		case "id":
			ev.ID = value
			sawField = true
		case "retry":
			ev.Retry = atoiSafe(value)
			sawField = true
		default:
			// Unknown fields are ignored per the specification.
		}
	}
}

// Close releases any buffered state. It does not close the underlying reader,
// which is owned by the HTTP response.
func (r *sseReader) Close() { r.scanner = nil }

// ndjsonReader reads newline-delimited JSON, the format Ollama uses for
// streaming chat responses.
type ndjsonReader struct {
	scanner *bufio.Scanner
}

// newNDJSONReader wraps a reader in an NDJSON parser.
func newNDJSONReader(r io.Reader) *ndjsonReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxStreamLineBytes)
	return &ndjsonReader{scanner: sc}
}

// Next returns the next non-empty line's bytes.
func (r *ndjsonReader) Next() ([]byte, bool, error) {
	for r.scanner.Scan() {
		line := bytes.TrimSpace(r.scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		// The scanner reuses its buffer, so copy before returning.
		out := make([]byte, len(line))
		copy(out, line)
		return out, true, nil
	}
	if err := r.scanner.Err(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

// atoiSafe parses a decimal integer, returning zero on any problem. Stream
// metadata must never abort a response.
func atoiSafe(s string) int {
	n := 0
	neg := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '-' && i == 0:
			neg = true
		case c >= '0' && c <= '9':
			n = n*10 + int(c-'0')
		default:
			return 0
		}
	}
	if neg {
		return -n
	}
	return n
}

// streamError wraps an error returned by the caller's StreamHandler so the
// adapter can distinguish "the consumer stopped" from "the provider failed" and
// avoid mislabelling a client disconnect as an upstream incident.
type streamError struct {
	err error
}

// Error implements the error interface.
func (e *streamError) Error() string { return e.err.Error() }

// Unwrap exposes the consumer's error.
func (e *streamError) Unwrap() error { return e.err }

// newStreamError marks an error as originating from the consumer.
func newStreamError(err error) error { return &streamError{err: err} }

// isStreamError reports whether err came from the consumer's handler.
func isStreamError(err error) bool {
	_, ok := err.(*streamError)
	return ok
}
