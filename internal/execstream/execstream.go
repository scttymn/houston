// Package execstream is how a command's output crosses Mission Control's
// API for houston exec --server (docs/plans/exec-server.md): framed
// records of stdout, stderr and, last, the exit code. Frames rather than
// an HTTP trailer, since a proxy on the way (Cloudflare's) may drop
// trailers.
//
// A frame is one byte of kind, four bytes of big-endian length, then that
// many bytes: the output, or for Exit the code in decimal.
package execstream

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// ContentType marks an answer in frames.
const ContentType = "application/vnd.houston.exec-stream"

// Header is set on every answer of the exec route, its errors too, so a
// client can tell a 404 from it from a server too old to have it.
const Header = "Houston-Exec"

// The kinds of frame.
const (
	Stdout byte = 1
	Stderr byte = 2
	Exit   byte = 3
)

// maxFrame is the largest frame Read accepts.
const maxFrame = 1 << 20

// Writer writes frames to w, one at a time (stdout and stderr may be
// written from two goroutines). flush, if not nil, runs after each frame.
type Writer struct {
	mu    sync.Mutex
	w     io.Writer
	flush func()
}

func NewWriter(w io.Writer, flush func()) *Writer { return &Writer{w: w, flush: flush} }

func (w *Writer) frame(kind byte, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	head := [5]byte{kind}
	binary.BigEndian.PutUint32(head[1:], uint32(len(p)))
	if _, err := w.w.Write(head[:]); err != nil {
		return err
	}
	if _, err := w.w.Write(p); err != nil {
		return err
	}
	if w.flush != nil {
		w.flush()
	}
	return nil
}

// Stdout and Stderr are writers of that kind of frame.
func (w *Writer) Stdout() io.Writer { return kindWriter{w, Stdout} }
func (w *Writer) Stderr() io.Writer { return kindWriter{w, Stderr} }

// Exit writes the last frame: the command's exit code.
func (w *Writer) Exit(code int) error { return w.frame(Exit, []byte(strconv.Itoa(code))) }

type kindWriter struct {
	w    *Writer
	kind byte
}

func (k kindWriter) Write(p []byte) (int, error) {
	for rest := p; len(rest) > 0; {
		n := min(len(rest), maxFrame)
		if err := k.w.frame(k.kind, rest[:n]); err != nil {
			return len(p) - len(rest), err
		}
		rest = rest[n:]
	}
	return len(p), nil
}

// ErrCutOff is a stream that ended before its exit code.
var ErrCutOff = errors.New("the connection ended before the command did")

// Read copies r's frames to stdout and stderr until the exit frame, and
// returns its code.
func Read(r io.Reader, stdout, stderr io.Writer) (int, error) {
	var head [5]byte
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, ErrCutOff
			}
			return 0, err
		}
		size := binary.BigEndian.Uint32(head[1:])
		if size > maxFrame {
			return 0, fmt.Errorf("a frame of %d bytes is too large", size)
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, ErrCutOff
			}
			return 0, err
		}
		switch head[0] {
		case Stdout:
			if _, err := stdout.Write(body); err != nil {
				return 0, err
			}
		case Stderr:
			if _, err := stderr.Write(body); err != nil {
				return 0, err
			}
		case Exit:
			code, err := strconv.Atoi(string(body))
			if err != nil {
				return 0, fmt.Errorf("an exit code of %q", body)
			}
			return code, nil
		default:
			return 0, fmt.Errorf("a frame of kind %d", head[0])
		}
	}
}
