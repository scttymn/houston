package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeployNeedsTheRunnerToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HOUSTON_TOKEN", "")
	var stdout, stderr bytes.Buffer
	d := &fakeDocker{}

	code := Main([]string{"deploy"}, strings.NewReader(""), &stdout, &stderr, d)

	if code != exitFailure {
		t.Errorf("exit %d, want %d", code, exitFailure)
	}
	for _, want := range []string{"HOUSTON_TOKEN", ".config/houston/runner-token"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr doesn't mention %s:\n%s", want, stderr.String())
		}
	}
	if d.calls() != 0 {
		t.Errorf("docker ran %d times", d.calls())
	}
}
