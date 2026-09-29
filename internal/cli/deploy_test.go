package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/houston/internal/deploy"
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

func TestDeployRunsAsTheHoustonUser(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.ssh/id_ed25519
	t.Setenv("HOUSTON_TOKEN", "a-token")
	t.Setenv("HOUSTON_URL", "http://127.0.0.1:1") // nothing listens; must not be called
	var stdout, stderr bytes.Buffer

	code := Main([]string{"deploy"}, strings.NewReader(""), &stdout, &stderr, &fakeDocker{})

	if code != exitFailure || !strings.Contains(stderr.String(), "houston user") || strings.Contains(stderr.String(), "Mission Control") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr.String())
	}
}

// houston rebuild, on the server as houston: the deploy, built fresh.
func TestRebuildLocal(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("key"), 0o600)
	t.Setenv("HOME", home)
	t.Setenv("HOUSTON_TOKEN", "a-token")
	t.Setenv("HOUSTON_URL", "http://127.0.0.1:1")
	var got []bool
	was := deployRun
	deployRun = func(_ context.Context, o deploy.Options, _ deploy.Deps) int { got = append(got, o.Fresh); return 0 }
	t.Cleanup(func() { deployRun = was })
	var stdout, stderr bytes.Buffer
	for _, cmd := range []string{"rebuild", "deploy"} {
		if code := Main([]string{cmd}, strings.NewReader(""), &stdout, &stderr, &fakeDocker{}); code != 0 {
			t.Fatalf("%s: exit %d\n%s", cmd, code, stderr.String())
		}
	}
	if len(got) != 2 || !got[0] || got[1] {
		t.Errorf("fresh: %v, want rebuild true, deploy false", got)
	}
}
