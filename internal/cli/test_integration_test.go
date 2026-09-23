//go:build integration

package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestTestIntegration_PassFailInterruptCleanup runs the real `houston test`
// against Docker: the fixture's own test checks the test stage, the db
// service, a throwaway secret, no bind mount and a fresh volume.
func TestTestIntegration_PassFailInterruptCleanup(t *testing.T) {
	bin := buildHouston(t)

	t.Run("passes twice with fresh volumes, ignoring .env", func(t *testing.T) {
		dir, composeFile, name := fixtureProject(t, "")
		must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_SECRET=from-dotenv\nDB_HOST=nowhere\n"), 0o644))
		for i := 1; i <= 2; i++ {
			out, err := exec.Command(bin, "test", "-f", composeFile).CombinedOutput()
			if code := exitCode(err); code != 0 {
				t.Fatalf("run %d: exit %d, want 0:\n%s", i, code, out)
			}
			if left := composeProjects(name); len(left) != 0 {
				t.Errorf("run %d left projects behind: %v", i, left)
			}
		}
	})

	t.Run("failing test exits with its code", func(t *testing.T) {
		_, composeFile, name := fixtureProject(t, "exit 7")
		out, err := exec.Command(bin, "test", "-f", composeFile).CombinedOutput()
		if code := exitCode(err); code != 7 {
			t.Errorf("exit %d, want 7:\n%s", code, out)
		}
		if left := composeProjects(name); len(left) != 0 {
			t.Errorf("left projects behind: %v", left)
		}
	})

	t.Run("Ctrl-C tears down", func(t *testing.T) {
		_, composeFile, name := fixtureProject(t, "sleep 300")
		var out bytes.Buffer
		cmd := exec.Command(bin, "test", "-f", composeFile)
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		must(t, cmd.Start())
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		deadline := time.Now().Add(3 * time.Minute)
		for !appRunning(name) {
			select {
			case err := <-exited:
				t.Fatalf("houston test exited before the test started (%v):\n%s", err, out.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("test container never started:\n%s", out.String())
			}
			time.Sleep(time.Second)
		}

		must(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGINT))
		select {
		case <-exited:
			if err := syscall.Kill(-cmd.Process.Pid, 0); err != syscall.ESRCH {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				t.Errorf("houston returned while docker was still running (kill -0: %v)", err)
			}
		case <-time.After(60 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			t.Fatalf("houston test didn't exit within 60s of Ctrl-C:\n%s", out.String())
		}
		if left := composeProjects(name); len(left) != 0 {
			t.Errorf("Ctrl-C left projects behind: %v\n%s", left, out.String())
		}
	})
}

// appRunning reports whether a throwaway test container for name is running.
func appRunning(name string) bool {
	out, _ := exec.Command("docker", "ps", "--format", `{{.Label "com.docker.compose.project"}} {{.Label "com.docker.compose.service"}}`).Output()
	for _, line := range bytes.Split(out, []byte("\n")) {
		fields := bytes.Fields(line)
		if len(fields) == 2 && bytes.HasPrefix(fields[0], []byte(name+"-test-")) && string(fields[1]) == "app" {
			return true
		}
	}
	return false
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}
