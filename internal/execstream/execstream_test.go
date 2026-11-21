package execstream

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// The bytes on the wire, exactly: kind, big-endian length, payload.
func TestWriterFrames(t *testing.T) {
	var buf bytes.Buffer
	flushes := 0
	w := NewWriter(&buf, func() { flushes++ })
	w.Stdout().Write([]byte("hi\n"))
	w.Stderr().Write([]byte("oops"))
	w.Exit(42)
	want := "\x01\x00\x00\x00\x03hi\n" + "\x02\x00\x00\x00\x04oops" + "\x03\x00\x00\x00\x0242"
	if buf.String() != want {
		t.Errorf("wrote %q\nwant  %q", buf.String(), want)
	}
	if flushes != 3 {
		t.Errorf("flushed %d times, want after each frame", flushes)
	}
}

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	big := strings.Repeat("x", maxFrame+10) // split into two frames
	w.Stdout().Write([]byte("out "))
	w.Stderr().Write([]byte("err"))
	w.Stdout().Write([]byte(big))
	w.Exit(-3)
	var out, errOut bytes.Buffer
	code, err := Read(&buf, &out, &errOut)
	if err != nil || code != -3 || out.String() != "out "+big || errOut.String() != "err" {
		t.Errorf("code %d, err %v, stdout %d bytes, stderr %q", code, err, out.Len(), errOut.String())
	}
}

func TestReadCutOff(t *testing.T) {
	for name, s := range map[string]string{
		"no exit frame":   "\x01\x00\x00\x00\x02hi",
		"half a header":   "\x01\x00\x00",
		"half a frame":    "\x01\x00\x00\x00\x09hi",
		"nothing at all":  "",
		"half the exit":   "\x03\x00\x00\x00\x02" + "4",
		"output then cut": "\x02\x00\x00\x00\x01e\x03",
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if _, err := Read(strings.NewReader(s), &out, &errOut); !errors.Is(err, ErrCutOff) {
				t.Errorf("err = %v, want ErrCutOff", err)
			}
		})
	}
}

func TestReadRefusesNonsense(t *testing.T) {
	for name, s := range map[string]string{
		"unknown kind":  "\x09\x00\x00\x00\x00",
		"huge frame":    "\x01\x7f\x00\x00\x00",
		"code not text": "\x03\x00\x00\x00\x02ab",
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if _, err := Read(strings.NewReader(s), &out, &errOut); err == nil || errors.Is(err, ErrCutOff) {
				t.Errorf("err = %v, want a refusal", err)
			}
		})
	}
}
