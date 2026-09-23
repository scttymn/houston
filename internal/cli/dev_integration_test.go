//go:build integration

package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDevIntegration_BuildsServesAndStops runs the real houston binary against
// real Docker. It runs inside the `cli` dev container, whose repo mount has the
// same path as on the host, so the project must live under the repo (not /tmp)
// for the host daemon to see its bind mount.
func TestDevIntegration_BuildsServesAndStops(t *testing.T) {
	repo, err := filepath.Abs("../..")
	must(t, err)
	scratch := filepath.Join(repo, ".houston", "test-tmp")
	must(t, os.MkdirAll(scratch, 0o755))
	dir, err := os.MkdirTemp(scratch, "devapp-")
	must(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	name := "houston-it-" + randomHex(t)
	for _, f := range []string{"Dockerfile", "compose.yml"} {
		b, err := os.ReadFile(filepath.Join("testdata", "devapp", f))
		must(t, err)
		b = bytes.ReplaceAll(b, []byte("NAME_SET_BY_TEST"), []byte(name))
		must(t, os.WriteFile(filepath.Join(dir, f), b, 0o644))
	}
	composeFile := filepath.Join(dir, "compose.yml")

	bin := filepath.Join(t.TempDir(), "houston")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/sevenmoons/houston/cmd/houston").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	compose := func(args ...string) (string, error) {
		base := []string{"compose", "-p", name, "--project-directory", dir, "-f", composeFile, "-f", filepath.Join(dir, ".houston", "compose.dev.yml")}
		out, err := exec.Command("docker", append(base, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-p", name, "down", "-v", "--rmi", "local", "--remove-orphans").Run()
	})

	var output bytes.Buffer
	cmd := exec.Command(bin, "dev", "-f", composeFile)
	cmd.Stdout, cmd.Stderr = &output, &output
	// Own process group, so the signal below reaches houston and compose
	// together, the way a terminal's Ctrl-C does.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Wait for the app container (first run pulls busybox and builds).
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if out, err := compose("exec", "-T", "app", "cat", "/stage"); err == nil {
			if out != "dev" {
				t.Fatalf("app runs the %q stage, want dev", out)
			}
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("houston dev exited early (%v):\n%s", err, output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("app never came up:\n%s", output.String())
		}
		time.Sleep(time.Second)
	}

	must(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("live"), 0o644))
	if out, err := compose("exec", "-T", "app", "cat", "/app/hello.txt"); err != nil || out != "live" {
		t.Errorf("bind mount isn't live: %q %v", out, err)
	}
	var db string
	for i := 0; i < 20; i++ {
		if db, err = compose("exec", "-T", "app", "sh", "-c", "wget -qO- $DB_URL"); err == nil && db == "db-ok" {
			break
		}
		time.Sleep(time.Second)
	}
	if db != "db-ok" {
		t.Errorf("${DB_HOST:-db} didn't reach the db service: %q %v", db, err)
	}

	must(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGINT))
	select {
	case <-exited:
		// houston must have waited for compose: nothing else may be left in
		// the process group once houston is gone.
		if err := syscall.Kill(-cmd.Process.Pid, 0); err != syscall.ESRCH {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			t.Errorf("houston returned while docker compose was still running (kill -0: %v)", err)
		}
	case <-time.After(30 * time.Second):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatalf("houston dev didn't exit within 30s of Ctrl-C:\n%s", output.String())
	}
	if out, _ := compose("ps", "-q"); out != "" {
		t.Errorf("containers still running after Ctrl-C: %s", out)
	}
}

func randomHex(t *testing.T) string {
	b := make([]byte, 4)
	_, err := rand.Read(b)
	must(t, err)
	return hex.EncodeToString(b)
}
