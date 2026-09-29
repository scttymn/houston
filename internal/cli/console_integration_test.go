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

// TestConsoleLogsIntegration runs the real console and logs commands against
// the app container `houston dev` started. The fixture's console is `sh`, so
// piped stdin drives it.
func TestConsoleLogsIntegration(t *testing.T) {
	dir, composeFile, name := fixtureProject(t, "")
	must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_SECRET=dev-secret\n"), 0o644))
	bin := buildHouston(t)

	houston := func(stdin string, args ...string) (string, int) {
		cmd := exec.Command(bin, append([]string{"-f", composeFile}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), exitCode(err)
	}

	var devOut bytes.Buffer
	dev := exec.Command(bin, "-f", composeFile, "dev")
	dev.Stdout, dev.Stderr = &devOut, &devOut
	dev.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(t, dev.Start())
	devExited := make(chan error, 1)
	go func() { devExited <- dev.Wait() }()
	defer syscall.Kill(-dev.Process.Pid, syscall.SIGKILL)

	deadline := time.Now().Add(3 * time.Minute)
	for !serviceRunning(name, "app") {
		select {
		case err := <-devExited:
			t.Fatalf("houston dev exited early (%v):\n%s", err, devOut.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("app never started:\n%s", devOut.String())
		}
		time.Sleep(time.Second)
	}

	if out, code := houston("", "logs"); code != 0 || !strings.Contains(out, "houston-dev-started") {
		t.Errorf("logs: exit %d, output:\n%s", code, out)
	}
	if out, code := houston("cat /stage\n", "console"); code != 0 || strings.TrimSpace(out) != "dev" {
		t.Errorf("console: exit %d, output %q; want 0 and dev", code, out)
	}
	if out, code := houston("exit 3\n", "console"); code != 3 {
		t.Errorf("console: exit %d, want 3:\n%s", code, out)
	}
	// houston exec in the running app (docs/plans/exec-and-init-name.md, row 12).
	if out, code := houston("", "exec", "cat", "/stage"); code != 0 || strings.TrimSpace(out) != "dev" {
		t.Errorf("exec: exit %d, output %q; want 0 and dev", code, out)
	}
	if out, code := houston("", "exec", "sh", "-c", "echo from-exec > /data/exec-was-here"); code != 0 {
		t.Errorf("exec writing /data: exit %d:\n%s", code, out)
	}

	var followOut bytes.Buffer
	follow := exec.Command(bin, "-f", composeFile, "logs", "-f")
	follow.Stdout, follow.Stderr = &followOut, &followOut
	follow.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(t, follow.Start())
	followExited := make(chan error, 1)
	go func() { followExited <- follow.Wait() }()
	time.Sleep(2 * time.Second)
	must(t, syscall.Kill(-follow.Process.Pid, syscall.SIGINT))
	select {
	case <-followExited:
		if err := syscall.Kill(-follow.Process.Pid, 0); err != syscall.ESRCH {
			syscall.Kill(-follow.Process.Pid, syscall.SIGKILL)
			t.Errorf("houston logs -f returned while docker was still running (kill -0: %v)", err)
		}
	case <-time.After(10 * time.Second):
		syscall.Kill(-follow.Process.Pid, syscall.SIGKILL)
		t.Fatalf("logs -f didn't exit within 10s of Ctrl-C:\n%s", followOut.String())
	}
	if !strings.Contains(followOut.String(), "houston-dev-started") {
		t.Errorf("logs -f output:\n%s", followOut.String())
	}

	must(t, syscall.Kill(-dev.Process.Pid, syscall.SIGINT))
	select {
	case <-devExited:
	case <-time.After(30 * time.Second):
		t.Fatalf("houston dev didn't stop:\n%s", devOut.String())
	}

	if out, code := houston("cat /stage\n", "console"); code != 1 || !strings.Contains(out, "isn't running") {
		t.Errorf("console after dev stopped: exit %d, output:\n%s", code, out)
	}
	// Not running: a one-off in the same project, with the same data, which
	// stops the database it started.
	if out, code := houston("", "exec", "cat", "/stage", "/data/exec-was-here"); code != 0 || !strings.Contains(out, "dev\nfrom-exec") {
		t.Errorf("exec after dev stopped: exit %d, output %q; want dev and the file written before", code, out)
	}
	if out, code := houston("", "exec", "sh", "-c", "exit 5"); code != 5 {
		t.Errorf("exec's exit code: %d, want 5:\n%s", code, out)
	}
	if serviceRunning(name, "db") || serviceRunning(name, "app") {
		t.Errorf("exec left the project running")
	}
	if out, code := houston("", "logs"); code != 0 || !strings.Contains(out, "houston-dev-started") {
		t.Errorf("logs after dev stopped: exit %d, output:\n%s", code, out)
	}
}

// serviceRunning reports whether project's service has a running container.
func serviceRunning(project, service string) bool {
	out, _ := exec.Command("docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+project,
		"--filter", "label=com.docker.compose.service="+service).Output()
	return strings.TrimSpace(string(out)) != ""
}
