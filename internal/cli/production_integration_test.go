//go:build integration

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDevProductionIntegration runs `houston dev --production`: the app runs
// the production stage with its files baked into the image, not mounted.
func TestDevProductionIntegration(t *testing.T) {
	dir, composeFile, name := fixtureProject(t, "")
	must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_SECRET=prod-secret\n"), 0o644))
	bin := buildHouston(t)

	var out bytes.Buffer
	cmd := exec.Command(bin, "-f", composeFile, "dev", "--production")
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)

	deadline := time.Now().Add(3 * time.Minute)
	for !serviceRunning(name, "app") {
		select {
		case err := <-exited:
			t.Fatalf("houston dev --production exited early (%v):\n%s", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("app never started:\n%s", out.String())
		}
		time.Sleep(time.Second)
	}

	exec_ := func(args ...string) (string, error) {
		b, err := exec.Command("docker", append([]string{"compose", "-p", name, "--project-directory", dir,
			"-f", composeFile, "-f", filepath.Join(dir, ".houston", "compose.production.yml"), "exec", "-T", "app"}, args...)...).Output()
		return strings.TrimSpace(string(b)), err
	}
	if stage, err := exec_("cat", "/stage"); err != nil || stage != "production" {
		t.Errorf("app runs the %q stage (%v), want production", stage, err)
	}
	if _, err := exec_("test", "-e", "/app/compose.yml"); err != nil {
		t.Errorf("the app's files aren't baked into the production image: %v", err)
	}
	must(t, os.WriteFile(filepath.Join(dir, "after-start.txt"), []byte("x"), 0o644))
	if _, err := exec_("test", "-e", "/app/after-start.txt"); err == nil {
		t.Errorf("a file written on the host after start is visible: the code is still bind-mounted")
	}

	must(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGINT))
	select {
	case <-exited:
		if err := syscall.Kill(-cmd.Process.Pid, 0); err != syscall.ESRCH {
			t.Errorf("houston returned while docker was still running (kill -0: %v)", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("houston dev --production didn't stop within 30s:\n%s", out.String())
	}
}
